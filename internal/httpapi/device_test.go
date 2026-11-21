package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/gitlab/gitlabtest"
	"github.com/ordo-systems/wrench/internal/login"
)

// postJSON sends body to path without credentials and returns the status and the decoded object.
func (s *stack) postJSON(path, body string) (int, map[string]any) {
	s.t.Helper()
	resp, err := http.Post(s.server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		s.t.Fatalf("%s: %d %s", path, resp.StatusCode, raw)
	}
	return resp.StatusCode, out
}

// TestDeviceLoginOverHTTP checks the wire shapes opencode reads: the code pair, RFC error objects and a token that
// works as a device key.
func TestDeviceLoginOverHTTP(t *testing.T) {
	s := newStack(t, 24*time.Hour)
	ctx := context.Background()
	u, err := s.store.BindIdentity(ctx, "alice@example.com", "https://gitlab.example.com", "1", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.RecordStatus(ctx, u.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}

	code, c := s.postJSON("/auth/device/code", `{"client_id":"opencode-cli","supports_org_scope":true}`)
	if code != http.StatusOK || c["device_code"] == "" || c["expires_in"] != 600.0 || c["interval"] != 5.0 ||
		c["verification_uri_complete"] != s.server.URL+"/device?code="+c["user_code"].(string) {
		t.Fatalf("code: %d %v", code, c)
	}
	poll := `{"grant_type":"urn:ietf:params:oauth:grant-type:device_code","client_id":"opencode-cli","device_code":"` +
		c["device_code"].(string) + `"}`
	if code, e := s.postJSON("/auth/device/token", poll); code != http.StatusBadRequest || e["error"] != "authorization_pending" || len(e) != 1 {
		t.Fatalf("pending: %d %v", code, e)
	}
	if code, e := s.postJSON("/auth/device/code", `{"client_id":"curl"}`); code != http.StatusBadRequest || e["error"] != "invalid_client" {
		t.Fatalf("foreign client: %d %v", code, e)
	}

	_, c2 := s.postJSON("/auth/device/code", `{"client_id":"opencode-cli"}`)
	if err := s.store.DecideDeviceCode(ctx, login.UserCodeHash(c2["user_code"].(string)), u.Email, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	poll2 := strings.Replace(poll, c["device_code"].(string), c2["device_code"].(string), 1)
	code, tok := s.postJSON("/auth/device/token", poll2)
	if code != http.StatusOK || tok["org_id"] != "wrench" || tok["refresh_token"] == "" || tok["expires_in"] != (24*time.Hour).Seconds() {
		t.Fatalf("token: %d %v", code, tok)
	}
	if got := get(t, s.server, "/api/me", tok["access_token"].(string), nil); got != http.StatusOK {
		t.Fatalf("the access token is not a device key: %d", got)
	}
}

// The device login replaced the CLI login; its routes are not served (404, or 405 where the dashboard's GET
// catch-all matches the path).
func TestCLILoginRoutesAreGone(t *testing.T) {
	s := newStack(t, time.Hour)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/auth/cli/login?port=1024"},
		{http.MethodPost, "/auth/cli/exchange"},
	} {
		req, _ := http.NewRequest(c.method, s.server.URL+c.path, strings.NewReader(`{"code":"x","verifier":"y"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
}

// webSession signs a GitLab account in to the dashboard and returns the browser holding the session cookie.
func (s *stack) webSession(account int64) *browser {
	s.t.Helper()
	b := newBrowser(s.t)
	resp := b.do(http.MethodGet, s.server.URL+"/auth/gitlab/login", "")
	if resp = b.do(http.MethodGet, s.gitlab.Authorize(s.t, resp.Header.Get("Location"), account), ""); resp.StatusCode != http.StatusFound {
		s.t.Fatalf("web login: %d", resp.StatusCode)
	}
	return b
}

// decideDevice posts a decision with the browser's cookies (nil for none), an optional bearer key and Origin.
func (s *stack) decideDevice(b *browser, key, origin, userCode, decision string) int {
	s.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, s.server.URL+"/api/device",
		strings.NewReader(`{"user_code":"`+userCode+`","decision":"`+decision+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if b != nil {
		for k, v := range b.cookies {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestDeviceApproval(t *testing.T) {
	s := newStack(t, 24*time.Hour)
	s.gitlab.Add(gitlabtest.Account{ID: 1, Email: "alice@example.com", EmailVerified: true, State: "active"})
	b := s.webSession(1)
	start := func() (string, string) {
		_, c := s.postJSON("/auth/device/code", `{"client_id":"opencode-cli"}`)
		return c["device_code"].(string), c["user_code"].(string)
	}
	poll := func(device string) (int, map[string]any) {
		return s.postJSON("/auth/device/token", `{"grant_type":"urn:ietf:params:oauth:grant-type:device_code","client_id":"opencode-cli","device_code":"`+device+`"}`)
	}

	device, user := start()
	if code := s.decideDevice(b, "", s.server.URL, user, "approve"); code != http.StatusNoContent {
		t.Fatalf("approve: %d", code)
	}
	code, tok := poll(device)
	if code != http.StatusOK {
		t.Fatalf("poll after approval: %d %v", code, tok)
	}

	_, other := start()
	if code := s.decideDevice(nil, tok["access_token"].(string), "", other, "approve"); code != http.StatusForbidden {
		t.Fatalf("approval with a device key: %d", code)
	}
	if code := s.decideDevice(b, "", "", other, "approve"); code != http.StatusForbidden {
		t.Fatalf("approval without Origin: %d", code)
	}
	if code := s.decideDevice(b, "", s.server.URL, "BBBB-BBBB", "approve"); code != http.StatusNotFound {
		t.Fatalf("unknown code: %d", code)
	}
	if code := s.decideDevice(b, "", s.server.URL, other, "maybe"); code != http.StatusBadRequest {
		t.Fatalf("bad decision: %d", code)
	}
	denied, deniedUser := start()
	if code := s.decideDevice(b, "", s.server.URL, deniedUser, "deny"); code != http.StatusNoContent {
		t.Fatalf("deny: %d", code)
	}
	if code, e := poll(denied); code != http.StatusBadRequest || e["error"] != "access_denied" {
		t.Fatalf("poll after denial: %d %v", code, e)
	}

	// A session whose GitLab status is stale cannot approve devices.
	alice, err := s.store.UserByID(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.RecordStatus(context.Background(), alice.ID, true, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if code := s.decideDevice(b, "", s.server.URL, other, "approve"); code != http.StatusForbidden {
		t.Fatalf("stale status: %d", code)
	}
}
