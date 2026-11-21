//go:build integration

package e2e

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/ordo-systems/wrench/tests/internal/wrenchapi"
)

const (
	// The demo stack runs in HTTP-only mode, so its cookies carry no __Host- prefix.
	loginCookie   = "wrench_login"
	sessionCookie = "wrench_session"
)

// browser follows no redirects: Wrench cookies are Secure and the demo is plain HTTP, so Go's cookie jar would drop
// them; the tests carry them by hand, as a browser on localhost would.
var browser = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// toTest points a URL the demo hands to browsers (localhost) at the address the tests reach.
func toTest(t *testing.T, raw, base string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := url.Parse(base)
	u.Scheme, u.Host = b.Scheme, b.Host
	return u.String()
}

func cookie(resp *http.Response, name string) string {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func send(t *testing.T, req *http.Request, cookies map[string]string) (*http.Response, string) {
	t.Helper()
	for k, v := range cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	resp, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// signIn walks the browser flow through the GitLab mock's picker as email and returns the callback response.
func signIn(t *testing.T, email string) (*http.Response, string) {
	t.Helper()
	wrench, gitlab := baseURL(t, "WRENCH_E2E_URL"), baseURL(t, "WRENCH_E2E_GITLAB_URL")
	req, _ := http.NewRequest(http.MethodGet, wrench+"/auth/gitlab/login", nil)
	resp, _ := send(t, req, nil)
	binding := cookie(resp, loginCookie)
	if resp.StatusCode != http.StatusFound || binding == "" {
		t.Fatalf("login start: %d, binding cookie %q", resp.StatusCode, binding)
	}
	req, _ = http.NewRequest(http.MethodGet, toTest(t, resp.Header.Get("Location"), gitlab), nil)
	resp, page := send(t, req, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "user1@example.com") {
		t.Fatalf("picker: %d %s", resp.StatusCode, page)
	}
	form := url.Values{}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`).FindAllStringSubmatch(page, -1) {
		if !form.Has(m[1]) {
			form.Set(m[1], html.UnescapeString(m[2]))
		}
	}
	form.Set("email", email)
	req, _ = http.NewRequest(http.MethodPost, gitlab+"/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, page = send(t, req, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("picker post: %d %s", resp.StatusCode, page)
	}
	req, _ = http.NewRequest(http.MethodGet, toTest(t, resp.Header.Get("Location"), wrench), nil)
	return send(t, req, map[string]string{loginCookie: binding})
}

func TestBrowserSignIn(t *testing.T) {
	resp, page := signIn(t, "user1@example.com")
	session := cookie(resp, sessionCookie)
	if resp.StatusCode != http.StatusFound || session == "" {
		t.Fatalf("callback: %d %s", resp.StatusCode, page)
	}
	me, err := client(t, withCookie(sessionCookie, session)).GetMeWithResponse(context.Background())
	if err != nil || me.JSON200 == nil {
		t.Fatalf("/api/me: %v %v", me, err)
	}
	if me.JSON200.Email != "user1@example.com" || me.JSON200.Credential != wrenchapi.IdentityCredentialSession {
		t.Errorf("/api/me = %+v", *me.JSON200)
	}
}

func TestSignInRejectsUnknownEmail(t *testing.T) {
	resp, page := signIn(t, "nobody@example.com")
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(page, "not allowed") {
		t.Fatalf("callback: %d %s", resp.StatusCode, page)
	}
}
