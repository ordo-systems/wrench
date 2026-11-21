package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/catalog/catalogtest"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/env"
	upstreamprobe "github.com/ordo-systems/wrench/internal/probe"
	"github.com/ordo-systems/wrench/internal/proxy"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

// tokens maps bearer tokens to identities so tests can act as several users.
type tokens map[string]auth.Identity

func (t tokens) Authenticate(r *http.Request) (auth.Identity, error) {
	id, ok := t[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	return id, nil
}

var users = tokens{
	"admin": {Email: "admin@example.com", Role: config.RoleAdmin, Credential: auth.CredentialKey, StatusFresh: true},
	"alice": {Email: "alice@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true},
	"bob":   {Email: "bob@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true},
	// stale has a valid credential but no recently confirmed GitLab status.
	"stale": {Email: "alice@example.com", Role: config.RoleUser, Credential: auth.CredentialKey},
	// cookie authenticates like a dashboard session.
	"cookie":       {Email: "alice@example.com", Role: config.RoleUser, Credential: auth.CredentialSession, StatusFresh: true},
	"cookie-admin": {Email: "admin@example.com", Role: config.RoleAdmin, Credential: auth.CredentialSession, StatusFresh: true},
}

func get(t *testing.T, srv *httptest.Server, path, token string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

type fakeStore struct {
	Store
	pingErr error
}

func (f fakeStore) Ping(context.Context) error { return f.pingErr }

func (fakeStore) ListIncidents(context.Context, string, int) ([]store.Incident, error) {
	return nil, nil
}

// /readyz answers from the probe's cache: an OpenRouter that never answers cannot hold the endpoint up.
func TestReadyzDoesNotWaitForTheProbe(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	p := upstreamprobe.New(func(context.Context) error { <-block; return nil }, time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: users, Proxy: http.NotFoundHandler(),
		Dashboard: http.NotFoundHandler(), UpstreamReady: p.Err}))
	defer srv.Close()
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz %d", resp.StatusCode)
	}
}

func TestHealthAndReadiness(t *testing.T) {
	cases := []struct {
		name           string
		db, openrouter error
		readyz         int
		mentionsInErr  string
	}{
		{"ready", nil, nil, http.StatusOK, ""},
		{"db down", errors.New("x"), nil, http.StatusServiceUnavailable, "postgres"},
		{"openrouter down", nil, errors.New("x"), http.StatusServiceUnavailable, "openrouter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			openrouter := c.openrouter
			srv := httptest.NewServer(New(Deps{
				Store: fakeStore{pingErr: c.db}, Auth: users, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(),
				UpstreamReady: func(context.Context) error { return openrouter },
			}))
			defer srv.Close()
			if code := get(t, srv, "/healthz", "", nil); code != http.StatusOK {
				t.Fatalf("healthz %d", code)
			}
			resp, _ := http.Get(srv.URL + "/readyz")
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.readyz || !strings.Contains(string(body), c.mentionsInErr) {
				t.Fatalf("readyz %d: %s", resp.StatusCode, body)
			}
		})
	}
}

func TestAPIRequiresAuth(t *testing.T) {
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: users, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(), UpstreamReady: func(context.Context) error { return nil }}))
	defer srv.Close()
	for _, p := range []string{"/api/me", "/api/models", "/api/requests", "/api/sessions", "/api/usage?window=day"} {
		if code := get(t, srv, p, "", nil); code != http.StatusUnauthorized {
			t.Errorf("%s: %d", p, code)
		}
	}
	resp, _ := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("proxy without auth: %d", resp.StatusCode)
	}
}

