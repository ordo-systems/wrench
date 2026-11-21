package login

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/env"
	"github.com/ordo-systems/wrench/internal/gitlab"
	"github.com/ordo-systems/wrench/internal/gitlab/gitlabtest"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

const policyYAML = `models:
  m:
    upstream_id: v/m
budget:
  currency: USD
  timezone: UTC
  global_limits: {5_hours: 1, day: 1, week: 1, month: 100}
tiers:
  basic:
    default: true
    budget: {5_hours: 1, day: 1, week: 1, month: 100}
    models:
      - {name: m, default: true}
users:
  - email: alice@example.com
  - email: bob@example.com
`

type env_ struct {
	t       *testing.T
	fake    *gitlabtest.Fake
	store   *store.Store
	server  *httptest.Server
	handler *Handler
	cfg     *config.Config
	cookies auth.Cookies
}

func setup(t *testing.T) *env_ {
	t.Helper()
	return setupMode(t, auth.Cookies{Secure: true})
}

// setupMode starts the login handler with the given cookie mode.
func setupMode(t *testing.T, cookies auth.Cookies) *env_ {
	t.Helper()
	st := storetest.Open(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(policyYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := gitlabtest.New(t)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	glURL, _ := url.Parse(fake.URL)
	public, _ := url.Parse(srv.URL)
	gl := gitlab.New(env.GitLabEnv{URL: glURL, ClientID: gitlabtest.ClientID, ClientSecret: gitlabtest.ClientSecret,
		SyncToken: gitlabtest.SyncToken, SyncInterval: time.Minute}, public)
	h := New(config.Fixed(cfg), st, gl, cookies, 12*time.Hour, 168*time.Hour, srv.URL, nil)
	h.Register(mux)
	return &env_{t: t, fake: fake, store: st, server: srv, handler: h, cfg: cfg, cookies: cookies}
}

// browser keeps cookies by name and never follows redirects, so each hop is visible. secure is the Secure
// attribute every cookie must carry in the server's mode.
type browser struct {
	t       *testing.T
	cookies map[string]string
	secure  bool
}

func newBrowser(t *testing.T) *browser {
	return &browser{t: t, cookies: map[string]string{}, secure: true}
}

func (b *browser) do(method, target string, body string) *http.Response {
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
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	for _, c := range resp.Cookies() {
		if c.Secure != b.secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
			b.t.Fatalf("cookie %s: want Secure=%v, HttpOnly, Lax, host-only: %+v", c.Name, b.secure, c)
		}
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	return resp
}

// login runs the browser through Wrench and GitLab and returns the final callback response.
func (e *env_) login(b *browser, start string, account int64) *http.Response {
	e.t.Helper()
	resp := b.do(http.MethodGet, e.server.URL+start, "")
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("start: %d", resp.StatusCode)
	}
	callback := e.fake.Authorize(e.t, resp.Header.Get("Location"), account)
	return b.do(http.MethodGet, callback, "")
}

func TestWebLogin(t *testing.T) {
	for _, cookies := range []auth.Cookies{{Secure: true}, {}} {
		t.Run(cookies.Session(), func(t *testing.T) { testWebLogin(t, cookies) })
	}
}

func testWebLogin(t *testing.T, cookies auth.Cookies) {
	e := setupMode(t, cookies)
	e.fake.Add(gitlabtest.Account{ID: 7, Email: "Alice@Example.com", EmailVerified: true, State: "active"})
	b := newBrowser(t)
	b.secure = cookies.Secure
	resp := e.login(b, "/auth/gitlab/login", 7)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	session := b.cookies[cookies.Session()]
	if session == "" {
		t.Fatal("no session cookie")
	}
	if _, ok := b.cookies[cookies.Binding()]; ok {
		t.Fatal("binding cookie not cleared")
	}
	c, err := e.store.LookupWebSession(context.Background(), auth.Hash(session), time.Now())
	if err != nil || c.UserEmail != "alice@example.com" || c.UserStatus != store.UserActive {
		t.Fatalf("session = %+v, %v", c, err)
	}

	// Logout revokes the stored session, not just the cookie, and deletes the cookie.
	if resp := b.do(http.MethodPost, e.server.URL+"/auth/logout", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if _, kept := b.cookies[cookies.Session()]; kept {
		t.Fatal("session cookie not deleted")
	}
	// Logging out without a session is harmless.
	if resp := (&browser{t: t, cookies: map[string]string{}, secure: cookies.Secure}).do(http.MethodPost, e.server.URL+"/auth/logout", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("anonymous logout: %d", resp.StatusCode)
	}
	if _, err := e.store.LookupWebSession(context.Background(), auth.Hash(session), time.Now()); err == nil {
		t.Fatal("session survived logout")
	}
}

func TestLoginRefusals(t *testing.T) {
	cases := map[string]struct {
		account gitlabtest.Account
		before  func(*env_)
		status  int
		bound   bool
		blocked bool
		message string
	}{
		"not allowlisted": {gitlabtest.Account{ID: 1, Email: "eve@example.com", EmailVerified: true, State: "active"}, nil, http.StatusForbidden, false, false,
			"The account eve@example.com is not allowed to use Wrench."},
		"unverified email":   {gitlabtest.Account{ID: 1, Email: "alice@example.com", State: "active"}, nil, http.StatusForbidden, false, false, ""},
		"blocked in GitLab":  {gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "blocked"}, nil, http.StatusForbidden, true, true, ""},
		"locked in GitLab":   {gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "active", Locked: true}, nil, http.StatusForbidden, true, true, ""},
		"status unavailable": {gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "active"}, func(e *env_) { e.fake.StatusDown = true }, http.StatusServiceUnavailable, true, false, ""},
		"identity conflict": {gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "active"}, func(e *env_) {
			_, _ = e.store.BindIdentity(context.Background(), "alice@example.com", e.fake.URL, "999", 999, time.Now())
		}, http.StatusForbidden, true, false, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.fake.Add(c.account)
			if c.before != nil {
				c.before(e)
			}
			var logs bytes.Buffer
			e.handler.log = slog.New(slog.NewJSONHandler(&logs, nil))
			b := newBrowser(t)
			resp := e.login(b, "/auth/gitlab/login", 1)
			if resp.StatusCode != c.status || b.cookies[e.cookies.Session()] != "" {
				t.Fatalf("status %d, session %q", resp.StatusCode, b.cookies[e.cookies.Session()])
			}
			if body, _ := io.ReadAll(resp.Body); c.message != "" && strings.TrimSpace(string(body)) != c.message {
				t.Fatalf("message %q, want %q", body, c.message)
			}
			if c.message != "" && !strings.Contains(logs.String(), `"email":"`+c.account.Email+`"`) {
				t.Fatalf("the refusal log lacks the email: %s", logs.String())
			}
			u, err := userByEmail(context.Background(), e.store, "alice@example.com")
			if (err == nil) != c.bound {
				t.Fatalf("bound = %v (%v)", err == nil, err)
			}
			if c.blocked && u.Status != store.UserBlocked {
				t.Fatalf("status %s", u.Status)
			}
		})
	}
}

