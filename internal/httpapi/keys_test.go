package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/login"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

const publicURL = "https://wrench.example.com"

func send(t *testing.T, srv *httptest.Server, method, path, token, origin string) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+token)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func keyServer(t *testing.T) (*httptest.Server, *store.Store, map[string]int64) {
	t.Helper()
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ids := map[string]int64{}
	for i, email := range []string{"alice@example.com", "bob@example.com"} {
		u, err := st.BindIdentity(ctx, email, "https://gitlab.example.com", strconv.Itoa(i+1), int64(i+1), now)
		if err != nil {
			t.Fatal(err)
		}
		k, err := st.IssueClientKey(ctx, u.ID, "laptop", "Laptop", []byte(email), now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		ids[email] = k.ID
		_ = st.CreateWebSession(ctx, u.ID, []byte("s-"+email), now, now.Add(time.Hour))
	}
	srv := httptest.NewServer(New(Deps{Store: st, Auth: users, Proxy: http.NotFoundHandler(),
		Dashboard: http.NotFoundHandler(), PublicURL: publicURL, UpstreamReady: func(context.Context) error { return nil }}))
	t.Cleanup(srv.Close)
	return srv, st, ids
}

func TestKeysAreScopedToTheirOwner(t *testing.T) {
	srv, st, ids := keyServer(t)
	var out struct {
		Keys []store.ClientKey `json:"keys"`
	}
	if code := get(t, srv, "/api/keys", "alice", &out); code != http.StatusOK || len(out.Keys) != 1 || out.Keys[0].UserEmail != "alice@example.com" {
		t.Fatalf("alice keys: %d %+v", code, out.Keys)
	}
	if code := get(t, srv, "/api/keys?user=bob@example.com", "alice", nil); code != http.StatusForbidden {
		t.Fatalf("alice escaped her scope: %d", code)
	}
	if code := get(t, srv, "/api/keys", "admin", &out); code != http.StatusOK || len(out.Keys) != 0 {
		t.Fatalf("admin own keys: %+v", out.Keys)
	}
	if code := get(t, srv, "/api/keys?user=*", "admin", &out); code != http.StatusOK || len(out.Keys) != 2 {
		t.Fatalf("admin keys: %+v", out.Keys)
	}

	bobKey := "/api/keys/" + strconv.FormatInt(ids["bob@example.com"], 10)
	if code := send(t, srv, http.MethodDelete, bobKey, "alice", ""); code != http.StatusNotFound {
		t.Fatalf("alice revoked bob's key: %d", code)
	}
	if code := send(t, srv, http.MethodDelete, "/api/keys/"+strconv.FormatInt(ids["alice@example.com"], 10), "alice", ""); code != http.StatusNoContent {
		t.Fatalf("own revoke: %d", code)
	}
	if code := send(t, srv, http.MethodDelete, bobKey, "admin", ""); code != http.StatusNoContent {
		t.Fatalf("admin revoke: %d", code)
	}
	if code := send(t, srv, http.MethodDelete, "/api/keys/abc", "admin", ""); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", code)
	}
	keys, _ := st.ListClientKeys(context.Background(), "")
	for _, k := range keys {
		if k.RevokedAt == nil {
			t.Fatalf("key %d not revoked", k.ID)
		}
	}
}

func TestAdminRevokesAllCredentials(t *testing.T) {
	srv, st, _ := keyServer(t)
	if code := send(t, srv, http.MethodPost, "/api/users/bob@example.com/revoke", "alice", ""); code != http.StatusForbidden {
		t.Fatalf("non-admin: %d", code)
	}
	if code := send(t, srv, http.MethodPost, "/api/users/nobody@example.com/revoke", "admin", ""); code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", code)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/users/bob@example.com/revoke", nil)
	req.Header.Set("Authorization", "Bearer admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Revoked int64 `json:"revoked"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || out.Revoked != 2 {
		t.Fatalf("revoke: %d %+v", resp.StatusCode, out)
	}
	if _, err := st.LookupWebSession(context.Background(), []byte("s-bob@example.com"), time.Now()); err == nil {
		t.Fatal("bob's session survived")
	}
}

