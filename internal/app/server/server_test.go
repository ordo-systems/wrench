package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/env"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/pgtest"
)

// repoConfig is the checked-in policy.
const repoConfig = "../../../deploy/shared/config/config.yaml"

func vars(dsn string) map[string]string {
	return map[string]string{
		"WRENCH_LISTEN_ADDR":                   "127.0.0.1:0",
		"WRENCH_PUBLIC_URL":                    "http://localhost",
		"WRENCH_WEB_SESSION_TTL":               "12h",
		"WRENCH_CLIENT_KEYS_TTL":               "168h",
		"WRENCH_POSTGRES_DSN":                  dsn,
		"WRENCH_GITLAB_URL":                    "https://gitlab.example.com",
		"WRENCH_GITLAB_CLIENT_ID":              "id",
		"WRENCH_GITLAB_CLIENT_SECRET":          "secret",
		"WRENCH_GITLAB_SYNC_TOKEN":             "token",
		"WRENCH_GITLAB_SYNC_INTERVAL":          "5m",
		"WRENCH_SESSION_OFFLINE_AFTER":         "60s",
		"WRENCH_OPENROUTER_URL":                "http://127.0.0.1:1",
		"WRENCH_OPENROUTER_KEY":                "key",
		"WRENCH_UPSTREAM_TIMEOUT_CONNECT":      "1s",
		"WRENCH_UPSTREAM_TIMEOUT_FIRST_TOKEN":  "5s",
		"WRENCH_UPSTREAM_TIMEOUT_STREAM_IDLE":  "5s",
		"WRENCH_UPSTREAM_TIMEOUT_TOTAL":        "5s",
		"WRENCH_UPSTREAM_CONCURRENCY_PER_USER": "3",
		"WRENCH_UPSTREAM_CONCURRENCY_GLOBAL":   "60",
		"WRENCH_LOG_LEVEL":                     "error",
	}
}

// deviceKey signs email in the way a CLI sign-in ends: it binds the identity and issues a device key through the
// store of a running server, and returns the key.
func deviceKey(t *testing.T, dsn, email string) string {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	u, err := st.BindIdentity(ctx, email, "https://gitlab.example.com", email, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	key, hash := auth.NewToken(auth.KeyPrefix)
	if _, err := st.IssueClientKey(ctx, u.ID, "test", "test", hash, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return key
}

func lookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestRunRejectsInvalidConfigurationBeforeStarting(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "none.yaml")
	cases := map[string]struct {
		config string
		vars   map[string]string
		msg    string
	}{
		"missing policy names its path": {missing, vars("postgres://x"), missing},
		"missing env":                   {repoConfig, map[string]string{}, "WRENCH_POSTGRES_DSN"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := Run(ctx, c.config, lookup(c.vars), nil)
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestProbe(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		err := Probe(context.Background(), srv.URL)
		srv.Close()
		if (err == nil) != (status == http.StatusOK) {
			t.Fatalf("status %d: err %v", status, err)
		}
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	dsn := pgtest.Database(t) // skips without a test database
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, repoConfig, lookup(vars(dsn)), ready)
	}()

	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("not ready")
	}
	resp, err := http.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	var checks map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&checks)
	resp.Body.Close()
	// OpenRouter points at a closed port, so readiness must fail closed while PostgreSQL is fine.
	if resp.StatusCode != http.StatusServiceUnavailable || checks["postgres"] != "ok" || checks["openrouter"] != "unavailable" {
		t.Fatalf("readyz %d %v", resp.StatusCode, checks)
	}
	// The system view is for administrators: user3 is one in the repository config.
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/system", nil)
	req.Header.Set("Authorization", "Bearer "+deviceKey(t, dsn, "user3@example.com"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sys struct{ Version string }
	_ = json.NewDecoder(resp.Body).Decode(&sys)
	resp.Body.Close()
	if sys.Version != "dev (unknown)" {
		t.Fatalf("system version %q", sys.Version)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Only device keys and sessions authenticate; there is no static or development token.
func TestRunRejectsStaticTokens(t *testing.T) {
	dsn := pgtest.Database(t) // skips without a test database
	v := vars(dsn)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, repoConfig, lookup(v), ready)
	}()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("not ready")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer dev")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("static token accepted: %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunAppliesConfigurationChanges(t *testing.T) {
	dsn := pgtest.Database(t) // skips without a test database
	dir := t.TempDir()
	data, err := os.ReadFile(repoConfig)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	go func() { _ = run(ctx, path, lookup(vars(dsn)), ready, 50*time.Millisecond) }()
	var addr string
	select {
	case addr = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("not ready")
	}
	key := deviceKey(t, dsn, "user1@example.com")
	defaultModel := func() string {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/api/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Default string `json:"default"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out.Default
	}
	if got := defaultModel(); got != "glm-5.3" {
		t.Fatalf("basic default = %q", got)
	}
	data, _ = os.ReadFile(path)
	edited := strings.Replace(string(data), "  - email: user1@example.com\n", "  - email: user1@example.com\n    tier: premium\n", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for defaultModel() != "opus-5.5" {
		if time.Now().After(deadline) {
			t.Fatal("tier change not applied without a restart")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStartupWarnings(t *testing.T) {
	if w := startupWarnings(env.Env{HTTPOnly: true}); len(w) != 1 || w[0] != "HTTP-only mode: session cookies are not Secure; never use it in production" {
		t.Fatalf("HTTP-only warnings = %v", w)
	}
	if w := startupWarnings(env.Env{}); len(w) != 0 {
		t.Fatalf("default warnings = %v", w)
	}
}
