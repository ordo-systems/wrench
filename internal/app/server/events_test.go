package server

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/store"
)

type published struct {
	topic events.Topic
	user  string
}

// recordingPublisher keeps every published event in order.
type recordingPublisher struct {
	mu  sync.Mutex
	got []published
}

func (r *recordingPublisher) Publish(t events.Topic, user string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, published{t, user})
}

func (r *recordingPublisher) take() []published {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.got
	r.got = nil
	return out
}

// fakeJournal accepts every call unless err is set.
type fakeJournal struct{ err error }

func (f fakeJournal) TouchSession(context.Context, string, string, *int64, string, time.Time) error {
	return f.err
}
func (f fakeJournal) StartAttempt(context.Context, store.Attempt) error                { return f.err }
func (f fakeJournal) RecordGeneration(context.Context, string, string) error           { return f.err }
func (f fakeJournal) FinishAttempt(context.Context, string, store.AttemptResult) error { return f.err }

func TestPublishingJournal(t *testing.T) {
	ctx := context.Background()
	rec := &recordingPublisher{}
	j := newPublishingJournal(fakeJournal{}, rec)

	if err := j.StartAttempt(ctx, store.Attempt{ID: "a1", UserEmail: "alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("start published %v", got)
	}
	if err := j.TouchSession(ctx, "s1", "alice@example.com", nil, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.take(), []published{{events.Sessions, "alice@example.com"}}; !slices.Equal(got, want) {
		t.Fatalf("touch published %v, want %v", got, want)
	}
	if err := j.FinishAttempt(ctx, "a1", store.AttemptResult{}); err != nil {
		t.Fatal(err)
	}
	want := []published{{events.Usage, "alice@example.com"}, {events.Budget, "alice@example.com"}, {events.Status, ""}}
	if got := rec.take(); !slices.Equal(got, want) {
		t.Fatalf("finish published %v, want %v", got, want)
	}
	// The remembered user is forgotten once the attempt finished.
	if err := j.FinishAttempt(ctx, "a1", store.AttemptResult{}); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.take(), []published{{events.Status, ""}}; !slices.Equal(got, want) {
		t.Fatalf("second finish published %v, want %v", got, want)
	}
}

func TestPublishingJournalStaysQuietOnFailure(t *testing.T) {
	rec := &recordingPublisher{}
	j := newPublishingJournal(fakeJournal{err: errors.New("db down")}, rec)
	ctx := context.Background()
	if j.TouchSession(ctx, "s1", "a@x", nil, "", time.Now()) == nil || j.FinishAttempt(ctx, "a1", store.AttemptResult{}) == nil {
		t.Fatal("errors were swallowed")
	}
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("published %v after failures", got)
	}
}

// fakeSettler is a reconcile.Store whose settlements report changed.
type fakeSettler struct {
	due     []store.Reconciliation
	changed bool
}

func (f fakeSettler) RecoverInterrupted(context.Context, time.Time, time.Time) (int64, error) {
	return 0, nil
}

func (f fakeSettler) DueReconciliations(context.Context, time.Time, int) ([]store.Reconciliation, error) {
	return f.due, nil
}

func (f fakeSettler) RescheduleReconciliation(context.Context, string, time.Time, string) error {
	return nil
}

func (f fakeSettler) SettleAttempt(context.Context, store.Settlement) (bool, error) {
	return f.changed, nil
}

func TestPublishingReconcileStore(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		changed bool
		want    []published
	}{
		{"changed cost", true, []published{{events.Usage, "alice@example.com"}, {events.Budget, "alice@example.com"}, {events.Status, ""}}},
		{"verified cost", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingPublisher{}
			s := newPublishingReconcileStore(fakeSettler{changed: tt.changed,
				due: []store.Reconciliation{{ID: "a1", UserEmail: "alice@example.com"}}}, rec)
			if _, err := s.DueReconciliations(ctx, time.Now(), 10); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SettleAttempt(ctx, store.Settlement{AttemptID: "a1"}); err != nil {
				t.Fatal(err)
			}
			if got := rec.take(); !slices.Equal(got, tt.want) {
				t.Fatalf("published %v, want %v", got, tt.want)
			}
		})
	}
}

// flakyJournal fails FinishAttempt while failFinish is set.
type flakyJournal struct {
	fakeJournal
	failFinish bool
}

func (f *flakyJournal) FinishAttempt(context.Context, string, store.AttemptResult) error {
	if f.failFinish {
		return errors.New("db down")
	}
	return nil
}

// A failed finish must not leave the attempt's user behind: nothing else would ever remove it.
func TestPublishingJournalForgetsUserWhenFinishFails(t *testing.T) {
	ctx := context.Background()
	rec := &recordingPublisher{}
	inner := &flakyJournal{failFinish: true}
	j := newPublishingJournal(inner, rec)
	if err := j.StartAttempt(ctx, store.Attempt{ID: "a1", UserEmail: "alice@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := j.FinishAttempt(ctx, "a1", store.AttemptResult{}); err == nil {
		t.Fatal("failure was swallowed")
	}
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("published %v after a failed finish", got)
	}
	if _, kept := j.users.Load("a1"); kept {
		t.Fatal("the attempt's user was kept after the finish failed")
	}
}