func TestCallbackNeedsTheStartingBrowserAndFreshState(t *testing.T) {
	e := setup(t)
	e.fake.Add(gitlabtest.Account{ID: 7, Email: "alice@example.com", EmailVerified: true, State: "active"})
	victim := newBrowser(t)
	resp := victim.do(http.MethodGet, e.server.URL+"/auth/gitlab/login", "")
	callback := e.fake.Authorize(t, resp.Header.Get("Location"), 7)

	// A callback replayed in another browser carries a different binding cookie.
	attacker := newBrowser(t)
	attacker.cookies["__Host-wrench_login"] = "attacker-binding"
	if resp := attacker.do(http.MethodGet, callback, ""); resp.StatusCode != http.StatusBadRequest || attacker.cookies[e.cookies.Session()] != "" {
		t.Fatalf("foreign browser: %d", resp.StatusCode)
	}
	// The flow was consumed by that attempt, so the original browser cannot reuse it either.
	if resp := victim.do(http.MethodGet, callback, ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("replay: %d", resp.StatusCode)
	}
	if resp := newBrowser(t).do(http.MethodGet, e.server.URL+gitlab.CallbackPath+"?error=access_denied&state=x", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied: %d", resp.StatusCode)
	}
}

func TestSyncer(t *testing.T) {
	e := setup(t)
	e.fake.Add(gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "active"})
	e.fake.Add(gitlabtest.Account{ID: 2, Email: "bob@example.com", EmailVerified: true, State: "active"})
	alice := newBrowser(t)
	e.login(alice, "/auth/gitlab/login", 1)
	e.login(newBrowser(t), "/auth/gitlab/login", 2)

	glURL, _ := url.Parse(e.fake.URL)
	gl := gitlab.New(env.GitLabEnv{URL: glURL, ClientID: gitlabtest.ClientID, ClientSecret: gitlabtest.ClientSecret,
		SyncToken: gitlabtest.SyncToken, SyncInterval: time.Minute}, glURL)
	syncer := NewSyncer(e.store, gl, time.Minute)
	ctx := context.Background()

	e.fake.StatusDown = true
	if r := syncer.SyncOnce(ctx); r.Unavailable != 2 || r.Checked != 0 {
		t.Fatalf("unavailable pass = %+v", r)
	}
	if u, _ := userByEmail(ctx, e.store, "alice@example.com"); u.Status != store.UserActive {
		t.Fatalf("unavailable check changed state: %s", u.Status)
	}

	e.fake.StatusDown = false
	e.fake.Update(1, func(a *gitlabtest.Account) { a.State = "blocked" })
	e.fake.Update(2, func(a *gitlabtest.Account) { a.Deleted = true })
	if r := syncer.SyncOnce(ctx); r != (SyncResult{Checked: 2, Disabled: 2}) {
		t.Fatalf("disable pass = %+v", r)
	}
	// Already blocked users are not reported again.
	if r := syncer.SyncOnce(ctx); r != (SyncResult{Checked: 2}) {
		t.Fatalf("repeat pass = %+v", r)
	}
	for _, email := range []string{"alice@example.com", "bob@example.com"} {
		if u, _ := userByEmail(ctx, e.store, email); u.Status != store.UserBlocked {
			t.Fatalf("%s status %s", email, u.Status)
		}
	}
	if _, err := e.store.LookupWebSession(ctx, auth.Hash(alice.cookies[e.cookies.Session()]), time.Now()); err == nil {
		t.Fatal("session survived GitLab block")
	}

	// Run returns when its context ends.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { syncer.Run(runCtx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

