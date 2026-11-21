package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
)

var t0 = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

type fakeSource struct {
	entries []Entry
	err     error
}

func (f *fakeSource) Fetch(context.Context) ([]Entry, error) { return f.entries, f.err }

type fakeStore struct {
	entries []Entry
	at      time.Time
}

func (f *fakeStore) LoadModels(context.Context) ([]Entry, time.Time, error) {
	return f.entries, f.at, nil
}
func (f *fakeStore) ReplaceModels(_ context.Context, es []Entry, at time.Time) error {
	f.entries, f.at = es, at
	return nil
}

func TestLookupStatuses(t *testing.T) {
	st := &fakeStore{}
	c := New(&fakeSource{entries: []Entry{{UpstreamID: "a", ContextLength: 1000, MaxCompletionTokens: 100}}}, st, func() time.Time { return t0 })
	if _, s := c.Lookup("a"); s != StatusNoCatalog {
		t.Fatalf("before data: %s", s)
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id     string
		status Status
	}{{"a", StatusOK}, {"b", StatusUnknownUpstream}}
	for _, tt := range tests {
		if e, s := c.Lookup(tt.id); s != tt.status || (s == StatusOK && e.MaxCompletionTokens != 100) {
			t.Errorf("Lookup(%q) = %+v, %s; want %s", tt.id, e, s, tt.status)
		}
	}
	if len(st.entries) != 1 || !st.at.Equal(t0) || !c.FetchedAt().Equal(t0) {
		t.Errorf("not persisted: %+v at %v, fetched %v", st.entries, st.at, c.FetchedAt())
	}
}

func TestRefreshFailureKeepsCache(t *testing.T) {
	st := &fakeStore{entries: []Entry{{UpstreamID: "a", ContextLength: 1000, MaxCompletionTokens: 100}}, at: t0}
	c := New(&fakeSource{err: errors.New("down")}, st, func() time.Time { return t0.Add(time.Hour) })
	if err := c.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh succeeded against a failing source")
	}
	if _, s := c.Lookup("a"); s != StatusOK || !c.FetchedAt().Equal(t0) {
		t.Errorf("cache lost: %s, fetched %v", s, c.FetchedAt())
	}
}

func TestOpenRouterSource(t *testing.T) {
	var auth, path string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":[
			{"id":"a","context_length":2000,"top_provider":{"max_completion_tokens":null}},
			{"id":"b","context_length":4000,"top_provider":{"max_completion_tokens":512},"pricing":{"prompt":"0.0000003","completion":"0.0000012"}},
			{"id":"c","context_length":0,"top_provider":{"max_completion_tokens":10}}]}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL + "/api/v1")
	src := NewOpenRouterSource(u, "or-key")
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/models" || auth != "Bearer or-key" {
		t.Errorf("request %s with %q", path, auth)
	}
	want := []Entry{{UpstreamID: "a", ContextLength: 2000, MaxCompletionTokens: 2000}, {UpstreamID: "b", ContextLength: 4000, MaxCompletionTokens: 512, PricePrompt: "0.0000003", PriceCompletion: "0.0000012"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("entries = %+v, want %+v", got, want)
	}
	status = http.StatusBadGateway
	if _, err := src.Fetch(context.Background()); err == nil {
		t.Error("non-200 accepted")
	}
}

func TestRunChecksAndStops(t *testing.T) {
	c := New(&fakeSource{entries: []Entry{{UpstreamID: "a", ContextLength: 1, MaxCompletionTokens: 1}}}, &fakeStore{}, func() time.Time { return t0 })
	ctx, cancel := context.WithCancel(context.Background())
	checks := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() { c.Run(ctx, time.Hour, func() { checks <- struct{}{} }); close(done) }()
	<-checks
	if _, s := c.Lookup("a"); s != StatusOK {
		t.Errorf("after first check: %s", s)
	}
	cancel()
	<-done
}

func TestStatuses(t *testing.T) {
	c := New(&fakeSource{entries: []Entry{{UpstreamID: "a", ContextLength: 1, MaxCompletionTokens: 1}}}, &fakeStore{}, func() time.Time { return t0 })
	models := []config.Model{{Name: "m1", UpstreamID: "a"}, {Name: "m2", UpstreamID: "b"}}
	if got := c.Statuses(models); got[0].Status != StatusNoCatalog || got[1].Status != StatusNoCatalog {
		t.Fatalf("before data: %+v", got)
	}
	_ = c.Refresh(context.Background())
	got := c.Statuses(models)
	want := []ModelStatus{{Name: "m1", UpstreamID: "a", Status: StatusOK}, {Name: "m2", UpstreamID: "b", Status: StatusUnknownUpstream}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("statuses = %+v", got)
	}
	if bad := Unavailable(got); len(bad) != 1 || bad[0].Name != "m2" {
		t.Fatalf("unavailable = %+v", bad)
	}
}

func TestEmptyCatalogKeepsCache(t *testing.T) {
	st := &fakeStore{entries: []Entry{{UpstreamID: "a", ContextLength: 1000, MaxCompletionTokens: 100}}, at: t0}
	c := New(&fakeSource{entries: []Entry{}}, st, func() time.Time { return t0.Add(time.Hour) })
	if err := c.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("an empty catalog was accepted")
	}
	if _, s := c.Lookup("a"); s != StatusOK || len(st.entries) != 1 {
		t.Fatalf("cache replaced by an empty catalog: %s, %+v", s, st.entries)
	}
}

func TestRefreshNowWakesRun(t *testing.T) {
	c := New(&fakeSource{entries: []Entry{{UpstreamID: "a", ContextLength: 1, MaxCompletionTokens: 1}}}, &fakeStore{}, func() time.Time { return t0 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks := make(chan struct{}, 4)
	go c.Run(ctx, time.Hour, func() { checks <- struct{}{} })
	<-checks
	c.RefreshNow()
	select {
	case <-checks:
	case <-time.After(2 * time.Second):
		t.Fatal("RefreshNow did not trigger a refresh")
	}
}
