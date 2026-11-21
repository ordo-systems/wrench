package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store/pgtest"
)

// openTestStore opens a migrated store on a disposable database.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, pgtest.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func ptr[T any](v T) *T { return &v }

var t0 = time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailsOnBadDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Open(ctx, "postgres://nobody@127.0.0.1:1/none?connect_timeout=1"); err == nil {
		t.Fatal("want error")
	}
}

func TestTouchSession(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.TouchSession(ctx, "s1", "a@x", nil, "", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, "s1", "a@x", nil, "", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// An out-of-order touch never moves LastSeenAt backwards.
	if err := s.TouchSession(ctx, "s1", "a@x", nil, "", t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, "s1", "b@x", nil, "", t0); !errors.Is(err, ErrSessionOwner) {
		t.Fatalf("err = %v", err)
	}
	if err := s.TouchSession(ctx, "s2", "b@x", nil, "", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListSessions(ctx, "", "", time.Time{}, 10)
	if err != nil || len(all) != 2 || all[0].ID != "s2" {
		t.Fatalf("all = %+v, %v", all, err)
	}
	mine, err := s.ListSessions(ctx, "a@x", "", time.Time{}, 10)
	if err != nil || len(mine) != 1 {
		t.Fatalf("mine = %+v, %v", mine, err)
	}
	if !mine[0].StartedAt.Equal(t0) || !mine[0].LastSeenAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("session = %+v", mine[0])
	}
}

func startAttempt(t *testing.T, s *Store, id, user, session string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := s.TouchSession(ctx, session, user, nil, "", at); err != nil {
		t.Fatal(err)
	}
	err := s.StartAttempt(ctx, Attempt{ID: id, RequestID: "r-" + id, UserEmail: user, SessionID: session, RequestedModel: "glm", StartedAt: at})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAttemptLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "a1", "a@x", "s1", t0)

	got, err := s.ListAttempts(ctx, AttemptFilter{}, 10)
	if err != nil || len(got) != 1 || got[0].CostState != CostPending || got[0].Outcome != "" || got[0].Cost != nil || got[0].FinishedAt != nil {
		t.Fatalf("pending = %+v, %v", got, err)
	}

	res := AttemptResult{
		UpstreamModel: "z-ai/glm-5.3", Provider: "openrouter", InferenceProvider: "Z.AI", GenerationID: "gen-1",
		FirstTokenAt: ptr(t0.Add(time.Second)), FinishedAt: t0.Add(3 * time.Second), Outcome: OutcomeSuccess,
		Tokens: Tokens{Input: ptr[int64](100), Output: ptr[int64](20), CacheRead: ptr[int64](0)},
		Cost:   ptr(money.Amount(1_234)),
	}
	if err := s.FinishAttempt(ctx, "a1", res); err != nil {
		t.Fatal(err)
	}
	// A repeated delivery does not overwrite the settled attempt.
	again := res
	again.Cost = ptr(money.Amount(9_999))
	if err := s.FinishAttempt(ctx, "a1", again); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAttempt(ctx, "missing", res); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}

	got, _ = s.ListAttempts(ctx, AttemptFilter{}, 10)
	a := got[0]
	if a.CostState != CostConfirmed || *a.Cost != 1_234 || a.GenerationID != "gen-1" || a.InferenceProvider != "Z.AI" || a.Outcome != OutcomeSuccess {
		t.Fatalf("finished = %+v", a)
	}
	if *a.Tokens.Input != 100 || *a.Tokens.CacheRead != 0 || a.Tokens.Reasoning != nil || !a.FirstTokenAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("tokens = %+v", a.Tokens)
	}
}

