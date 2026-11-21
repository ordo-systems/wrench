package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/store"
)

func policy(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	y := `models:
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
  - email: dev@example.com
    role: admin
  - email: user@example.com
`
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// fakeStore holds credentials by token hash.
type fakeStore struct {
	keys, sessions map[string]store.Credential
}

func (f fakeStore) LookupClientKey(_ context.Context, h []byte, _ time.Time) (store.Credential, error) {
	if c, ok := f.keys[string(h)]; ok {
		return c, nil
	}
	return store.Credential{}, store.ErrNotFound
}

func (f fakeStore) LookupWebSession(_ context.Context, h []byte, _ time.Time) (store.Credential, error) {
	if c, ok := f.sessions[string(h)]; ok {
		return c, nil
	}
	return store.Credential{}, store.ErrNotFound
}

func cred(email string, status store.UserStatus, checked *time.Time) store.Credential {
	return store.Credential{ID: 9, UserID: 1, UserEmail: email, UserStatus: status, StatusCheckedAt: checked}
}

func resolver(t *testing.T, keys, sessions map[string]store.Credential) *Resolver {
	t.Helper()
	fs := fakeStore{keys: map[string]store.Credential{}, sessions: map[string]store.Credential{}}
	for tok, c := range keys {
		fs.keys[string(Hash(tok))] = c
	}
	for tok, c := range sessions {
		fs.sessions[string(Hash(tok))] = c
	}
	r := NewResolver(config.Fixed(policy(t)), fs, Cookies{Secure: true}, time.Minute)
	r.now = func() time.Time { return now }
	return r
}

func request(bearer, cookie string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: Cookies{Secure: true}.Session(), Value: cookie})
	}
	return r
}

func TestResolverCredentials(t *testing.T) {
	key, _ := NewToken(KeyPrefix)
	session, _ := NewToken("")
	fresh := ptr(now.Add(-time.Minute))
	stale := ptr(now.Add(-3 * time.Minute))
	r := resolver(t,
		map[string]store.Credential{
			key:            cred("user@example.com", store.UserActive, fresh),
			"wrk_stale":    cred("user@example.com", store.UserActive, stale),
			"wrk_pending":  cred("user@example.com", store.UserPending, nil),
			"wrk_removed":  cred("gone@example.com", store.UserActive, fresh),
			"wrk_blocked":  cred("user@example.com", store.UserBlocked, fresh),
			"wrk_Admin":    cred("DEV@example.com", store.UserActive, fresh),
			"static-token": cred("user@example.com", store.UserActive, fresh),
		},
		map[string]store.Credential{session: cred("user@example.com", store.UserActive, stale)})

	cases := []struct {
		name  string
		req   *http.Request
		ok    bool
		email string
		role  config.Role
		kind  CredentialKind
		fresh bool
	}{
		{"key", request(key, ""), true, "user@example.com", config.RoleUser, CredentialKey, true},
		{"stale status still reads", request("wrk_stale", ""), true, "user@example.com", config.RoleUser, CredentialKey, false},
		{"pending status", request("wrk_pending", ""), true, "user@example.com", config.RoleUser, CredentialKey, false},
		{"role comes from YAML", request("wrk_Admin", ""), true, "dev@example.com", config.RoleAdmin, CredentialKey, true},
		{"session cookie", request("", session), true, "user@example.com", config.RoleUser, CredentialSession, false},
		{"bearer without the key prefix", request("static-token", ""), false, "", "", "", false},
		{"removed from allowlist", request("wrk_removed", ""), false, "", "", "", false},
		{"blocked", request("wrk_blocked", ""), false, "", "", "", false},
		{"unknown key", request("wrk_nope", ""), false, "", "", "", false},
		{"unknown bearer", request("nope", ""), false, "", "", "", false},
		{"unknown session", request("", "nope"), false, "", "", "", false},
		{"nothing", request("", ""), false, "", "", "", false},
		// A present but empty bearer is a bad credential, not an absent one.
		{"empty bearer beside a session", func() *http.Request { r := request("", session); r.Header.Set("Authorization", "Bearer "); return r }(), false, "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := r.Authenticate(c.req)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v", err)
			}
			// Only device keys identify a device; cred() gives every stored credential ID 9.
			wantKey := int64(0)
			if c.kind == CredentialKey {
				wantKey = 9
			}
			if c.ok && (id.Email != c.email || id.Role != c.role || id.Credential != c.kind || id.StatusFresh != c.fresh || id.KeyID != wantKey) {
				t.Fatalf("identity = %+v", id)
			}
		})
	}
}