type failingUsers struct{ SyncStore }

func (failingUsers) ListUsers(context.Context) ([]store.User, error) {
	return nil, errors.New("database down")
}

func TestSyncReportsUnreadableUsers(t *testing.T) {
	r := NewSyncer(failingUsers{}, nil, time.Minute).SyncOnce(context.Background())
	if r != (SyncResult{Failed: true}) {
		t.Fatalf("result = %+v", r)
	}
}

// userByEmail finds a bound user through the same listing the syncer uses.
func userByEmail(ctx context.Context, st SyncStore, email string) (store.User, error) {
	users, err := st.ListUsers(ctx)
	if err != nil {
		return store.User{}, err
	}
	for _, u := range users {
		if u.Email == email {
			return u, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

// A sign-in as another user in the same browser replaces the dashboard session and revokes the old one.
func TestSignInReplacesTheBrowserSession(t *testing.T) {
	e := setup(t)
	e.fake.Add(gitlabtest.Account{ID: 7, Email: "alice@example.com", EmailVerified: true, State: "active"})
	e.fake.Add(gitlabtest.Account{ID: 8, Email: "bob@example.com", EmailVerified: true, State: "active"})
	b := newBrowser(t)
	if resp := e.login(b, "/auth/gitlab/login", 7); resp.StatusCode != http.StatusFound {
		t.Fatalf("callback: %d", resp.StatusCode)
	}
	alice := b.cookies[e.cookies.Session()]
	c, err := e.store.LookupWebSession(context.Background(), auth.Hash(alice), time.Now())
	if alice == "" || err != nil || c.UserEmail != "alice@example.com" {
		t.Fatalf("first session = %q, %+v, %v", alice, c, err)
	}
	e.login(b, "/auth/gitlab/login", 8)
	bob := b.cookies[e.cookies.Session()]
	c, err = e.store.LookupWebSession(context.Background(), auth.Hash(bob), time.Now())
	if bob == alice || err != nil || c.UserEmail != "bob@example.com" {
		t.Fatalf("second sign-in session = %+v, %v", c, err)
	}
	// The replaced session is revoked, not orphaned: the browser no longer holds it, so nothing could sign it out.
	if _, err := e.store.LookupWebSession(context.Background(), auth.Hash(alice), time.Now()); err == nil {
		t.Fatal("replaced dashboard session is still valid")
	}
}