func TestModelsPublishesTierCatalog(t *testing.T) {
	cfg := repoConfig(t)
	premium := tokens{"u4": {Email: "user4@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true},
		"u1": {Email: "user1@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true}}
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: premium, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(), Config: config.Fixed(cfg), Catalog: catalogtest.For(t, cfg, catalog.Entry{UpstreamID: "z-ai/glm-5.3", ContextLength: 128000, MaxCompletionTokens: 16384, PricePrompt: "0.0000006", PriceCompletion: "0.0000022"}), UpstreamReady: func(context.Context) error { return nil }}))
	defer srv.Close()
	var out struct {
		Default string           `json:"default"`
		Models  []map[string]any `json:"models"`
	}
	if code := get(t, srv, "/api/models", "u4", &out); code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	names := []string{}
	for _, m := range out.Models {
		names = append(names, m["name"].(string))
		if _, leaked := m["upstream_id"]; leaked {
			t.Fatal("upstream id published")
		}
	}
	if p := out.Models[1]["price_input_per_mtok"]; p != "0.6" || out.Models[1]["price_output_per_mtok"] != "2.2" {
		t.Fatalf("prices = %v / %v", p, out.Models[1]["price_output_per_mtok"])
	}
	if p, ok := out.Models[0]["price_input_per_mtok"]; !ok || p != nil {
		t.Fatalf("unpriced model must carry null, got %v (present %v)", p, ok)
	}
	if out.Default != "opus-5.5" || strings.Join(names, ",") != "opus-5.5,glm-5.3,glm-5.3-flash,qwen3-coder,deepseek-v4-flash" || out.Models[0]["reasoning"] != true {
		t.Fatalf("catalog = %+v", out)
	}
	out.Models = nil
	if get(t, srv, "/api/models", "u1", &out); out.Default != "glm-5.3" || len(out.Models) != 4 {
		t.Fatalf("basic catalog = %+v", out)
	}
}

func TestDashboardIsServedWithoutAuth(t *testing.T) {
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: users, Proxy: http.NotFoundHandler(),
		Dashboard:     http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "dashboard") }),
		UpstreamReady: func(context.Context) error { return nil }}))
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/sessions/abc")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "dashboard" {
		t.Fatalf("got %q", body)
	}
}

const policyYAML = `models:
  glm:
    upstream_id: z-ai/glm-5.3
    reasoning_effort: low
budget:
  currency: USD
  timezone: UTC
  global_limits: {5_hours: 1, day: 1, week: 1, month: 100}
tiers:
  basic:
    default: true
    budget: {5_hours: 1, day: 1, week: 1, month: 100}
    models:
      - {name: glm, default: true}
users:
  - email: admin@example.com
    role: admin
  - email: alice@example.com
  - email: bob@example.com
`

