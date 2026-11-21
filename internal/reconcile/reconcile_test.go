package reconcile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

var t0 = time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)

// fakeGenerations answers lookups from a map; missing ids are not available yet.
type fakeGenerations struct {
	mu    sync.Mutex
	known map[string]Generation
	err   error
	asked []string
}

func (f *fakeGenerations) Lookup(_ context.Context, id string) (Generation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, id)
	if f.err != nil {
		return Generation{}, f.err
	}
	g, ok := f.known[id]
	if !ok {
		return Generation{}, errNotYet
	}
	return g, nil
}

func setup(t *testing.T) (*store.Store, *fakeGenerations, *Reconciler, *time.Time) {
	t.Helper()
	st := storetest.Open(t)
	gens := &fakeGenerations{known: map[string]Generation{}}
	now := t0
	r := New(st, gens, 15*time.Minute)
	r.now = func() time.Time { return now }
	return st, gens, r, &now
}

func begin(t *testing.T, st *store.Store, id string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := st.TouchSession(ctx, "s1", "a@x", nil, "", at); err != nil {
		t.Fatal(err)
	}
	if err := st.StartAttempt(ctx, store.Attempt{ID: id, RequestID: id, UserEmail: "a@x", SessionID: "s1", RequestedModel: "glm", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, st *store.Store, id string) store.Attempt {
	t.Helper()
	all, _ := st.ListAttempts(context.Background(), store.AttemptFilter{}, 100)
	for _, a := range all {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no attempt %s", id)
	return store.Attempt{}
}

func amount(v money.Amount) *money.Amount { return &v }

func TestPendingCostIsSettledOnSchedule(t *testing.T) {
	st, gens, r, now := setup(t)
	ctx := context.Background()
	begin(t, st, "a1", t0)
	_ = st.FinishAttempt(ctx, "a1", store.AttemptResult{GenerationID: "gen-1", FinishedAt: t0, Outcome: store.OutcomeCancelled})

	// Not due before the first delay.
	if res := r.runOnce(ctx); res.Looked != 0 {
		t.Fatalf("early pass = %+v", res)
	}
	*now = t0.Add(5 * time.Second)
	if res := r.runOnce(ctx); res.Looked != 1 || res.Retried != 1 {
		t.Fatalf("not-yet pass = %+v", res)
	}
	// The next try waits until 15s after the finish, not 15s after the failed try.
	*now = t0.Add(14 * time.Second)
	if res := r.runOnce(ctx); res.Looked != 0 {
		t.Fatalf("retry too early: %+v", res)
	}
	gens.known["gen-1"] = Generation{Cost: 4215, Provider: "Z.AI"}
	*now = t0.Add(15 * time.Second)
	if res := r.runOnce(ctx); res.Settled != 1 {
		t.Fatalf("settle pass = %+v", res)
	}
	a := get(t, st, "a1")
	if a.CostState != store.CostConfirmed || *a.Cost != 4215 || a.InferenceProvider != "Z.AI" {
		t.Fatalf("attempt = %+v", a)
	}
	// Reconciled attempts are not looked up again.
	*now = t0.Add(48 * time.Hour)
	if res := r.runOnce(ctx); res.Looked != 0 {
		t.Fatalf("looked again: %+v", res)
	}
}

func TestStreamCostIsVerifiedAndCorrected(t *testing.T) {
	st, gens, r, now := setup(t)
	ctx := context.Background()
	begin(t, st, "same", t0)
	begin(t, st, "diff", t0)
	_ = st.FinishAttempt(ctx, "same", store.AttemptResult{GenerationID: "g-same", FinishedAt: t0, Outcome: store.OutcomeSuccess, Cost: amount(100)})
	_ = st.FinishAttempt(ctx, "diff", store.AttemptResult{GenerationID: "g-diff", FinishedAt: t0, Outcome: store.OutcomeSuccess, Cost: amount(100)})
	gens.known["g-same"] = Generation{Cost: 100, Provider: "P1"}
	gens.known["g-diff"] = Generation{Cost: 130, Provider: "P2"}
	*now = t0.Add(time.Minute)
	res := r.runOnce(ctx)
	if res.Looked != 2 || res.Settled != 1 || res.Verified != 1 {
		t.Fatalf("pass = %+v", res)
	}
	if a := get(t, st, "same"); a.InferenceProvider != "P1" || *a.Cost != 100 {
		t.Fatalf("same = %+v", a)
	}
	ev, _ := st.ChargeEvents(ctx, "diff")
	if len(ev) != 2 || ev[1].Source != store.ChargeGeneration || ev[1].Cost != 130 || *ev[1].Previous != 100 {
		t.Fatalf("correction not audited: %+v", ev)
	}
}

func TestInterruptedAttemptsAreRecovered(t *testing.T) {
	st, gens, r, now := setup(t)
	ctx := context.Background()
	begin(t, st, "crashed", t0)
	_ = st.RecordGeneration(ctx, "crashed", "gen-c")
	begin(t, st, "crashed-blind", t0)
	gens.known["gen-c"] = Generation{Cost: 7}

	*now = t0.Add(10 * time.Minute) // still within the stale threshold
	if res := r.runOnce(ctx); res.Recovered != 0 {
		t.Fatalf("recovered too early: %+v", res)
	}
	*now = t0.Add(16 * time.Minute)
	res := r.runOnce(ctx)
	if res.Recovered != 2 || res.Settled != 1 {
		t.Fatalf("pass = %+v", res)
	}
	if a := get(t, st, "crashed"); a.CostState != store.CostConfirmed || *a.Cost != 7 {
		t.Fatalf("crashed = %+v", a)
	}
	// Without a generation id there is nothing to look up: its cost stays unknown.
	if a := get(t, st, "crashed-blind"); a.CostState != store.CostPending || a.Outcome != store.OutcomeError {
		t.Fatalf("blind = %+v", a)
	}
}

func TestLookupErrorsAreRetriedHourlyAfterTheSchedule(t *testing.T) {
	st, gens, r, now := setup(t)
	ctx := context.Background()
	begin(t, st, "a1", t0)
	_ = st.FinishAttempt(ctx, "a1", store.AttemptResult{GenerationID: "gen-1", FinishedAt: t0, Outcome: store.OutcomeSuccess})
	gens.err = errors.New("OpenRouter down")
	for i, offset := range []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, 75 * time.Minute, 135 * time.Minute} {
		*now = t0.Add(offset)
		if res := r.runOnce(ctx); res.Looked != 1 || res.Retried != 1 {
			t.Fatalf("try %d at +%s: %+v", i, offset, res)
		}
		*now = t0.Add(offset + time.Second)
		if res := r.runOnce(ctx); res.Looked != 0 {
			t.Fatalf("try %d repeated immediately", i)
		}
	}
}

func TestOpenRouterGenerations(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.Query().Get("id"), r.Header.Get("Authorization")
		switch r.URL.Query().Get("id") {
		case "gen-ok":
			_, _ = w.Write([]byte(`{"data":{"id":"gen-ok","total_cost":4.215e-04,"provider_name":"Z.AI"}}`))
		case "gen-missing":
			http.Error(w, `{"error":{"message":"Generation not found"}}`, http.StatusNotFound)
		case "gen-bad":
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			http.Error(w, "boom", http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL + "/api/v1")
	g := NewOpenRouterGenerations(u, "or-key")
	ctx := context.Background()

	got, err := g.Lookup(ctx, "gen-ok")
	if err != nil || got.Cost != 421_500 || got.Provider != "Z.AI" {
		t.Fatalf("ok = %+v, %v", got, err)
	}
	if gotPath != "/api/v1/generation" || gotQuery != "gen-ok" || gotAuth != "Bearer or-key" {
		t.Fatalf("request %s ?id=%s auth %q", gotPath, gotQuery, gotAuth)
	}
	if _, err := g.Lookup(ctx, "gen-missing"); !errors.Is(err, errNotYet) {
		t.Fatalf("404: %v", err)
	}
	if _, err := g.Lookup(ctx, "gen-bad"); err == nil || errors.Is(err, errNotYet) {
		t.Fatalf("no cost: %v", err)
	}
	if _, err := g.Lookup(ctx, "other"); err == nil || errors.Is(err, errNotYet) {
		t.Fatalf("502: %v", err)
	}
}

func TestRunPassesUntilCancelled(t *testing.T) {
	st, gens, r, now := setup(t)
	ctx := context.Background()
	begin(t, st, "a1", t0)
	_ = st.FinishAttempt(ctx, "a1", store.AttemptResult{GenerationID: "gen-1", FinishedAt: t0, Outcome: store.OutcomeSuccess})
	gens.known["gen-1"] = Generation{Cost: 1}
	*now = t0.Add(time.Minute)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { r.Run(runCtx, time.Hour); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for get(t, st, "a1").Cost == nil {
		if time.Now().After(deadline) {
			t.Fatal("Run did not pass immediately")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// An attempt that finished long ago must not be retried on every pass after a failure.
func TestOldAttemptsBackOffFromNow(t *testing.T) {
	st, gens, r, now := setup(t)
	ctx := context.Background()
	begin(t, st, "old", t0)
	_ = st.FinishAttempt(ctx, "old", store.AttemptResult{GenerationID: "gen-old", FinishedAt: t0, Outcome: store.OutcomeSuccess})
	gens.err = errors.New("OpenRouter down")
	*now = t0.Add(48 * time.Hour)
	if res := r.runOnce(ctx); res.Retried != 1 {
		t.Fatalf("first pass = %+v", res)
	}
	// The next schedule step after the first try is 10 seconds; it is measured from now.
	*now = t0.Add(48*time.Hour + 9*time.Second)
	if res := r.runOnce(ctx); res.Looked != 0 {
		t.Fatalf("retried before one step passed: %+v", res)
	}
	*now = t0.Add(48*time.Hour + 10*time.Second)
	if res := r.runOnce(ctx); res.Looked != 1 {
		t.Fatalf("not retried after one step: %+v", res)
	}
}

// brokenStore fails the step named by failAt.
type brokenStore struct {
	Store
	failAt string
}

func (b brokenStore) RecoverInterrupted(context.Context, time.Time, time.Time) (int64, error) {
	if b.failAt == "recover" {
		return 0, errors.New("db down")
	}
	return 3, nil
}

func (b brokenStore) DueReconciliations(context.Context, time.Time, int) ([]store.Reconciliation, error) {
	return nil, errors.New("db down")
}

func TestStorageFailuresAreReported(t *testing.T) {
	if res := New(brokenStore{failAt: "recover"}, nil, time.Minute).runOnce(context.Background()); res != (passResult{Failed: true}) {
		t.Fatalf("recover failure = %+v", res)
	}
	if res := New(brokenStore{failAt: "due"}, nil, time.Minute).runOnce(context.Background()); res != (passResult{Recovered: 3, Failed: true}) {
		t.Fatalf("due failure = %+v", res)
	}
}
