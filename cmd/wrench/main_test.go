package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ordo-systems/wrench/internal/app/mocks"
	"github.com/spf13/cobra"
)

// run executes the command tree with args and returns the exit code, stdout and stderr.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	code := execute(context.Background(), root, args, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		code     int
		stdout   string
		stderrIn string
	}{
		{name: "version", args: []string{"version"}, stdout: "wrench dev (unknown)\n"},
		{name: "no args", code: exitUsage, stderrIn: "a subcommand is required"},
		{name: "run alone", args: []string{"run"}, code: exitUsage},
		{name: "run unknown", args: []string{"run", "foo"}, code: exitUsage, stderrIn: `unknown command "foo"`},
		{name: "mock alone", args: []string{"mock"}, code: exitUsage},
		{name: "unknown command", args: []string{"frob"}, code: exitUsage},
		{name: "restore two snapshots", args: []string{"restore", "a", "b"}, code: exitUsage},
		{name: "healthcheck without url", args: []string{"healthcheck"}, code: exitUsage},
		{name: "unknown flag", args: []string{"run", "server", "--nope"}, code: exitUsage, stderrIn: "Usage:"},
		{name: "bad restore source", args: []string{"restore", "--from", "ftp"}, code: exitUsage,
			stderrIn: `--from must be s3 or local, got "ftp"`},
		{name: "mock llm args", args: []string{"mock", "llm", "x"}, code: exitUsage},
		{name: "runtime error", args: []string{"healthcheck", "http://127.0.0.1:1/healthz"}, code: exitError,
			stderrIn: "wrench: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tt.args...)
			if code != tt.code {
				t.Fatalf("exit %d, want %d; stderr: %s", code, tt.code, stderr)
			}
			if tt.stdout != "" && stdout != tt.stdout {
				t.Errorf("stdout %q, want %q", stdout, tt.stdout)
			}
			if !strings.Contains(stderr, tt.stderrIn) {
				t.Errorf("stderr %q, want it to contain %q", stderr, tt.stderrIn)
			}
		})
	}
}

// The binary and command are named wrench.
func TestHelpNamesTheCommand(t *testing.T) {
	_, stdout, stderr := run(t, "--help")
	if out := stdout + stderr; !strings.Contains(out, "Usage:\n  wrench [flags]\n  wrench [command]\n") {
		t.Fatalf("help = %q", out)
	}
}

func TestResolveConfig(t *testing.T) {
	tests := []struct {
		name, env string
		args      []string
		want      string
	}{
		{name: "default", want: "config.yaml"},
		{name: "from env", env: "/etc/w.yaml", want: "/etc/w.yaml"},
		{name: "flag beats env", env: "/etc/w.yaml", args: []string{"--config", "x.yaml"}, want: "x.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WRENCH_CONFIG", tt.env)
			var path string
			c := &cobra.Command{Use: "x"}
			configFlag(c, &path)
			if err := c.ParseFlags(tt.args); err != nil {
				t.Fatal(err)
			}
			if got := resolveConfig(c, path); got != tt.want {
				t.Fatalf("config %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeGitLab(t *testing.T) {
	tests := []struct {
		name, issuer, public, wantIssuer, wantPublic string
	}{
		{"public defaults to issuer", "http://127.0.0.1:9000", "", "http://127.0.0.1:9000", "http://127.0.0.1:9000"},
		{"trailing slashes", "http://gitlab:9000/", "http://localhost:19000/", "http://gitlab:9000", "http://localhost:19000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := normalizeGitLab(mocks.GitLabOptions{Issuer: tt.issuer, PublicURL: tt.public})
			if o.Issuer != tt.wantIssuer || o.PublicURL != tt.wantPublic {
				t.Fatalf("issuer %q public %q, want %q %q", o.Issuer, o.PublicURL, tt.wantIssuer, tt.wantPublic)
			}
		})
	}
}
