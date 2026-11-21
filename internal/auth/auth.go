// Package auth resolves the caller of client and dashboard requests from a device key or a dashboard session
// cookie; there is no static or development token.
// Every request is re-checked against the YAML allowlist and the stored GitLab account state.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/store"
)

const (
	// KeyPrefix marks device keys so they are recognizable in logs and secret scanners.
	KeyPrefix = "wrk_"
	// statusAgeFactor: a GitLab state older than this many sync intervals blocks inference and login.
	statusAgeFactor = 3
)

// CredentialKind says how the caller authenticated.
type CredentialKind string

const (
	CredentialKey     CredentialKind = "key"
	CredentialSession CredentialKind = "session"
)

// Identity is an authenticated, allowlisted caller. Role always comes from the current YAML.
type Identity struct {
	Email      string         `json:"email"`
	Role       config.Role    `json:"role"`
	Credential CredentialKind `json:"credential"`
	// StatusFresh means GitLab confirmed the account active within three sync intervals.
	StatusFresh bool `json:"status_fresh"`
	// KeyID is the device key that authenticated the request; zero for other credentials.
	KeyID int64 `json:"-"`
	// UserID is the stored user's id.
	UserID int64 `json:"-"`
}

// Authenticator resolves the identity of a request or returns an error that maps to 401.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

// ErrUnauthenticated is returned for a missing, unknown, expired or revoked credential,
// and for users who are blocked or no longer allowlisted.
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// NewToken returns a random URL-safe token with the prefix and its storage hash.
func NewToken(prefix string) (string, []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := prefix + base64.RawURLEncoding.EncodeToString(b)
	return tok, Hash(tok)
}

// Hash is the only form in which tokens are stored.
func Hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// CredentialStore looks up stored credentials by hash.
type CredentialStore interface {
	LookupClientKey(ctx context.Context, tokenHash []byte, now time.Time) (store.Credential, error)
	LookupWebSession(ctx context.Context, tokenHash []byte, now time.Time) (store.Credential, error)
}

// Cookies names and writes Wrench's cookies. Secure cookies carry the __Host- prefix (host-only, HTTPS only);
// HTTP-only deployments (WRENCH_HTTP_ONLY) drop both, because browsers refuse __Host- cookies without Secure.
type Cookies struct {
	Secure bool
}

// Session is the dashboard session cookie.
func (c Cookies) Session() string { return c.prefix() + "wrench_session" }

// Binding ties a GitLab callback to the browser that started the sign-in.
func (c Cookies) Binding() string { return c.prefix() + "wrench_login" }

func (c Cookies) prefix() string {
	if c.Secure {
		return "__Host-"
	}
	return ""
}

// Set writes a host-only, HttpOnly, SameSite=Lax cookie, Secure unless HTTP-only; Lax lets the GitLab redirect
// carry it. A negative maxAge deletes the cookie.
func (c Cookies) Set(w http.ResponseWriter, name, value string, maxAge int) {
	//nolint:gosec // Secure is off only in the explicit HTTP-only mode for local stacks
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge,
		Secure: c.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// Resolver is the production Authenticator.
type Resolver struct {
	cfg          *config.Holder
	store        CredentialStore
	cookies      Cookies
	maxStatusAge time.Duration
	now          func() time.Time
}

// NewResolver builds the authenticator; cookies names the session cookie for the deployment mode.
func NewResolver(cfg *config.Holder, s CredentialStore, cookies Cookies, syncInterval time.Duration) *Resolver {
	return &Resolver{cfg: cfg, store: s, cookies: cookies, maxStatusAge: statusAgeFactor * syncInterval,
		now: func() time.Time { return time.Now().UTC() }}
}

// Authenticate accepts a bearer device key or, without a bearer, the session cookie.
func (r *Resolver) Authenticate(req *http.Request) (Identity, error) {
	now := r.now()
	if bearer, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer "); ok {
		if strings.HasPrefix(bearer, KeyPrefix) {
			c, err := r.store.LookupClientKey(req.Context(), Hash(bearer), now)
			return r.identity(req.Context(), c, err, CredentialKey, now)
		}
		return Identity{}, ErrUnauthenticated
	}
	if c, err := req.Cookie(r.cookies.Session()); err == nil && c.Value != "" {
		cred, err := r.store.LookupWebSession(req.Context(), Hash(c.Value), now)
		return r.identity(req.Context(), cred, err, CredentialSession, now)
	}
	return Identity{}, ErrUnauthenticated
}

// identity turns a credential lookup result into an allowlisted identity with the current YAML role.
func (r *Resolver) identity(ctx context.Context, c store.Credential, err error, kind CredentialKind, now time.Time) (Identity, error) {
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, ErrUnauthenticated
	}
	if err != nil {
		return Identity{}, fmt.Errorf("auth: credential lookup: %w", err)
	}
	user, ok := config.For(ctx, r.cfg).User(c.UserEmail)
	if !ok || c.UserStatus == store.UserBlocked {
		return Identity{}, ErrUnauthenticated
	}
	fresh := c.UserStatus == store.UserActive && c.StatusCheckedAt != nil && now.Sub(*c.StatusCheckedAt) < r.maxStatusAge
	id := Identity{Email: user.Email, Role: user.Role, Credential: kind, StatusFresh: fresh, UserID: c.UserID}
	if kind == CredentialKey {
		id.KeyID = c.ID
	}
	return id, nil
}

type identityKey struct{}

// Middleware rejects unauthenticated requests with an OpenAI-style JSON error
// and stores the identity in the request context. Storage failures are 503, not 401.
func Middleware(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := a.Authenticate(r)
		switch {
		case errors.Is(err, ErrUnauthenticated):
			writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication_error")
		case err != nil:
			writeError(w, http.StatusServiceUnavailable, "authentication is unavailable", "wrench_error")
		default:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
		}
	})
}

// RequireFreshStatus guards inference: the GitLab account state must be recently confirmed.
func RequireFreshStatus(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, _ := FromContext(r.Context()); !id.StatusFresh {
			writeError(w, http.StatusForbidden, "GitLab account status is not confirmed; try again later", "permission_error")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// FromContext returns the identity stored by Middleware.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

func writeError(w http.ResponseWriter, status int, message, kind string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message, "type": kind}})
}
