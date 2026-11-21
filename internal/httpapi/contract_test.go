package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/login"
)

// documented lists the operations of the embedded OpenAPI document with sample paths.
func documented(t *testing.T) []apiOperation {
	t.Helper()
	ops, err := apiOperations(api.Document)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("the document has no operations")
	}
	return ops
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

func samplePath(op apiOperation) string { return pathParam.ReplaceAllString(op.Path, "x") }

// probe sends a request with a malformed body and parameter, so only routing and authentication decide the result.
func probe(t *testing.T, srv *httptest.Server, method, path, token, origin string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path+"?limit=abc", strings.NewReader("{"))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body.Error.Message
}

func contractServer(t *testing.T) *httptest.Server {
	t.Helper()
	routes := registerFunc(func(*http.ServeMux) {})
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: users, Proxy: http.NotFoundHandler(),
		Dashboard: http.NotFoundHandler(), Login: routes, PublicURL: publicURL,
		UpstreamReady: func(context.Context) error { return nil }}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEveryOperationIsRouted(t *testing.T) {
	srv := contractServer(t)
	for _, op := range documented(t) {
		status, message := probe(t, srv, op.Method, samplePath(op), "", "")
		switch {
		case op.Secured && (status != http.StatusUnauthorized || message != "unauthenticated"):
			t.Errorf("%s %s without a credential: %d %q, want 401 unauthenticated", op.Method, op.Path, status, message)
		case !op.Secured && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed):
			t.Errorf("%s %s is not routed: %d", op.Method, op.Path, status)
		}
	}
	if status, _ := probe(t, srv, http.MethodHead, "/api/me", "", ""); status != http.StatusUnauthorized {
		t.Errorf("HEAD /api/me without a credential: %d, want 401", status)
	}
}

func TestCookieMutationsNeedOrigin(t *testing.T) {
	srv, _ := testServer(t, tierConfig(t))
	for _, op := range documented(t) {
		if !op.Secured || op.Method == http.MethodGet {
			continue
		}
		if status, message := probe(t, srv, op.Method, samplePath(op), "cookie-admin", ""); status != http.StatusForbidden || message != "cross-origin request rejected" {
			t.Errorf("%s %s by cookie without Origin: %d %q", op.Method, op.Path, status, message)
		}
		if _, message := probe(t, srv, op.Method, samplePath(op), "admin", ""); message == "cross-origin request rejected" {
			t.Errorf("%s %s by bearer key was rejected as cross-origin", op.Method, op.Path)
		}
		if _, message := probe(t, srv, op.Method, samplePath(op), "cookie-admin", publicURL); message == "cross-origin request rejected" {
			t.Errorf("%s %s by cookie with the public Origin was rejected", op.Method, op.Path)
		}
	}
}

func TestAdminOperationsRefuseUsers(t *testing.T) {
	srv, _ := testServer(t, tierConfig(t))
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/users/bob@example.com/revoke"},
	} {
		if status, message := probe(t, srv, c.method, c.path, "alice", ""); status != http.StatusForbidden || message != "administrator role required" {
			t.Errorf("%s %s as a user: %d %q", c.method, c.path, status, message)
		}
	}
}

func TestBadParameterIsErrorBody(t *testing.T) {
	srv := contractServer(t)
	status, message := probe(t, srv, http.MethodGet, "/api/requests", "alice", "")
	if status != http.StatusBadRequest || message == "" {
		t.Fatalf("GET /api/requests?limit=abc: %d %q, want 400 with an error message", status, message)
	}
}

// fakeLogin answers every device-login poll with tok or err.
type fakeLogin struct {
	tok login.DeviceTokenResponse
	err error
}

func (fakeLogin) Register(*http.ServeMux) {}

func (f fakeLogin) DeviceCode(context.Context, string) (login.DeviceCodeResponse, error) {
	return login.DeviceCodeResponse{DeviceCode: "d", UserCode: "BCDF-GHJK"}, f.err
}

func (f fakeLogin) DeviceToken(context.Context, login.DeviceTokenRequest) (login.DeviceTokenResponse, error) {
	return f.tok, f.err
}

func loginServer(t *testing.T, l Login) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: users, Proxy: http.NotFoundHandler(),
		Dashboard: http.NotFoundHandler(), Login: l, UpstreamReady: func(context.Context) error { return nil }}))
	t.Cleanup(srv.Close)
	return srv
}

const devicePoll = `{"grant_type":"urn:ietf:params:oauth:grant-type:device_code","client_id":"opencode-cli","device_code":"d"}`

func postDevice(t *testing.T, srv *httptest.Server, path, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// Device keys and codes are shown once: no cache may keep them.
func TestDeviceLoginIsNotCached(t *testing.T) {
	srv := loginServer(t, fakeLogin{tok: login.DeviceTokenResponse{AccessToken: "wrk_new", OrgID: "wrench"}})
	for _, c := range []struct{ path, body string }{
		{"/auth/device/code", `{"client_id":"opencode-cli"}`},
		{"/auth/device/token", devicePoll},
	} {
		if resp, _ := postDevice(t, srv, c.path, c.body); resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d Cache-Control %q", c.path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
}

func TestDeviceLoginRefusalsAndFailures(t *testing.T) {
	refused := loginServer(t, fakeLogin{err: &login.DeviceTokenError{Code: "access_denied"}})
	if resp, out := postDevice(t, refused, "/auth/device/token", devicePoll); resp.StatusCode != http.StatusBadRequest || out["error"] != "access_denied" {
		t.Errorf("refusal: %d %v", resp.StatusCode, out)
	}
	if resp, out := postDevice(t, loginServer(t, fakeLogin{}), "/auth/device/token", "{"); resp.StatusCode != http.StatusBadRequest || out["error"] == nil {
		t.Errorf("malformed body: %d %v", resp.StatusCode, out)
	}
	if resp, _ := postDevice(t, loginServer(t, nil), "/auth/device/token", devicePoll); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("without login: %d", resp.StatusCode)
	}
}

// TestEmptyListsAreArrays guards the dashboard, which maps over every list: an empty result is [], never null.
func TestEmptyListsAreArrays(t *testing.T) {
	srv, _ := testServer(t, tierConfig(t))
	for _, c := range []struct {
		path   string
		fields []string // dot-separated paths to arrays
	}{
		{"/api/requests", []string{"attempts"}},
		{"/api/sessions", []string{"sessions"}},
		{"/api/keys", []string{"keys"}},
		{"/api/alerts", []string{"alerts"}},
		{"/api/users", []string{"users"}},
		{"/api/usage?window=day&group=user", []string{"groups"}},
		{"/api/stats", []string{"stats.buckets", "stats.by_user", "stats.by_model", "stats.by_provider"}},
		{"/api/system", []string{"models", "tiers", "budgets.warn_at_percent"}},
	} {
		var body map[string]any
		if code := get(t, srv, c.path, "admin", &body); code != http.StatusOK {
			t.Errorf("%s: %d", c.path, code)
			continue
		}
		for _, f := range c.fields {
			var v any = body
			for _, key := range strings.Split(f, ".") {
				m, _ := v.(map[string]any)
				v = m[key]
			}
			if _, ok := v.([]any); !ok {
				t.Errorf("%s %s = %#v, want an array", c.path, f, v)
			}
		}
	}
}

func TestStatsAcceptsEmptyFilters(t *testing.T) {
	srv, _ := testServer(t, tierConfig(t))
	if code := get(t, srv, "/api/stats?user=&model=&provider=&session=&from=&to=&bucket=", "alice", nil); code != http.StatusOK {
		t.Fatalf("stats with empty filters: %d", code)
	}
}
