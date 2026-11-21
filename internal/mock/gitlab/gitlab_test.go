package gitlab

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
)

// mapDirectory is a fixed set of accounts for the core tests.
type mapDirectory map[int64]Account

func (d mapDirectory) Lookup(id int64) (Account, bool) { a, ok := d[id]; return a, ok }
func (d mapDirectory) Candidates() []string            { return nil }
func (d mapDirectory) SignIn(string) (int64, error)    { return 0, errors.New("no picker") }

const verifier = "a-verifier-that-is-long-enough-for-pkce-0123456789"

func challenge() string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func newMock(t *testing.T, o Options) (*Mock, *httptest.Server) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	if o.Issuer == "" {
		o.Issuer = "http://" + srv.Listener.Addr().String()
	}
	o.ClientID, o.ClientSecret, o.SyncToken = "cid", "secret", "sync"
	if o.Directory == nil {
		o.Directory = mapDirectory{7: {ID: 7, Email: "a@example.com", EmailVerified: true, State: "active"}}
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = m.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	return m, srv
}

func authQuery() url.Values {
	return url.Values{
		"client_id": {"cid"}, "response_type": {"code"}, "code_challenge_method": {"S256"},
		"code_challenge": {challenge()}, "redirect_uri": {"http://app/callback"}, "state": {"st"},
		"nonce": {"n1"}, "scope": {"openid profile email"},
	}
}

func getJSON(t *testing.T, u string, out any) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverySplitsURLs(t *testing.T) {
	_, srv := newMock(t, Options{PublicURL: "http://localhost:19000"})
	var d map[string]any
	getJSON(t, srv.URL+"/.well-known/openid-configuration", &d)
	want := map[string]string{
		"issuer":                 srv.URL,
		"token_endpoint":         srv.URL + "/oauth/token",
		"jwks_uri":               srv.URL + "/oauth/discovery/keys",
		"authorization_endpoint": "http://localhost:19000/oauth/authorize",
	}
	for k, v := range want {
		if d[k] != v {
			t.Errorf("%s = %v, want %s", k, d[k], v)
		}
	}
}

func TestIssueCodeRejects(t *testing.T) {
	m, _ := newMock(t, Options{})
	tests := []struct{ key, value string }{
		{"client_id", "other"},
		{"response_type", "token"},
		{"code_challenge_method", "plain"},
		{"redirect_uri", ""},
		{"scope", "openid profile"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			q := authQuery()
			q.Set(tt.key, tt.value)
			if _, err := m.IssueCode(7, q); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func exchange(t *testing.T, srv *httptest.Server, code, secret, verifier string) (int, map[string]any) {
	t.Helper()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://app/callback"},
		"code_verifier": {verifier}, "client_id": {"cid"}, "client_secret": {secret}}
	resp, err := http.PostForm(srv.URL+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestTokenExchange(t *testing.T) {
	m, srv := newMock(t, Options{})
	cb, err := m.IssueCode(7, authQuery())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(cb)
	if u.Host != "app" || u.Query().Get("state") != "st" {
		t.Fatalf("callback %s", cb)
	}
	code := u.Query().Get("code")
	if status, _ := exchange(t, srv, code, "wrong", verifier); status != http.StatusUnauthorized {
		t.Errorf("wrong secret: %d", status)
	}
	cb, _ = m.IssueCode(7, authQuery())
	u, _ = url.Parse(cb)
	if status, _ := exchange(t, srv, u.Query().Get("code"), "secret", "bad-verifier"); status != http.StatusBadRequest {
		t.Errorf("bad verifier: %d", status)
	}
	cb, _ = m.IssueCode(7, authQuery())
	u, _ = url.Parse(cb)
	code = u.Query().Get("code")
	status, out := exchange(t, srv, code, "secret", verifier)
	if status != http.StatusOK {
		t.Fatalf("exchange: %d %v", status, out)
	}
	claims := idClaims(t, out["id_token"].(string))
	if claims["sub"] != "7" || claims["email"] != "a@example.com" || claims["nonce"] != "n1" || claims["iss"] != srv.URL {
		t.Errorf("claims %v", claims)
	}
	if status, _ := exchange(t, srv, code, "secret", verifier); status != http.StatusBadRequest {
		t.Errorf("reused code: %d", status)
	}
}

func TestClaimsHook(t *testing.T) {
	m, srv := newMock(t, Options{Claims: func(c map[string]any) { c["aud"] = "other" }})
	cb, _ := m.IssueCode(7, authQuery())
	u, _ := url.Parse(cb)
	_, out := exchange(t, srv, u.Query().Get("code"), "secret", verifier)
	if aud := idClaims(t, out["id_token"].(string))["aud"]; aud != "other" {
		t.Errorf("aud %v", aud)
	}
}

func idClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	sig, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(sig.UnsafePayloadWithoutVerification(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUsersAPI(t *testing.T) {
	_, srv := newMock(t, Options{})
	tests := []struct {
		path, token string
		status      int
		contains    string
	}{
		{"/api/v4/users/7", "", http.StatusUnauthorized, "Unauthorized"},
		{"/api/v4/users/8", "sync", http.StatusNotFound, "Not Found"},
		{"/api/v4/users/7", "sync", http.StatusOK, `"state":"active"`},
	}
	for _, tt := range tests {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+tt.path, nil)
		if tt.token != "" {
			req.Header.Set("PRIVATE-TOKEN", tt.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tt.status || !strings.Contains(string(b), tt.contains) {
			t.Errorf("%s token %q: %d %s", tt.path, tt.token, resp.StatusCode, b)
		}
	}
}
