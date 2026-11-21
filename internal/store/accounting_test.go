package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/money"
)

func events(t *testing.T, s *Store, id string) []ChargeEvent {
	t.Helper()
	ev, err := s.ChargeEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func attempt(t *testing.T, s *Store, id string) Attempt {
	t.Helper()
	all, err := s.ListAttempts(context.Background(), AttemptFilter{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("attempt %s not found", id)
	return Attempt{}
}

func TestStreamCostIsJournaledAndScheduledForVerification(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "a1", "a@x", "s1", t0)
	if err := s.RecordGeneration(ctx, "a1", "gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAttempt(ctx, "a1", AttemptResult{FinishedAt: t0.Add(time.Second), Outcome: OutcomeSuccess, Cost: ptr(money.Amount(100))}); err != nil {
		t.Fatal(err)
	}
	if ev := events(t, s, "a1"); len(ev) != 1 || ev[0].Source != ChargeStream || ev[0].Cost != 100 || ev[0].Previous != nil {
		t.Fatalf("events = %+v", ev)
	}
	if a := attempt(t, s, "a1"); a.GenerationID != "gen-1" {
		t.Fatalf("early generation id lost: %+v", a)
	}
	due, err := s.DueReconciliations(ctx, t0.Add(time.Second+ReconcileDelay(0)), 10)
	if err != nil || len(due) != 1 || due[0].ID != "a1" || due[0].GenerationID != "gen-1" {
		t.Fatalf("due = %+v, %v", due, err)
	}
	if due, _ := s.DueReconciliations(ctx, t0.Add(time.Second), 10); len(due) != 0 {
		t.Fatalf("due too early: %+v", due)
	}
}

func TestSettleIsIdempotentAndCorrectionsAreAudited(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "a1", "a@x", "s1", t0)
	_ = s.FinishAttempt(ctx, "a1", AttemptResult{GenerationID: "gen-1", FinishedAt: t0, Outcome: OutcomeSuccess})
	if a := attempt(t, s, "a1"); a.CostState != CostPending {
		t.Fatalf("state %s", a.CostState)
	}

	settle := func(cost money.Amount, src ChargeSource) bool {
		t.Helper()
		changed, err := s.SettleAttempt(ctx, Settlement{AttemptID: "a1", Cost: cost, Source: src, Provider: "Z.AI", Actor: "reconciler", At: t0.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if !settle(250, ChargeGeneration) {
		t.Fatal("first settlement reported no change")
	}
	// Delivering the same result again changes nothing.
	if settle(250, ChargeGeneration) {
		t.Fatal("repeated settlement changed the charge")
	}
	a := attempt(t, s, "a1")
	if a.CostState != CostConfirmed || *a.Cost != 250 || a.InferenceProvider != "Z.AI" {
		t.Fatalf("settled = %+v", a)
	}
	if !settle(300, ChargeGeneration) {
		t.Fatal("correction reported no change")
	}
	ev := events(t, s, "a1")
	if len(ev) != 2 || ev[1].Source != ChargeGeneration || ev[1].Cost != 300 || *ev[1].Previous != 250 || ev[1].Actor != "reconciler" {
		t.Fatalf("events = %+v", ev)
	}
	if due, _ := s.DueReconciliations(ctx, t0.Add(time.Hour), 10); len(due) != 0 {
		t.Fatalf("reconciled attempt still due: %+v", due)
	}
	if _, err := s.SettleAttempt(ctx, Settlement{AttemptID: "nope", Source: ChargeGeneration, At: t0}); err == nil {
		t.Fatal("settled a missing attempt")
	}
}

func TestNotSentIsConfirmedFree(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "a1", "a@x", "s1", t0)
	_ = s.FinishAttempt(ctx, "a1", AttemptResult{FinishedAt: t0, Outcome: OutcomeError, ErrorReason: "upstream unreachable", NotSent: true})
	a := attempt(t, s, "a1")
	if a.CostState != CostConfirmed || a.Cost == nil || *a.Cost != 0 {
		t.Fatalf("not-sent attempt = %+v", a)
	}
	if ev := events(t, s, "a1"); len(ev) != 1 || ev[0].Source != ChargeNotSent {
		t.Fatalf("events = %+v", ev)
	}
}

func TestReschedule(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "a1", "a@x", "s1", t0)
	_ = s.FinishAttempt(ctx, "a1", AttemptResult{GenerationID: "gen-1", FinishedAt: t0, Outcome: OutcomeSuccess})
	next := t0.Add(time.Hour)
	if err := s.RescheduleReconciliation(ctx, "a1", next, "generation not found"); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueReconciliations(ctx, next.Add(-time.Second), 10); len(due) != 0 {
		t.Fatalf("due before reschedule: %+v", due)
	}
	due, _ := s.DueReconciliations(ctx, next, 10)
	if len(due) != 1 || due[0].Tries != 1 || !due[0].FinishedAt.Equal(t0) {
		t.Fatalf("due = %+v", due)
	}
}

func TestRecoverInterrupted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "lost", "a@x", "s1", t0)
	startAttempt(t, s, "lost-gen", "a@x", "s1", t0)
	startAttempt(t, s, "running", "a@x", "s1", t0.Add(time.Hour))
	_ = s.RecordGeneration(ctx, "lost-gen", "gen-9")

	n, err := s.RecoverInterrupted(ctx, t0.Add(30*time.Minute), t0.Add(2*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("recovered %d, %v", n, err)
	}
	if a := attempt(t, s, "lost"); a.Outcome != OutcomeError || a.CostState != CostPending || a.FinishedAt == nil {
		t.Fatalf("lost = %+v", a)
	}
	if a := attempt(t, s, "running"); a.FinishedAt != nil {
		t.Fatal("a running attempt was closed")
	}
	due, _ := s.DueReconciliations(ctx, t0.Add(2*time.Hour), 10)
	if len(due) != 1 || due[0].ID != "lost-gen" {
		t.Fatalf("due = %+v", due)
	}
}

func TestSpendInWindows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) // day 2026-11-10, 5h window 08:00-13:00 (anchored at 2026-11-02), week from 11-09
	finish := func(id, user string, at time.Time, cost *money.Amount) {
		startAttempt(t, s, id, user, "s-"+user, at)
		_ = s.FinishAttempt(ctx, id, AttemptResult{FinishedAt: at, Outcome: OutcomeSuccess, Cost: cost})
	}
	finish("in5h", "a@x", now.Add(-time.Hour), ptr(money.Amount(1)))
	finish("today", "a@x", now.Add(-5*time.Hour), ptr(money.Amount(10)))
	finish("week", "a@x", now.Add(-24*time.Hour), ptr(money.Amount(100)))
	finish("lastweek", "a@x", now.Add(-8*24*time.Hour), ptr(money.Amount(1000)))
	finish("other", "b@x", now.Add(-time.Minute), ptr(money.Amount(5)))
	finish("pending", "a@x", now.Add(-time.Minute), nil)
	finish("pendingDay", "a@x", now.Add(-5*time.Hour), nil)
	finish("pendingOther", "b@x", now.Add(-time.Minute), nil)
	startAttempt(t, s, "running", "a@x", "s-a@x", now)

	sp, err := s.Spend(ctx, "a@x", now)
	if err != nil {
		t.Fatal(err)
	}
	want := Spend{
		User:   map[Window]money.Amount{WindowFiveHours: 1, WindowDay: 11, WindowWeek: 111},
		Global: map[Window]money.Amount{WindowFiveHours: 6, WindowDay: 16, WindowWeek: 116},
	}
	for _, w := range []Window{WindowFiveHours, WindowDay, WindowWeek} {
		if sp.User[w] != want.User[w] || sp.Global[w] != want.Global[w] {
			t.Errorf("%s: user %d global %d, want %d %d", w, sp.User[w], sp.Global[w], want.User[w], want.Global[w])
		}
	}
	// Finished attempts of unknown cost are counted where they started; the running one is not.
	wantUser := map[Window]int64{WindowFiveHours: 1, WindowDay: 2, WindowWeek: 2}
	wantGlobal := map[Window]int64{WindowFiveHours: 2, WindowDay: 3, WindowWeek: 3}
	for _, w := range []Window{WindowFiveHours, WindowDay, WindowWeek} {
		if sp.UserUnsettled[w] != wantUser[w] || sp.GlobalUnsettled[w] != wantGlobal[w] {
			t.Errorf("%s unsettled: user %d global %d, want %d %d", w, sp.UserUnsettled[w], sp.GlobalUnsettled[w], wantUser[w], wantGlobal[w])
		}
	}
}

func TestSettleRunningAttemptIsRefused(t *testing.T) {
	s := openTestStore(t)
	startAttempt(t, s, "a1", "a@x", "s1", t0)
	_, err := s.SettleAttempt(context.Background(), Settlement{AttemptID: "a1", Cost: 1, Source: ChargeGeneration, At: t0})
	if !errors.Is(err, ErrAttemptRunning) {
		t.Fatalf("err = %v; want ErrAttemptRunning", err)
	}
}
