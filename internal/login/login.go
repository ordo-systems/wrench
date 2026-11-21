// Package login runs GitLab sign-in for the dashboard and the device login for opencode, issues dashboard
// sessions and device keys, and keeps stored GitLab account states current.
//
// GitLab tokens never leave this package: the dashboard gets a session cookie, and opencode gets a Wrench device key
// once a signed-in user approves its code on the dashboard.
package login

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/gitlab"
	"github.com/ordo-systems/wrench/internal/store"
)

const flowTTL = 10 * time.Minute

// Store is the persistence the login flows need.
type Store interface {
	BindIdentity(ctx context.Context, email, issuer, subject string, gitlabID int64, at time.Time) (store.User, error)
	RecordStatus(ctx context.Context, userID int64, active bool, at time.Time) error
	CreateLoginFlow(ctx context.Context, f store.LoginFlow) error
	ConsumeLoginFlow(ctx context.Context, state string, now time.Time) (store.LoginFlow, error)
	CreateWebSession(ctx context.Context, userID int64, tokenHash []byte, now, expires time.Time) error
	RevokeWebSession(ctx context.Context, tokenHash []byte, now time.Time) error
	IssueClientKey(ctx context.Context, userID int64, deviceID, deviceName string, tokenHash []byte, now, expires time.Time) (store.ClientKey, error)
	CreateDeviceCode(ctx context.Context, deviceHash, userCodeHash []byte, expires time.Time, limit int) error
	DecideDeviceCode(ctx context.Context, userCodeHash []byte, userEmail string, approve bool, now time.Time) error
	PollDeviceCode(ctx context.Context, deviceHash []byte, now time.Time, interval time.Duration) (store.DeviceCode, error)
	UserByID(ctx context.Context, id int64) (store.User, error)
	IssueDeviceKey(ctx context.Context, userID int64, deviceID, deviceName string, tokenHash, refreshHash []byte, now, expires time.Time) (store.ClientKey, error)
	RefreshClientKey(ctx context.Context, refreshHash, tokenHash, newRefreshHash []byte, now, expires time.Time) (store.ClientKey, store.User, error)
	RevokeClientKey(ctx context.Context, id int64, userEmail string, now time.Time) (string, error)
}

// GitLab is the identity provider.
type GitLab interface {
	AuthURL(ctx context.Context, state, nonce, verifier string) (string, error)
	Exchange(ctx context.Context, code, verifier, nonce string) (gitlab.Claims, error)
	AccountActive(ctx context.Context, gitlabID int64) (bool, error)
}

// Handler serves the /auth routes. None of them require an existing credential.
type Handler struct {
	cfg        *config.Holder
	store      Store
	gitlab     GitLab
	cookies    auth.Cookies
	sessionTTL time.Duration
	keyTTL     time.Duration
	// publicURL is the origin browsers use, for the device verification link.
	publicURL string
	events    events.Publisher
	now       func() time.Time
	log       *slog.Logger
}

// New builds the login handler; publicURL is the origin browsers use, without a trailing slash.
// ev, which may be nil, hears about issued device keys.
func New(cfg *config.Holder, s Store, gl GitLab, cookies auth.Cookies, sessionTTL, keyTTL time.Duration, publicURL string, ev events.Publisher) *Handler {
	return &Handler{cfg: cfg, store: s, gitlab: gl, cookies: cookies, sessionTTL: sessionTTL, keyTTL: keyTTL, publicURL: publicURL, events: ev,
		now: func() time.Time { return time.Now().UTC() }, log: slog.Default()}
}

// Register mounts the routes.
//
//	GET  /auth/gitlab/login     start a dashboard login (?return=<same-origin path> to come back to)
//	GET  /auth/gitlab/callback  GitLab redirect target
//	POST /auth/logout           end the dashboard session
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/gitlab/login", h.webLogin)
	mux.HandleFunc("GET "+gitlab.CallbackPath, h.callback)
	mux.HandleFunc("POST /auth/logout", h.logout)
}

func (h *Handler) webLogin(w http.ResponseWriter, r *http.Request) {
	h.start(w, r, store.LoginFlow{ReturnTo: safeReturn(r.URL.Query().Get("return"))})
}

// start records the OAuth state server-side, binds it to this browser and redirects to GitLab.
func (h *Handler) start(w http.ResponseWriter, r *http.Request, f store.LoginFlow) {
	binding, bindingHash := auth.NewToken("")
	f.State, f.Nonce, f.Verifier = rand.Text(), rand.Text(), gitlab.NewVerifier()
	f.BrowserBinding = bindingHash
	f.ExpiresAt = h.now().Add(flowTTL)
	authURL, err := h.gitlab.AuthURL(r.Context(), f.State, f.Nonce, f.Verifier)
	if err != nil {
		h.log.Error("GitLab login unavailable", "err", err)
		page(w, http.StatusServiceUnavailable, "GitLab sign-in is unavailable; try again later.")
		return
	}
	if err := h.store.CreateLoginFlow(r.Context(), f); err != nil {
		h.log.Error("login flow not stored", "err", err)
		page(w, http.StatusServiceUnavailable, "Sign-in is unavailable; try again later.")
		return
	}
	h.cookies.Set(w, h.cookies.Binding(), binding, int(flowTTL.Seconds()))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// callback finishes a login. Every check happens before any credential exists.
func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.cookies.Set(w, h.cookies.Binding(), "", -1)
	if q.Get("error") != "" {
		page(w, http.StatusForbidden, "GitLab sign-in was not completed.")
		return
	}
	f, err := h.store.ConsumeLoginFlow(r.Context(), q.Get("state"), h.now())
	if err != nil {
		page(w, http.StatusBadRequest, "This sign-in link has expired or was already used. Start again.")
		return
	}
	c, err := r.Cookie(h.cookies.Binding())
	if err != nil || subtle.ConstantTimeCompare(auth.Hash(c.Value), f.BrowserBinding) != 1 {
		page(w, http.StatusBadRequest, "Sign-in must finish in the browser that started it.")
		return
	}
	user, status, msg := h.admit(r.Context(), q.Get("code"), f)
	if status != http.StatusOK {
		page(w, status, msg)
		return
	}
	h.finishWeb(w, r, user, f.ReturnTo)
}

