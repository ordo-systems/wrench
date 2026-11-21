// Package gitlab talks to the company GitLab: OpenID Connect login (authorization code with PKCE)
// and the Users API that confirms an account is still active.
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/ordo-systems/wrench/internal/env"
)

// CallbackPath is the exact redirect URI path registered in the GitLab OIDC application.
const CallbackPath = "/auth/gitlab/callback"

var (
	// ErrEmailNotVerified means the ID token lacks an email or GitLab has not verified it.
	ErrEmailNotVerified = errors.New("gitlab: email missing or not verified")
	// errNonce means the ID token was not issued for this login attempt.
	errNonce = errors.New("gitlab: ID token nonce mismatch")
	// errStatusUnavailable means the account state could not be confirmed either way.
	errStatusUnavailable = errors.New("gitlab: account status unavailable")
)

// Claims is the verified identity from an ID token.
type Claims struct {
	Issuer   string
	Subject  string
	GitLabID int64
	Email    string
}

// Client is safe for concurrent use. Provider discovery happens on first use and is retried
// after failures, so GitLab being down at startup does not stop Wrench.
type Client struct {
	cfg      env.GitLabEnv
	issuer   string
	redirect string
	http     *http.Client

	mu       sync.Mutex
	provider *oidc.Provider
}

// New configures a client; it performs no network calls.
func New(cfg env.GitLabEnv, publicURL interface{ String() string }) *Client {
	return &Client{
		cfg:      cfg,
		issuer:   strings.TrimRight(cfg.URL.String(), "/"),
		redirect: strings.TrimRight(publicURL.String(), "/") + CallbackPath,
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) oauth(ctx context.Context) (*oidc.Provider, *oauth2.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider == nil {
		p, err := oidc.NewProvider(oidc.ClientContext(ctx, c.http), c.issuer)
		if err != nil {
			return nil, nil, fmt.Errorf("gitlab: OIDC discovery: %w", err)
		}
		c.provider = p
	}
	return c.provider, &oauth2.Config{
		ClientID:     c.cfg.ClientID,
		ClientSecret: c.cfg.ClientSecret.Reveal(),
		Endpoint:     c.provider.Endpoint(),
		RedirectURL:  c.redirect,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}, nil
}

// NewVerifier returns a fresh PKCE code verifier.
func NewVerifier() string { return oauth2.GenerateVerifier() }

// AuthURL builds the GitLab authorization URL for one login attempt.
func (c *Client) AuthURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	_, cfg, err := c.oauth(ctx)
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

// Exchange redeems the authorization code and verifies the ID token: signature, issuer, audience,
// expiry and nonce, then requires a verified email.
func (c *Client) Exchange(ctx context.Context, code, verifier, nonce string) (Claims, error) {
	provider, cfg, err := c.oauth(ctx)
	if err != nil {
		return Claims{}, err
	}
	ctx = oidc.ClientContext(ctx, c.http)
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Claims{}, fmt.Errorf("gitlab: code exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return Claims{}, errors.New("gitlab: token response has no id_token")
	}
	idt, err := provider.Verifier(&oidc.Config{ClientID: c.cfg.ClientID}).Verify(ctx, raw)
	if err != nil {
		return Claims{}, fmt.Errorf("gitlab: ID token: %w", err)
	}
	return identity(idt, nonce)
}

// identity checks the nonce and extracts a verified email and the numeric GitLab user id.
func identity(idt *oidc.IDToken, nonce string) (Claims, error) {
	if idt.Nonce != nonce {
		return Claims{}, errNonce
	}
	var extra struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := idt.Claims(&extra); err != nil {
		return Claims{}, fmt.Errorf("gitlab: ID token claims: %w", err)
	}
	if extra.Email == "" || !extra.EmailVerified {
		return Claims{}, ErrEmailNotVerified
	}
	id, err := strconv.ParseInt(idt.Subject, 10, 64)
	if err != nil {
		return Claims{}, fmt.Errorf("gitlab: subject %q is not a GitLab user id", idt.Subject)
	}
	return Claims{Issuer: idt.Issuer, Subject: idt.Subject, GitLabID: id, Email: extra.Email}, nil
}

// AccountActive asks the Users API whether the account is active and unlocked.
// 404 is a confirmed removal (false, nil); timeouts, 5xx and rejected credentials are
// errStatusUnavailable, never proof of removal.
func (c *Client) AccountActive(ctx context.Context, gitlabID int64) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.issuer+"/api/v4/users/"+strconv.FormatInt(gitlabID, 10), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.cfg.SyncToken.Reveal())
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errStatusUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode != http.StatusOK:
		return false, fmt.Errorf("%w: status %d", errStatusUnavailable, resp.StatusCode)
	}
	var u struct {
		State  string `json:"state"`
		Locked bool   `json:"locked"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return false, fmt.Errorf("%w: %w", errStatusUnavailable, err)
	}
	return u.State == "active" && !u.Locked, nil
}
