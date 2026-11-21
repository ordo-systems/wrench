// Package gitlab is the GitLab mock run by `wrench mock gitlab` and wrapped by gitlabtest: an OpenID Connect
// issuer with signed ID tokens, PKCE-checked code exchange, the Users API used for account status, and an account
// picker in place of a GitLab sign-in page. Accounts come from a Directory, so the command and the tests share one
// protocol implementation.
package gitlab

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// Account is a GitLab user as the mock reports it.
type Account struct {
	ID            int64
	Email         string
	EmailVerified bool
	State         string // "active", "blocked", "deactivated", ...
	Locked        bool
	Deleted       bool
}

// Directory resolves the accounts the mock serves.
type Directory interface {
	// Lookup returns the account the Users API and the token endpoint report; false means it does not exist.
	Lookup(id int64) (Account, bool)
	// Candidates lists the emails the picker offers.
	Candidates() []string
	// SignIn returns the account ID for an email chosen in the picker, or a user-facing error.
	SignIn(email string) (int64, error)
}

// Options configure a mock. Issuer is the base of every server-to-server endpoint; PublicURL, when set, is the
// browser-facing base of the authorization endpoint.
type Options struct {
	Issuer, PublicURL                 string
	ClientID, ClientSecret, SyncToken string
	Directory                         Directory
	// Claims, when set, edits ID token claims before signing (tests of issuer, audience and nonce checks).
	Claims func(map[string]any)
}

// Mock is a GitLab mock. It is safe for concurrent use.
type Mock struct {
	o   Options
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]pendingCode
}

type pendingCode struct {
	account   int64
	nonce     string
	challenge string
	redirect  string
}

// New builds a mock with a freshly generated signing key.
func New(o Options) (*Mock, error) {
	o.Issuer = strings.TrimRight(o.Issuer, "/")
	o.PublicURL = strings.TrimRight(o.PublicURL, "/")
	if o.PublicURL == "" {
		o.PublicURL = o.Issuer
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("gitlab mock: signing key: %w", err)
	}
	return &Mock{o: o, key: key, codes: map[string]pendingCode{}}, nil
}

// Handler serves the mock's endpoints.
func (m *Mock) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", m.discovery)
	mux.HandleFunc("GET /oauth/discovery/keys", m.keys)
	mux.HandleFunc("GET /oauth/authorize", m.authorize)
	mux.HandleFunc("POST /oauth/authorize", m.approve)
	mux.HandleFunc("POST /oauth/token", m.token)
	mux.HandleFunc("GET /api/v4/users/{id}", m.user)
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	return mux
}

// checkAuthorization validates an authorization request the way GitLab would before showing its sign-in page.
func (m *Mock) checkAuthorization(q url.Values) error {
	switch {
	case q.Get("client_id") != m.o.ClientID:
		return errors.New("unknown client_id")
	case q.Get("response_type") != "code":
		return errors.New("response_type must be code")
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		return errors.New("an S256 code challenge is required")
	case q.Get("redirect_uri") == "":
		return errors.New("redirect_uri is required")
	}
	for _, scope := range []string{"openid", "profile", "email"} {
		if !strings.Contains(" "+q.Get("scope")+" ", " "+scope+" ") {
			return fmt.Errorf("scope %q is required", scope)
		}
	}
	return nil
}

// IssueCode approves the authorization request q as account id and returns the callback URL GitLab would
// redirect the browser to.
func (m *Mock) IssueCode(id int64, q url.Values) (string, error) {
	if err := m.checkAuthorization(q); err != nil {
		return "", err
	}
	cb, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		return "", fmt.Errorf("redirect_uri: %w", err)
	}
	code := random()
	m.mu.Lock()
	m.codes[code] = pendingCode{account: id, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	m.mu.Unlock()
	cq := cb.Query()
	cq.Set("code", code)
	cq.Set("state", q.Get("state"))
	cb.RawQuery = cq.Encode()
	return cb.String(), nil
}

func (m *Mock) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                m.o.Issuer,
		"authorization_endpoint":                m.o.PublicURL + "/oauth/authorize",
		"token_endpoint":                        m.o.Issuer + "/oauth/token",
		"jwks_uri":                              m.o.Issuer + "/oauth/discovery/keys",
		"userinfo_endpoint":                     m.o.Issuer + "/oauth/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (m *Mock) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &m.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
}

// token redeems a code once: client authentication, redirect URI match and the PKCE verifier are all checked.
func (m *Mock) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	m.mu.Lock()
	pc, found := m.codes[r.Form.Get("code")]
	delete(m.codes, r.Form.Get("code"))
	m.mu.Unlock()
	acct, known := m.o.Directory.Lookup(pc.account)
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	switch {
	case id != m.o.ClientID || secret != m.o.ClientSecret:
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	case !found || !known || r.Form.Get("redirect_uri") != pc.redirect:
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != pc.challenge:
		http.Error(w, `{"error":"invalid_grant","error_description":"PKCE verification failed"}`, http.StatusBadRequest)
		return
	}
	idToken, err := m.idToken(acct, pc.nonce)
	if err != nil {
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "gitlab-access-" + random(),
		"token_type":   "Bearer",
		"expires_in":   7200,
		"id_token":     idToken,
	})
}

func (m *Mock) idToken(a Account, nonce string) (string, error) {
	claims := map[string]any{
		"iss": m.o.Issuer, "aud": m.o.ClientID, "sub": strconv.FormatInt(a.ID, 10), "nonce": nonce,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	if a.Email != "" {
		claims["email"] = a.Email
		claims["email_verified"] = a.EmailVerified
	}
	if m.o.Claims != nil {
		m.o.Claims(claims)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: m.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	sig, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return sig.CompactSerialize()
}

func (m *Mock) user(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("PRIVATE-TOKEN") != m.o.SyncToken {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, ok := m.o.Directory.Lookup(id)
	if !ok || a.Deleted {
		http.Error(w, `{"message":"404 User Not Found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"id": a.ID, "username": fmt.Sprintf("user%d", a.ID), "state": a.State, "locked": a.Locked})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func random() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