func TestCookieMutationsNeedSameOrigin(t *testing.T) {
	srv, _, ids := keyServer(t)
	path := "/api/keys/" + strconv.FormatInt(ids["alice@example.com"], 10)
	if code := send(t, srv, http.MethodDelete, path, "cookie", ""); code != http.StatusForbidden {
		t.Fatalf("no origin: %d", code)
	}
	if code := send(t, srv, http.MethodDelete, path, "cookie", "https://evil.example.com"); code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", code)
	}
	if code := send(t, srv, http.MethodPost, "/api/users/bob@example.com/revoke", "cookie-admin", "https://evil.example.com"); code != http.StatusForbidden {
		t.Fatalf("foreign origin admin: %d", code)
	}
	if code := send(t, srv, http.MethodDelete, path, "cookie", publicURL); code != http.StatusNoContent {
		t.Fatalf("same origin: %d", code)
	}
}

func TestInferenceNeedsFreshStatus(t *testing.T) {
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: users, Dashboard: http.NotFoundHandler(),
		Proxy:         http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		UpstreamReady: func(context.Context) error { return nil }}))
	defer srv.Close()
	if code := send(t, srv, http.MethodPost, "/v1/chat/completions", "alice", ""); code != http.StatusTeapot {
		t.Fatalf("fresh: %d", code)
	}
	if code := send(t, srv, http.MethodPost, "/v1/chat/completions", "stale", ""); code != http.StatusForbidden {
		t.Fatalf("stale: %d", code)
	}
	// Reading one's own data does not need a fresh status.
	if code := get(t, srv, "/api/me", "stale", nil); code != http.StatusOK {
		t.Fatalf("stale /api/me: %d", code)
	}
}

func TestLoginRoutesAreMounted(t *testing.T) {
	mount := registerFunc(func(m *http.ServeMux) {
		m.HandleFunc("GET /auth/gitlab/login", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusFound) })
	})
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: users, Proxy: http.NotFoundHandler(),
		Dashboard: http.NotFoundHandler(), Login: mount,
		UpstreamReady: func(context.Context) error { return nil }}))
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/auth/gitlab/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login route: %d", resp.StatusCode)
	}
}

type registerFunc func(*http.ServeMux)

func (f registerFunc) Register(m *http.ServeMux) { f(m) }

// DeviceCode and DeviceToken refuse every device login; registerFunc tests exercise the other login routes.
func (registerFunc) DeviceCode(ctx context.Context, clientID string) (login.DeviceCodeResponse, error) {
	return noDeviceLogin{}.DeviceCode(ctx, clientID)
}

func (registerFunc) DeviceToken(ctx context.Context, req login.DeviceTokenRequest) (login.DeviceTokenResponse, error) {
	return noDeviceLogin{}.DeviceToken(ctx, req)
}

// TestRevokeUserWithEncodedEmail follows the dashboard, which percent-encodes the email in the path.
func TestRevokeUserWithEncodedEmail(t *testing.T) {
	srv, _, _ := keyServer(t)
	if code := send(t, srv, http.MethodPost, "/api/users/bob%40example.com/revoke", "admin", ""); code != http.StatusOK {
		t.Fatalf("revoke bob%%40example.com: %d", code)
	}
	var out struct {
		Keys []struct {
			RevokedAt *time.Time `json:"revoked_at"`
		} `json:"keys"`
	}
	if code := get(t, srv, "/api/keys?user=bob@example.com", "admin", &out); code != http.StatusOK || len(out.Keys) != 1 || out.Keys[0].RevokedAt == nil {
		t.Fatalf("bob's keys after revoke: %d %+v", code, out)
	}
}

// recv reports whether topic arrives on ch within a moment.
func recv(ch <-chan events.Topic, topic events.Topic) bool {
	select {
	case got := <-ch:
		return got == topic
	case <-time.After(time.Second):
		return false
	}
}

