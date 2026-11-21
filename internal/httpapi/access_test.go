package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/catalog/catalogtest"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/env"
	"github.com/ordo-systems/wrench/internal/gitlab"
	"github.com/ordo-systems/wrench/internal/gitlab/gitlabtest"
	"github.com/ordo-systems/wrench/internal/login"
	"github.com/ordo-systems/wrench/internal/proxy"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

const accessPolicy = `models:
  glm:
    upstream_id: z-ai/glm-5.3
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
  - email: alice@example.com
  - email: bob@example.com
`

// stack is Wrench assembled from real components, with fake GitLab and fake OpenRouter.
type stack struct {
	t      *testing.T
	store  *store.Store
	gitlab *gitlabtest.Fake
	gl     *gitlab.Client
	server *httptest.Server
	cfg    *config.Config
	// handler is swapped to simulate a restart with a changed policy.
	handler http.Handler
}

func loadPolicy(t *testing.T, yaml string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newStack(t *testing.T, keyTTL time.Duration) *stack {
	t.Helper()
	s := &stack{t: t, store: storetest.Open(t), gitlab: gitlabtest.New(t)}
	openrouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"gen-`+strconv.FormatInt(time.Now().UnixNano(), 10)+`","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0.001}}`)
	}))
	t.Cleanup(openrouter.Close)
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }))
	t.Cleanup(s.server.Close)
	glURL, _ := url.Parse(s.gitlab.URL)
	public, _ := url.Parse(s.server.URL)
	s.gl = gitlab.New(env.GitLabEnv{URL: glURL, ClientID: gitlabtest.ClientID, ClientSecret: gitlabtest.ClientSecret,
		SyncToken: gitlabtest.SyncToken, SyncInterval: time.Minute}, public)
	upstream, _ := url.Parse(openrouter.URL)
	s.restart(loadPolicy(t, accessPolicy), keyTTL, upstream)
	return s
}

// restart rebuilds the handler as a process restart with cfg would.
func (s *stack) restart(cfg *config.Config, keyTTL time.Duration, upstream *url.URL) {
	s.cfg = cfg
	p := proxy.New(config.Fixed(cfg), catalogtest.For(s.t, cfg), env.UpstreamEnv{URL: upstream, Key: "k", TimeoutConnect: time.Second, TimeoutFirstToken: 5 * time.Second,
		TimeoutStreamIdle: 5 * time.Second, TimeoutTotal: 10 * time.Second, ConcurrencyPerUser: 3, ConcurrencyGlobal: 60}, s.store, budget.NewChecker(config.Fixed(cfg), s.store))
	s.handler = New(Deps{
		Store: s.store, Auth: auth.NewResolver(config.Fixed(cfg), s.store, auth.Cookies{Secure: true}, time.Minute), Proxy: p, Dashboard: http.NotFoundHandler(),
		Config: config.Fixed(cfg), Catalog: catalogtest.For(s.t, cfg), PublicURL: s.server.URL, Login: login.New(config.Fixed(cfg), s.store, s.gl, auth.Cookies{Secure: true}, time.Hour, keyTTL, s.server.URL, nil),
		UpstreamReady: func(context.Context) error { return nil },
	})
}

// deviceLogin runs opencode's device login for a GitLab account: the dashboard sign-in, the approval and one poll.
// It returns the device key, its refresh token and 200, or the status that refused the sign-in.
func (s *stack) deviceLogin(account int64) (string, string, int) {
	s.t.Helper()
	b := newBrowser(s.t)
	resp := b.do(http.MethodGet, s.server.URL+"/auth/gitlab/login", "")
	if resp = b.do(http.MethodGet, s.gitlab.Authorize(s.t, resp.Header.Get("Location"), account), ""); resp.StatusCode != http.StatusFound {
		return "", "", resp.StatusCode
	}
	_, c := s.postJSON("/auth/device/code", `{"client_id":"opencode-cli"}`)
	if code := s.decideDevice(b, "", s.server.URL, c["user_code"].(string), "approve"); code != http.StatusNoContent {
		return "", "", code
	}
	code, tok := s.postJSON("/auth/device/token", `{"grant_type":"urn:ietf:params:oauth:grant-type:device_code","client_id":"opencode-cli","device_code":"`+
		c["device_code"].(string)+`"}`)
	if code != http.StatusOK {
		return "", "", code
	}
	return tok["access_token"].(string), tok["refresh_token"].(string), http.StatusOK
}

// refresh trades a refresh token for a new device key.
func (s *stack) refresh(token string) string {
	s.t.Helper()
	code, tok := s.postJSON("/auth/device/token", `{"grant_type":"refresh_token","client_id":"opencode-cli","refresh_token":"`+token+`"}`)
	if code != http.StatusOK {
		s.t.Fatalf("refresh: %d %v", code, tok)
	}
	return tok["access_token"].(string)
}

// access reports the status of an inference call and of a dashboard data read with the key.
func (s *stack) access(key string) (inference, dashboard int) {
	s.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, s.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"glm","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set(proxy.SessionHeader, "session-"+key[len(key)-8:])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, get(s.t, s.server, "/api/requests", key, nil)
}

