package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/config/configtest"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

// sessionServer serves a real store with a controllable clock and a one-minute offline timeout.
func sessionServer(t *testing.T) (*httptest.Server, *store.Store, *time.Time) {
	t.Helper()
	st := storetest.Open(t)
	clock := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	h := New(Deps{Store: st, Auth: users, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(),
		PublicURL: publicURL, UpstreamReady: func(context.Context) error { return nil },
		Config: config.Fixed(configtest.Load(t, configtest.YAML)), Sessions: SessionTiming{OfflineAfter: time.Minute},
		now: func() time.Time { return clock }})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, st, &clock
}

type liveSession struct {
	store.Session
	Status string `json:"status"`
}

func sessionList(t *testing.T, srv *httptest.Server, token string) []liveSession {
	t.Helper()
	var out struct {
		Sessions []liveSession `json:"sessions"`
	}
	if code := get(t, srv, "/api/sessions", token, &out); code != http.StatusOK {
		t.Fatalf("sessions: %d", code)
	}
	return out.Sessions
}

func TestStatusOf(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		seen time.Duration
		want liveStatus
	}{
		{time.Minute, statusActive},
		{5 * time.Minute, statusActive},
		{6 * time.Minute, statusOffline},
	}
	for _, tt := range tests {
		if got := statusOf(store.Session{LastSeenAt: now.Add(-tt.seen)}, now, 5*time.Minute); got != tt.want {
			t.Errorf("seen %v ago: %s, want %s", tt.seen, got, tt.want)
		}
	}
}

func TestSessionStatusFollowsLastRequest(t *testing.T) {
	srv, st, clock := sessionServer(t)
	if err := st.TouchSession(context.Background(), "s1", "alice@example.com", nil, "", *clock); err != nil {
		t.Fatal(err)
	}
	if got := sessionList(t, srv, "alice"); len(got) != 1 || got[0].Status != "active" {
		t.Fatalf("sessions = %+v", got)
	}
	if other := sessionList(t, srv, "bob"); len(other) != 0 {
		t.Fatalf("bob sees %+v", other)
	}
	*clock = clock.Add(61 * time.Second)
	if got := sessionList(t, srv, "alice"); got[0].Status != "offline" {
		t.Fatalf("after silence: %+v", got[0])
	}
}

func TestSessionStatusFilter(t *testing.T) {
	srv, st, clock := sessionServer(t)
	ctx := context.Background()
	if err := st.TouchSession(ctx, "old", "alice@example.com", nil, "opencode/1.0", clock.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchSession(ctx, "new", "alice@example.com", nil, "", *clock); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		query string
		want  []string
	}{
		{"", []string{"new", "old"}},
		{"?status=active", []string{"new"}},
		{"?status=offline", []string{"old"}},
	}
	for _, tt := range tests {
		var out struct {
			Sessions []struct {
				ID    string `json:"id"`
				Agent string `json:"agent"`
			} `json:"sessions"`
		}
		if code := get(t, srv, "/api/sessions"+tt.query, "alice", &out); code != http.StatusOK {
			t.Fatalf("%q: %d", tt.query, code)
		}
		var got []string
		for _, s := range out.Sessions {
			got = append(got, s.ID)
			if s.ID == "old" && s.Agent != "opencode/1.0" {
				t.Errorf("agent = %q", s.Agent)
			}
		}
		if strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%q: %v, want %v", tt.query, got, tt.want)
		}
	}
	if code := get(t, srv, "/api/sessions?status=bogus", "alice", nil); code != http.StatusBadRequest {
		t.Errorf("status=bogus: %d, want 400", code)
	}
}