func TestRevokeKeyPublishes(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"owner revokes", "alice"},
		{"admin revokes", "admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := context.Background()
			now := time.Now().UTC()
			u, err := st.BindIdentity(ctx, "alice@example.com", "https://gitlab.example.com", "1", 1, now)
			if err != nil {
				t.Fatal(err)
			}
			k, err := st.IssueClientKey(ctx, u.ID, "laptop", "Laptop", []byte("k"), now, now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			bus := events.New()
			srv := httptest.NewServer(New(Deps{Store: st, Auth: users, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(),
				PublicURL: publicURL, UpstreamReady: func(context.Context) error { return nil }, Events: bus}))
			t.Cleanup(srv.Close)
			aliceCh, stop := bus.Subscribe("alice@example.com", false)
			defer stop()
			otherCh, stopOther := bus.Subscribe("bob@example.com", false)
			defer stopOther()

			if code := send(t, srv, http.MethodDelete, "/api/keys/"+strconv.FormatInt(k.ID, 10), tt.token, ""); code != http.StatusNoContent {
				t.Fatalf("revoke: %d", code)
			}
			if !recv(aliceCh, events.Keys) {
				t.Fatal("the key's owner got no keys event")
			}
			select {
			case got := <-otherCh:
				t.Fatalf("another user got %q", got)
			default:
			}
		})
	}
}

// postJSON posts to path as token and decodes a 200 body into out.
func postJSON(t *testing.T, srv *httptest.Server, path, token string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestRevokeKeysByScope(t *testing.T) {
	tests := []struct {
		name        string
		token, path string
		status      int
		revoked     int64
		// revokedOwner is whose key is revoked afterwards; empty when none is.
		revokedOwner string
	}{
		{"own keys", "alice", "/api/keys/revoke", http.StatusOK, 1, "alice@example.com"},
		{"own keys by name", "alice", "/api/keys/revoke?user=alice@example.com", http.StatusOK, 1, "alice@example.com"},
		{"foreign user", "alice", "/api/keys/revoke?user=bob@example.com", http.StatusForbidden, 0, ""},
		{"admin names a user", "admin", "/api/keys/revoke?user=bob@example.com", http.StatusOK, 1, "bob@example.com"},
		{"admin own keys", "admin", "/api/keys/revoke", http.StatusOK, 0, ""},
		{"admin everyone", "admin", "/api/keys/revoke?user=*", http.StatusBadRequest, 0, ""},
		{"user everyone", "alice", "/api/keys/revoke?user=*", http.StatusForbidden, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st, _ := keyServer(t)
			var out struct {
				Revoked int64 `json:"revoked"`
			}
			if code := postJSON(t, srv, tt.path, tt.token, &out); code != tt.status || out.Revoked != tt.revoked {
				t.Fatalf("got %d %+v, want %d revoked %d", code, out, tt.status, tt.revoked)
			}
			keys, err := st.ListClientKeys(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range keys {
				if got := k.RevokedAt != nil; got != (k.UserEmail == tt.revokedOwner) {
					t.Fatalf("%s revoked = %v", k.UserEmail, got)
				}
			}
		})
	}
}

func TestRevokeKeysPublishesOnlyWhenKeysChanged(t *testing.T) {
	tests := []struct {
		name, token, path string
		want              bool
	}{
		{"keys revoked", "alice", "/api/keys/revoke", true},
		{"admin revokes another user's keys", "admin", "/api/keys/revoke?user=alice@example.com", true},
		{"nothing to revoke", "admin", "/api/keys/revoke", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := storetest.Open(t)
			ctx := context.Background()
			now := time.Now().UTC()
			u, err := st.BindIdentity(ctx, "alice@example.com", "https://gitlab.example.com", "1", 1, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.IssueClientKey(ctx, u.ID, "laptop", "Laptop", []byte("k"), now, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			bus := events.New()
			srv := httptest.NewServer(New(Deps{Store: st, Auth: users, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(),
				PublicURL: publicURL, UpstreamReady: func(context.Context) error { return nil }, Events: bus}))
			t.Cleanup(srv.Close)
			aliceCh, stop := bus.Subscribe("alice@example.com", false)
			defer stop()

			if code := postJSON(t, srv, tt.path, tt.token, nil); code != http.StatusOK {
				t.Fatalf("revoke: %d", code)
			}
			if got := recv(aliceCh, events.Keys); got != tt.want {
				t.Fatalf("keys event = %v, want %v", got, tt.want)
			}
		})
	}
}
