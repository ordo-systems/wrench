package gitlab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/env"
	"github.com/ordo-systems/wrench/internal/gitlab/gitlabtest"
)

func client(t *testing.T, fake *gitlabtest.Fake) *Client {
	t.Helper()
	gl, _ := url.Parse(fake.URL)
	public, _ := url.Parse("https://wrench.example.com")
	return New(env.GitLabEnv{
		URL: gl, ClientID: gitlabtest.ClientID, ClientSecret: gitlabtest.ClientSecret, SyncToken: gitlabtest.SyncToken,
		SyncInterval: time.Minute,
	}, public)
}

// login runs the authorization-code flow against the fake for one account.
func login(t *testing.T, fake *gitlabtest.Fake, c *Client, id int64) (Claims, error) {
	t.Helper()
	ctx := context.Background()
	verifier := NewVerifier()
	authURL, err := c.AuthURL(ctx, "state-1", "nonce-1", verifier)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(authURL, url.QueryEscape("https://wrench.example.com/auth/gitlab/callback")) {
		t.Fatalf("redirect uri missing: %s", authURL)
	}
	cb, _ := url.Parse(fake.Authorize(t, authURL, id))
	if cb.Query().Get("state") != "state-1" {
		t.Fatalf("state lost: %s", cb)
	}
	return c.Exchange(ctx, cb.Query().Get("code"), verifier, "nonce-1")
}

func TestLoginReturnsVerifiedIdentity(t *testing.T) {
	fake := gitlabtest.New(t)
	fake.Add(gitlabtest.Account{ID: 7, Email: "Alice@Example.com", EmailVerified: true, State: "active"})
	claims, err := login(t, fake, client(t, fake), 7)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != fake.URL || claims.Subject != "7" || claims.GitLabID != 7 || claims.Email != "Alice@Example.com" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestLoginRejectsUntrustedTokens(t *testing.T) {
	cases := map[string]struct {
		account gitlabtest.Account
		tamper  func(*gitlabtest.Fake)
		want    error
	}{
		"unverified email": {gitlabtest.Account{ID: 1, Email: "a@x", EmailVerified: false}, nil, ErrEmailNotVerified},
		"no email":         {gitlabtest.Account{ID: 1}, nil, ErrEmailNotVerified},
		"wrong issuer":     {gitlabtest.Account{ID: 1, Email: "a@x", EmailVerified: true}, func(f *gitlabtest.Fake) { f.Issuer = "https://evil" }, nil},
		"wrong audience":   {gitlabtest.Account{ID: 1, Email: "a@x", EmailVerified: true}, func(f *gitlabtest.Fake) { f.Audience = "other" }, nil},
		"wrong nonce":      {gitlabtest.Account{ID: 1, Email: "a@x", EmailVerified: true}, func(f *gitlabtest.Fake) { f.Nonce = "replayed" }, errNonce},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fake := gitlabtest.New(t)
			fake.Add(c.account)
			if c.tamper != nil {
				c.tamper(fake)
			}
			_, err := login(t, fake, client(t, fake), 1)
			if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestExchangeRejectsWrongVerifier(t *testing.T) {
	fake := gitlabtest.New(t)
	fake.Add(gitlabtest.Account{ID: 1, Email: "a@x", EmailVerified: true})
	c := client(t, fake)
	authURL, _ := c.AuthURL(context.Background(), "s", "n", NewVerifier())
	cb, _ := url.Parse(fake.Authorize(t, authURL, 1))
	if _, err := c.Exchange(context.Background(), cb.Query().Get("code"), NewVerifier(), "n"); err == nil {
		t.Fatal("exchange accepted another PKCE verifier")
	}
}

func TestDiscoveryFailureIsRetried(t *testing.T) {
	fake := gitlabtest.New(t)
	c := client(t, fake)
	fake.Close()
	if _, err := c.AuthURL(context.Background(), "s", "n", NewVerifier()); err == nil {
		t.Fatal("want discovery error")
	}
}

func TestAccountStatus(t *testing.T) {
	fake := gitlabtest.New(t)
	c := client(t, fake)
	ctx := context.Background()
	fake.Add(gitlabtest.Account{ID: 1, State: "active"})
	fake.Add(gitlabtest.Account{ID: 2, State: "blocked"})
	fake.Add(gitlabtest.Account{ID: 3, State: "active", Locked: true})
	fake.Add(gitlabtest.Account{ID: 4, State: "deactivated"})

	for id, want := range map[int64]bool{1: true, 2: false, 3: false, 4: false, 99: false} {
		got, err := c.AccountActive(ctx, id)
		if err != nil || got != want {
			t.Errorf("account %d: %v, %v; want %v", id, got, err, want)
		}
	}

	fake.StatusDown = true
	if got, err := c.AccountActive(ctx, 1); got || !errors.Is(err, errStatusUnavailable) {
		t.Fatalf("5xx: %v, %v", got, err)
	}
	fake.StatusDown = false

	// A rejected sync token is a configuration problem, not proof that the account is gone.
	gl, _ := url.Parse(fake.URL)
	bad := New(env.GitLabEnv{URL: gl, ClientID: "x", ClientSecret: "x", SyncToken: "wrong", SyncInterval: time.Minute}, gl)
	if got, err := bad.AccountActive(ctx, 1); got || !errors.Is(err, errStatusUnavailable) {
		t.Fatalf("401: %v, %v", got, err)
	}
	// A dead GitLab and a garbled answer are unavailable too, and never report "active".
	gone := New(env.GitLabEnv{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, SyncToken: gitlabtest.SyncToken, SyncInterval: time.Minute}, gl)
	if got, err := gone.AccountActive(ctx, 1); got || !errors.Is(err, errStatusUnavailable) {
		t.Fatalf("connection refused: %v, %v", got, err)
	}
	garbled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "{") }))
	defer garbled.Close()
	gu, _ := url.Parse(garbled.URL)
	if got, err := New(env.GitLabEnv{URL: gu, SyncInterval: time.Minute}, gu).AccountActive(ctx, 1); got || !errors.Is(err, errStatusUnavailable) {
		t.Fatalf("garbled: %v, %v", got, err)
	}
}
