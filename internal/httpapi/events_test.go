package httpapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/events"
)

// eventsServer serves /api/events over a bus on a store-less server.
func eventsServer(t *testing.T, bus *events.Bus) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(Deps{Auth: users, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(), Events: bus}))
	t.Cleanup(srv.Close)
	return srv
}

// openEvents connects as token and returns the response and a line reader; the stream closes with the test.
func openEvents(t *testing.T, srv *httptest.Server, token string) (*http.Response, <-chan string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	return resp, lines
}

func nextLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case l := <-lines:
			if l == want {
				return
			}
		case <-timeout:
			t.Fatalf("no line %q within 2s", want)
		}
	}
}

func TestEventsStreamTopics(t *testing.T) {
	bus := events.New()
	srv := eventsServer(t, bus)
	resp, lines := openEvents(t, srv, "alice")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
		resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("status %d, headers %v", resp.StatusCode, resp.Header)
	}
	// The handler subscribes before the headers are flushed, so these publishes cannot be missed.
	bus.Publish(events.Usage, "bob@example.com")
	bus.Publish(events.Usage, "alice@example.com")
	bus.Publish(events.Keys, "alice@example.com")
	var got []string
	timeout := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case l := <-lines:
			if strings.HasPrefix(l, "event:") {
				got = append(got, l)
			}
		case <-timeout:
			t.Fatalf("got %v", got)
		}
	}
	if got[0] != "event: usage" || got[1] != "event: keys" {
		t.Errorf("events = %v, want usage then keys (bob's usage must not reach alice)", got)
	}
}

func TestEventsKeepAlive(t *testing.T) {
	old := keepAlive
	keepAlive = 20 * time.Millisecond
	t.Cleanup(func() { keepAlive = old })
	_, lines := openEvents(t, eventsServer(t, events.New()), "alice")
	nextLine(t, lines, ": keep-alive")
}

func TestEventsRejections(t *testing.T) {
	tests := []struct {
		name  string
		bus   *events.Bus
		token string
		want  int
	}{
		{"anonymous", events.New(), "", http.StatusUnauthorized},
		{"no bus", nil, "alice", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := openEvents(t, eventsServer(t, tc.bus), tc.token)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}