func TestFinishWithoutCostStaysPending(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "a1", "a@x", "s1", t0)
	if err := s.FinishAttempt(ctx, "a1", AttemptResult{FinishedAt: t0.Add(time.Second), Outcome: OutcomeCancelled, ErrorReason: "client disconnected"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListAttempts(ctx, AttemptFilter{}, 10)
	if got[0].CostState != CostPending || got[0].Cost != nil || got[0].Outcome != OutcomeCancelled || got[0].ErrorReason != "client disconnected" {
		t.Fatalf("got %+v", got[0])
	}
}

func TestDuplicateGenerationIsRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "a1", "a@x", "s1", t0)
	startAttempt(t, s, "a2", "a@x", "s1", t0)
	r := AttemptResult{GenerationID: "gen-1", FinishedAt: t0, Outcome: OutcomeSuccess, Cost: ptr(money.Amount(1))}
	if err := s.FinishAttempt(ctx, "a1", r); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishAttempt(ctx, "a2", r); err == nil {
		t.Fatal("same generation charged twice")
	}
}

func TestSumUsage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	finish := func(id string, cost *money.Amount, in *int64) {
		if err := s.FinishAttempt(ctx, id, AttemptResult{FinishedAt: t0, Outcome: OutcomeSuccess, Cost: cost, Tokens: Tokens{Input: in}}); err != nil {
			t.Fatal(err)
		}
	}
	startAttempt(t, s, "a1", "a@x", "s1", t0)
	startAttempt(t, s, "a2", "a@x", "s2", t0.Add(time.Hour))
	startAttempt(t, s, "b1", "b@x", "s3", t0.Add(2*time.Hour))
	startAttempt(t, s, "old", "a@x", "s1", t0.Add(-24*time.Hour))
	finish("a1", ptr(money.Amount(100)), ptr[int64](10))
	finish("a2", nil, ptr[int64](5))
	finish("b1", ptr(money.Amount(50)), nil)
	finish("old", ptr(money.Amount(1000)), ptr[int64](1))

	day := AttemptFilter{From: t0.Truncate(24 * time.Hour), To: t0.Truncate(24 * time.Hour).Add(24 * time.Hour)}
	tot, err := s.SumUsage(ctx, day)
	if err != nil || tot.Cost != 150 || tot.Attempts != 3 || tot.Pending != 1 || *tot.Tokens.Input != 15 || tot.Tokens.Output != nil {
		t.Fatalf("day totals = %+v, %v", tot, err)
	}

	byUser, err := s.SumUsageByUser(ctx, day)
	if err != nil || len(byUser) != 2 || byUser[0].Key != "a@x" || byUser[0].Cost != 100 || byUser[0].Pending != 1 || byUser[1].Tokens.Input != nil {
		t.Fatalf("by user = %+v, %v", byUser, err)
	}

	bySession, err := s.SumUsageBySession(ctx, AttemptFilter{UserEmail: "a@x"})
	if err != nil || len(bySession) != 2 || bySession[0].Key != "s1" || bySession[0].Cost != 1100 || bySession[0].Attempts != 2 {
		t.Fatalf("by session = %+v, %v", bySession, err)
	}
	only, err := s.SumUsageBySession(ctx, AttemptFilter{UserEmail: "a@x", SessionIDs: []string{"s2", "s3"}})
	if err != nil || len(only) != 1 || only[0].Key != "s2" || only[0].Pending != 1 {
		t.Fatalf("selected sessions = %+v, %v", only, err)
	}

	byModel, err := s.SumUsageByModel(ctx, AttemptFilter{UserEmail: "a@x"})
	if err != nil || len(byModel) != 1 || byModel[0].Key != "glm" || byModel[0].Attempts != 3 {
		t.Fatalf("by model = %+v, %v", byModel, err)
	}

	empty, err := s.SumUsage(ctx, AttemptFilter{UserEmail: "nobody@x"})
	if err != nil || empty.Cost != 0 || empty.Attempts != 0 || empty.Tokens.Input != nil {
		t.Fatalf("empty = %+v, %v", empty, err)
	}

	list, err := s.ListAttempts(ctx, AttemptFilter{SessionIDs: []string{"s1"}}, 10)
	if err != nil || len(list) != 2 || list[0].ID != "a1" {
		t.Fatalf("list = %+v, %v", list, err)
	}
}