func TestAccessAcceptance(t *testing.T) {
	s := newStack(t, time.Hour)
	s.gitlab.Add(gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "active"})
	s.gitlab.Add(gitlabtest.Account{ID: 2, Email: "bob@example.com", EmailVerified: true, State: "active"})
	s.gitlab.Add(gitlabtest.Account{ID: 3, Email: "eve@example.com", EmailVerified: true, State: "active"})

	alice, aliceRefresh, code := s.deviceLogin(1)
	if code != http.StatusOK {
		t.Fatalf("alice login: %d", code)
	}
	if inf, dash := s.access(alice); inf != http.StatusOK || dash != http.StatusOK {
		t.Fatalf("alice: inference %d, dashboard %d", inf, dash)
	}

	t.Run("outside the allowlist", func(t *testing.T) {
		if key, _, code := s.deviceLogin(3); code == http.StatusOK || key != "" {
			t.Fatalf("eve got a key: %d", code)
		}
	})

	t.Run("second device keeps working after the first refreshes", func(t *testing.T) {
		desktop, _, _ := s.deviceLogin(1)
		rotated := s.refresh(aliceRefresh)
		if inf, _ := s.access(alice); inf != http.StatusUnauthorized {
			t.Fatalf("refreshed key still works: %d", inf)
		}
		for _, k := range []string{desktop, rotated} {
			if inf, dash := s.access(k); inf != http.StatusOK || dash != http.StatusOK {
				t.Fatalf("key: inference %d, dashboard %d", inf, dash)
			}
		}
		alice = rotated
	})

	t.Run("revoked key", func(t *testing.T) {
		var out struct {
			Keys []store.ClientKey `json:"keys"`
		}
		get(t, s.server, "/api/keys", alice, &out)
		// The listing itself used alice's key last, which tells her row from the desktop's.
		var own *store.ClientKey
		for i, k := range out.Keys {
			if k.RevokedAt == nil && k.LastUsedAt != nil && (own == nil || k.LastUsedAt.After(*own.LastUsedAt)) {
				own = &out.Keys[i]
			}
		}
		if own == nil {
			t.Fatalf("no active key in %+v", out.Keys)
		}
		if code := send(t, s.server, http.MethodDelete, "/api/keys/"+strconv.FormatInt(own.ID, 10), alice, ""); code != http.StatusNoContent {
			t.Fatalf("revoke: %d", code)
		}
		if inf, dash := s.access(alice); inf != http.StatusUnauthorized || dash != http.StatusUnauthorized {
			t.Fatalf("revoked: inference %d, dashboard %d", inf, dash)
		}
	})

	t.Run("removed from the allowlist", func(t *testing.T) {
		bob, _, _ := s.deviceLogin(2)
		upstream, _ := url.Parse("http://127.0.0.1:1")
		s.restart(loadPolicy(t, strings.Replace(accessPolicy, "  - email: bob@example.com\n", "", 1)), time.Hour, upstream)
		if inf, dash := s.access(bob); inf != http.StatusUnauthorized || dash != http.StatusUnauthorized {
			t.Fatalf("removed bob: inference %d, dashboard %d", inf, dash)
		}
	})
}

func TestExpiredKeyIsRejected(t *testing.T) {
	s := newStack(t, time.Second)
	s.gitlab.Add(gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "active"})
	key, _, code := s.deviceLogin(1)
	if code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	time.Sleep(1100 * time.Millisecond)
	if inf, dash := s.access(key); inf != http.StatusUnauthorized || dash != http.StatusUnauthorized {
		t.Fatalf("expired: inference %d, dashboard %d", inf, dash)
	}
}

func TestGitLabDisableRevokesAccess(t *testing.T) {
	s := newStack(t, time.Hour)
	s.gitlab.Add(gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "active"})
	key, _, _ := s.deviceLogin(1)
	s.gitlab.Update(1, func(a *gitlabtest.Account) { a.State = "blocked" })
	login.NewSyncer(s.store, s.gl, time.Minute).SyncOnce(context.Background())
	if inf, dash := s.access(key); inf != http.StatusUnauthorized || dash != http.StatusUnauthorized {
		t.Fatalf("disabled: inference %d, dashboard %d", inf, dash)
	}
	// Re-enabling in GitLab does not resurrect revoked keys; the user signs in again.
	s.gitlab.Update(1, func(a *gitlabtest.Account) { a.State = "active" })
	login.NewSyncer(s.store, s.gl, time.Minute).SyncOnce(context.Background())
	if inf, _ := s.access(key); inf != http.StatusUnauthorized {
		t.Fatalf("revoked key came back: %d", inf)
	}
	if _, _, code := s.deviceLogin(1); code != http.StatusOK {
		t.Fatalf("relogin after re-enable: %d", code)
	}
}

// browser keeps cookies by name and never follows redirects.
type browser struct {
	t       *testing.T
	cookies map[string]string
}

func newBrowser(t *testing.T) *browser { return &browser{t: t, cookies: map[string]string{}} }

func (b *browser) do(method, target, body string) *http.Response {
	b.t.Helper()
	req, _ := http.NewRequest(method, target, strings.NewReader(body))
	for k, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	return resp
}