func TestCookies(t *testing.T) {
	tests := []struct {
		c                Cookies
		session, binding string
	}{
		{Cookies{Secure: true}, "__Host-wrench_session", "__Host-wrench_login"},
		{Cookies{}, "wrench_session", "wrench_login"},
	}
	for _, tt := range tests {
		if tt.c.Session() != tt.session || tt.c.Binding() != tt.binding {
			t.Errorf("%+v: %s %s", tt.c, tt.c.Session(), tt.c.Binding())
		}
		rec := httptest.NewRecorder()
		tt.c.Set(rec, tt.c.Session(), "v", 60)
		got := rec.Result().Cookies()[0]
		if got.Secure != tt.c.Secure || !got.HttpOnly || got.SameSite != http.SameSiteLaxMode || got.Path != "/" || got.MaxAge != 60 {
			t.Errorf("%+v: cookie %+v", tt.c, got)
		}
	}
}

func TestHTTPOnlyResolverReadsItsCookie(t *testing.T) {
	r := resolver(t, nil, map[string]store.Credential{"s": cred("user@example.com", store.UserActive, ptr(now))})
	r.cookies = Cookies{}
	plain := httptest.NewRequest(http.MethodGet, "/", nil)
	plain.AddCookie(&http.Cookie{Name: "wrench_session", Value: "s"})
	if id, err := r.Authenticate(plain); err != nil || id.Email != "user@example.com" {
		t.Fatalf("wrench_session: %+v, %v", id, err)
	}
	prefixed := httptest.NewRequest(http.MethodGet, "/", nil)
	prefixed.AddCookie(&http.Cookie{Name: "__Host-wrench_session", Value: "s"})
	if _, err := r.Authenticate(prefixed); err == nil {
		t.Fatal("HTTP-only mode accepted the Secure cookie name")
	}
}

func serve(h http.Handler, r *http.Request) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

func TestMiddlewares(t *testing.T) {
	key, _ := NewToken(KeyPrefix)
	r := resolver(t, map[string]store.Credential{
		key:         cred("user@example.com", store.UserActive, ptr(now)),
		"wrk_stale": cred("user@example.com", store.UserActive, ptr(now.Add(-time.Hour))),
	}, nil)
	var seen Identity
	inner := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { seen, _ = FromContext(req.Context()) })

	if code := serve(Middleware(r, inner), request(key, "")); code != http.StatusOK || seen.Email != "user@example.com" {
		t.Fatalf("code %d, seen %+v", code, seen)
	}
	if code := serve(Middleware(r, inner), request("", "")); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	fresh := Middleware(r, RequireFreshStatus(inner))
	if code := serve(fresh, request(key, "")); code != http.StatusOK {
		t.Fatalf("fresh: %d", code)
	}
	if code := serve(fresh, request("wrk_stale", "")); code != http.StatusForbidden {
		t.Fatalf("stale: %d", code)
	}
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("identity without middleware")
	}
}

func TestTokens(t *testing.T) {
	a, ha := NewToken(KeyPrefix)
	b, _ := NewToken(KeyPrefix)
	if a == b || !strings.HasPrefix(a, KeyPrefix) || len(a) < 40 || string(ha) != string(Hash(a)) || len(ha) != 32 {
		t.Fatalf("tokens %q %q", a, b)
	}
	if strings.ContainsAny(a, "$!+/=") {
		t.Fatalf("token %q is not safe for Pi apiKey literals or URLs", a)
	}
}