// admit verifies the GitLab login and the account; it returns an HTTP status and user-facing message on refusal.
func (h *Handler) admit(ctx context.Context, code string, f store.LoginFlow) (store.User, int, string) {
	claims, err := h.gitlab.Exchange(ctx, code, f.Verifier, f.Nonce)
	if errors.Is(err, gitlab.ErrEmailNotVerified) {
		return store.User{}, http.StatusForbidden, "Your GitLab account has no verified email address."
	}
	if err != nil {
		h.log.Warn("GitLab login rejected", "err", err)
		return store.User{}, http.StatusBadGateway, "GitLab sign-in could not be verified. Start again."
	}
	allowed, ok := config.For(ctx, h.cfg).User(claims.Email)
	if !ok {
		h.log.Warn("login from a non-allowlisted account", "email", claims.Email, "gitlab_id", claims.GitLabID)
		return store.User{}, http.StatusForbidden, fmt.Sprintf("The account %s is not allowed to use Wrench.", claims.Email)
	}
	now := h.now()
	user, err := h.store.BindIdentity(ctx, allowed.Email, claims.Issuer, claims.Subject, claims.GitLabID, now)
	if errors.Is(err, store.ErrIdentityConflict) {
		h.log.Warn("identity conflict", "email", allowed.Email, "gitlab_id", claims.GitLabID)
		return store.User{}, http.StatusForbidden, "This email is bound to a different GitLab account. Ask an administrator."
	}
	if err != nil {
		h.log.Error("identity not stored", "err", err)
		return store.User{}, http.StatusServiceUnavailable, "Sign-in is unavailable; try again later."
	}
	return h.confirmActive(ctx, user, now)
}

// confirmActive requires a successful GitLab status check before any credential is issued.
func (h *Handler) confirmActive(ctx context.Context, user store.User, now time.Time) (store.User, int, string) {
	active, err := h.gitlab.AccountActive(ctx, user.GitLabID)
	if err != nil {
		h.log.Warn("account status unavailable at login", "email", user.Email, "err", err)
		return store.User{}, http.StatusServiceUnavailable, "GitLab could not confirm your account; try again later."
	}
	if err := h.store.RecordStatus(ctx, user.ID, active, now); err != nil {
		h.log.Error("account status not stored", "err", err)
		return store.User{}, http.StatusServiceUnavailable, "Sign-in is unavailable; try again later."
	}
	if !active {
		return store.User{}, http.StatusForbidden, "Your GitLab account is blocked, locked or removed."
	}
	return user, http.StatusOK, ""
}

func (h *Handler) finishWeb(w http.ResponseWriter, r *http.Request, user store.User, returnTo string) {
	if !h.startSession(w, r, user) {
		return
	}
	http.Redirect(w, r, safeReturn(returnTo), http.StatusFound)
}

// startSession opens a dashboard session for user and sets its cookie. A session the browser already held is
// revoked first: the new cookie replaces it, so nothing could sign it out later. On failure it answers 503 and
// returns false.
func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, user store.User) bool {
	now := h.now()
	if c, err := r.Cookie(h.cookies.Session()); err == nil && c.Value != "" {
		if err := h.store.RevokeWebSession(r.Context(), auth.Hash(c.Value), now); err != nil && !errors.Is(err, store.ErrNotFound) {
			h.log.Warn("replaced session not revoked", "err", err)
		}
	}
	token, tokenHash := auth.NewToken("")
	if err := h.store.CreateWebSession(r.Context(), user.ID, tokenHash, now, now.Add(h.sessionTTL)); err != nil {
		h.log.Error("session not stored", "err", err)
		page(w, http.StatusServiceUnavailable, "Sign-in is unavailable; try again later.")
		return false
	}
	h.cookies.Set(w, h.cookies.Session(), token, int(h.sessionTTL.Seconds()))
	return true
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.cookies.Session()); err == nil && c.Value != "" {
		if err := h.store.RevokeWebSession(r.Context(), auth.Hash(c.Value), h.now()); err != nil {
			h.log.Error("session not revoked", "err", err)
			jsonError(w, http.StatusServiceUnavailable, "logout is unavailable")
			return
		}
	}
	h.cookies.Set(w, h.cookies.Session(), "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func page(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = fmt.Fprintln(w, message) //nolint:gosec // text/plain: the browser does not interpret markup
}

func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message}})
}