// TestChainRecordsAndShowsCost drives the phase-1 chain through the real router and PostgreSQL:
// client -> Wrench proxy -> fake OpenRouter stream -> journal -> dashboard API.
func TestChainRecordsAndShowsCost(t *testing.T) {
	st := storetest.Open(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(policyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	openrouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gen := strconv.FormatInt(calls.Add(1), 10)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{
			`{"id":"gen-` + gen + `","model":"z-ai/glm-5.3","provider":"Z.AI","choices":[{"delta":{"content":"hi"}}]}`,
			`{"id":"gen-` + gen + `","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.00025}}`,
			`[DONE]`,
		} {
			_, _ = io.WriteString(w, "data: "+e+"\n\n")
		}
	}))
	defer openrouter.Close()
	u, _ := url.Parse(openrouter.URL)
	p := proxy.New(config.Fixed(cfg), catalogtest.For(t, cfg), env.UpstreamEnv{URL: u, Key: "k", TimeoutConnect: time.Second, TimeoutFirstToken: 5 * time.Second, TimeoutStreamIdle: 5 * time.Second, TimeoutTotal: 10 * time.Second, ConcurrencyPerUser: 3, ConcurrencyGlobal: 60}, st, budget.NewChecker(config.Fixed(cfg), st))
	srv := httptest.NewServer(New(Deps{Store: st, Auth: users, Proxy: p, Dashboard: http.NotFoundHandler(), Config: config.Fixed(cfg), Catalog: catalogtest.For(t, cfg), UpstreamReady: func(context.Context) error { return nil }}))
	defer srv.Close()

	for _, c := range []struct{ token, session string }{{"alice", "s-alice"}, {"bob", "s-bob"}} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"glm","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set(proxy.SessionHeader, c.session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"content":"hi"`) {
			t.Fatalf("%s: %d %s", c.token, resp.StatusCode, body)
		}
	}

	var reqs struct {
		Attempts []store.Attempt `json:"attempts"`
	}
	if code := get(t, srv, "/api/requests", "alice", &reqs); code != http.StatusOK {
		t.Fatalf("requests %d", code)
	}
	if len(reqs.Attempts) != 1 || reqs.Attempts[0].UserEmail != "alice@example.com" {
		t.Fatalf("alice sees %+v", reqs.Attempts)
	}
	a := reqs.Attempts[0]
	if a.CostState != store.CostConfirmed || a.Cost.String() != "0.00025" || *a.Tokens.Input != 10 || a.Outcome != store.OutcomeSuccess || a.InferenceProvider != "Z.AI" {
		t.Fatalf("attempt = %+v", a)
	}

	if code := get(t, srv, "/api/requests", "admin", &reqs); code != http.StatusOK || len(reqs.Attempts) != 0 {
		t.Fatalf("admin's own view sees %d attempts", len(reqs.Attempts))
	}
	if code := get(t, srv, "/api/requests?user=*", "admin", &reqs); code != http.StatusOK || len(reqs.Attempts) != 2 {
		t.Fatalf("admin sees %d attempts", len(reqs.Attempts))
	}
	if code := get(t, srv, "/api/requests?user=bob@example.com", "admin", &reqs); code != http.StatusOK || len(reqs.Attempts) != 1 {
		t.Fatalf("admin filter sees %d attempts", len(reqs.Attempts))
	}
	if code := get(t, srv, "/api/requests?user=bob@example.com", "alice", nil); code != http.StatusForbidden {
		t.Fatalf("alice filter escapes own scope: %d", code)
	}

	var usage struct {
		Window string              `json:"window"`
		Start  time.Time           `json:"start"`
		End    time.Time           `json:"end"`
		Total  store.UsageTotals   `json:"total"`
		Groups []store.UsageTotals `json:"groups"`
	}
	if code := get(t, srv, "/api/usage?window=day&group=user&user=*", "admin", &usage); code != http.StatusOK {
		t.Fatalf("usage %d", code)
	}
	if usage.Window != "day" || usage.Total.Attempts != 2 || len(usage.Groups) != 2 || usage.End.Sub(usage.Start) != 24*time.Hour {
		t.Fatalf("usage = %+v", usage)
	}
	if code := get(t, srv, "/api/usage?window=day&group=user", "bob", &usage); code != http.StatusOK || len(usage.Groups) != 1 || usage.Groups[0].Key != "bob@example.com" {
		t.Fatalf("bob usage = %+v", usage)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/usage?window=year", nil)
	req.Header.Set("Authorization", "Bearer admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(msg), "5_hours, day, week or month") {
		t.Fatalf("bad window: %d %s", resp.StatusCode, msg)
	}
	if code := get(t, srv, "/api/usage?window=day&group=bogus", "admin", nil); code != http.StatusBadRequest {
		t.Fatalf("bad group: %d", code)
	}

	var sessions struct {
		Sessions []struct {
			store.Session
			Usage store.UsageTotals `json:"usage"`
		} `json:"sessions"`
	}
	if code := get(t, srv, "/api/sessions", "alice", &sessions); code != http.StatusOK || len(sessions.Sessions) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	if s := sessions.Sessions[0]; s.ID != "s-alice" || s.Usage.Cost.String() != "0.00025" || s.Usage.Attempts != 1 {
		t.Fatalf("session = %+v", s)
	}

	var me auth.Identity
	if code := get(t, srv, "/api/me", "admin", &me); code != http.StatusOK || me.Role != config.RoleAdmin {
		t.Fatalf("me = %+v", me)
	}
}
