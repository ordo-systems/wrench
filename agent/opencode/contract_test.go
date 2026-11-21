//go:build opencode

// Package opencode holds the contract test that drives a real opencode binary against Wrench. opencode's Console
// API is private, so this test is what tells a new opencode release apart from a supported one.
package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/tests/login"
)

// userCode finds the code opencode prints for the device login, ignoring terminal colors.
var userCode = regexp.MustCompile(`Enter code: ([A-Z]{4}-[A-Z]{4})`)

const user = "user1@example.com"

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set; run make opencode-contract", name)
	}
	return v
}

// opencode runs the binary with private XDG directories and a PATH whose open and xdg-open do nothing, so the
// test touches neither the user's opencode setup nor a browser.
type opencode struct {
	t   *testing.T
	env []string
}

func newOpencode(t *testing.T) *opencode {
	t.Helper()
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Fatal("opencode is not installed")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e := append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, d := range []string{"CONFIG", "DATA", "STATE", "CACHE"} {
		e = append(e, "XDG_"+d+"_HOME="+filepath.Join(root, strings.ToLower(d)))
	}
	return &opencode{t: t, env: e}
}

func (o *opencode) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "opencode", args...)
	cmd.Env = o.env
	cmd.Dir = o.t.TempDir()
	return cmd
}

// signIn runs opencode's device login against wrench and approves the code it prints as user.
func (o *opencode) signIn(ctx context.Context, wrench, gitlab string) {
	o.t.Helper()
	cmd := o.command(ctx, "auth", "login", "opencode", "--standalone", "--method", "device", "--answer", "server="+wrench)
	out, err := cmd.StdoutPipe()
	if err != nil {
		o.t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		o.t.Fatal(err)
	}
	var transcript strings.Builder
	approved := false
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		line := scanner.Text()
		transcript.WriteString(line + "\n")
		if m := userCode.FindStringSubmatch(line); m != nil && !approved {
			if err := login.ApproveDevice(ctx, wrench, gitlab, user, m[1]); err != nil {
				o.t.Fatalf("approve %s: %v", m[1], err)
			}
			approved = true
		}
	}
	if err := cmd.Wait(); err != nil || !approved {
		o.t.Fatalf("opencode auth login: %v (approved %v)\n%s", err, approved, transcript.String())
	}
}

func TestOpencodeSignsInAndRunsThroughWrench(t *testing.T) {
	wrench, gitlab := env(t, "WRENCH_OC_URL"), env(t, "WRENCH_OC_GITLAB_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	oc := newOpencode(t)
	oc.signIn(ctx, wrench, gitlab)

	run := oc.command(ctx, "run", "--standalone", "-m", "wrench/glm-5.3", "say ping")
	run.Stdin = nil
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("opencode run: %v\n%s", err, out)
	}

	// The run is accounted under opencode's own session id.
	key, err := login.DeviceKey(ctx, wrench, gitlab, user)
	if err != nil {
		t.Fatal(err)
	}
	var sessions struct {
		Sessions []struct {
			ID    string `json:"id"`
			Usage struct {
				Attempts int64 `json:"attempts"`
			} `json:"usage"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(getJSON(t, wrench+"/api/sessions", key)), &sessions); err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions.Sessions {
		if strings.HasPrefix(s.ID, "ses_") && s.Usage.Attempts > 0 {
			return
		}
	}
	t.Fatalf("no opencode session with attempts: %+v", sessions.Sessions)
}

func getJSON(t *testing.T, url, key string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, body)
	}
	return string(body)
}
