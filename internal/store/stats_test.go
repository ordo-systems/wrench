package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/money"
)

// statsFixture is a control journal over two hours:
//
//	a1  alice s1 glm       t0      success  input 100, cache read 40, output 10, cost 1000, ttft 1s, 3s
//	a2  alice s1 glm-flash t0+10m  error    nothing reported, cost unknown
//	a3  bob   s2 glm       t0+70m  success  input 50 (no cache field), output 5, cost 3000, ttft 2s, 4s
//	a4  bob   s2 glm       t0+80m  still running
func statsFixture(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	ctx := context.Background()
	startAttempt(t, s, "a1", "alice@x", "s1", t0)
	startAttempt(t, s, "a3", "bob@x", "s2", t0.Add(70*time.Minute))
	startAttempt(t, s, "a4", "bob@x", "s2", t0.Add(80*time.Minute))
	if err := s.StartAttempt(ctx, Attempt{ID: "a2", RequestID: "r-a2", UserEmail: "alice@x", SessionID: "s1",
		RequestedModel: "glm-flash", StartedAt: t0.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	results := map[string]AttemptResult{
		"a1": {FirstTokenAt: ptr(t0.Add(time.Second)), FinishedAt: t0.Add(3 * time.Second), Outcome: OutcomeSuccess,
			InferenceProvider: "Z.AI", Tokens: Tokens{Input: ptr[int64](100), CacheRead: ptr[int64](40), Output: ptr[int64](10)},
			Cost: ptr(money.Amount(1000))},
		"a2": {FinishedAt: t0.Add(11 * time.Minute), Outcome: OutcomeError, ErrorReason: "upstream 500"},
		"a3": {FirstTokenAt: ptr(t0.Add(70*time.Minute + 2*time.Second)), FinishedAt: t0.Add(70*time.Minute + 4*time.Second),
			Outcome: OutcomeSuccess, InferenceProvider: "DeepInfra",
			Tokens: Tokens{Input: ptr[int64](50), Output: ptr[int64](5)}, Cost: ptr(money.Amount(3000))},
	}
	for id, r := range results {
		if err := s.FinishAttempt(ctx, id, r); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestStatsMatchTheControlJournal(t *testing.T) {
	s := statsFixture(t)
	st, err := s.Stats(context.Background(), AttemptFilter{From: t0, To: t0.Add(2 * time.Hour)}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tot := st.Total
	if tot.Attempts != 4 || tot.Requests != 4 || tot.Success != 2 || tot.Errors != 1 || tot.Running != 1 ||
		tot.Pending != 1 || tot.Confirmed != 2 || tot.Cost != 4000 {
		t.Fatalf("total = %+v", tot)
	}
	// Missing telemetry stays visible: only two attempts reported input, one reported cache reads.
	if *tot.Input.Sum != 150 || tot.Input.Reported != 2 || *tot.CacheRead.Sum != 40 || tot.CacheRead.Reported != 1 ||
		tot.Reasoning.Sum != nil || tot.Reasoning.Reported != 0 {
		t.Fatalf("tokens = %+v %+v %+v", tot.Input, tot.CacheRead, tot.Reasoning)
	}
	// The cache share uses only attempts that reported both fields: 40 of a1's 100, not 40 of 150.
	if tot.CachedInput != 40 || tot.CacheableInput != 100 {
		t.Fatalf("cache share = %d/%d", tot.CachedInput, tot.CacheableInput)
	}
	if tot.FirstToken.Observed != 2 || *tot.FirstToken.P50 != 1500 || tot.Duration.Observed != 3 {
		t.Fatalf("latency = %+v %+v", tot.FirstToken, tot.Duration)
	}

	if len(st.Buckets) != 2 {
		t.Fatalf("buckets = %d", len(st.Buckets))
	}
	b0, b1 := st.Buckets[0], st.Buckets[1]
	if !b0.Start.Equal(t0) || b0.Attempts != 2 || b0.Cost != 1000 || b1.Attempts != 2 || b1.Cost != 3000 {
		t.Fatalf("buckets = %+v / %+v", b0, b1)
	}
	if *b0.ActiveSessions != 1 || *b1.ActiveSessions != 1 {
		t.Fatalf("active sessions = %d %d", *b0.ActiveSessions, *b1.ActiveSessions)
	}
	if sum := b0.Cost + b1.Cost; sum != tot.Cost {
		t.Fatalf("buckets sum to %d, total %d", sum, tot.Cost)
	}

	if len(st.ByUser) != 2 || st.ByUser[0].Key != "bob@x" || st.ByUser[0].Cost != 3000 || st.ByUser[1].Pending != 1 {
		t.Fatalf("by user = %+v", st.ByUser)
	}
	if len(st.ByProvider) != 3 || st.ByProvider[0].Key != "DeepInfra" {
		t.Fatalf("by provider = %+v", st.ByProvider)
	}
	// Session s1 is incomplete because of its attempt without a cost.
	sc := st.Sessions
	if sc.Sessions != 2 || sc.Incomplete != 1 || *sc.Average != 2000 || *sc.P50 != 1000 || *sc.P95 != 3000 {
		t.Fatalf("sessions = %+v", sc)
	}
	if st.OldestPending == nil || !st.OldestPending.Equal(t0.Add(11*time.Minute)) {
		t.Fatalf("oldest pending = %v", st.OldestPending)
	}
}

func TestStatsFilters(t *testing.T) {
	s := statsFixture(t)
	ctx := context.Background()
	period := AttemptFilter{From: t0, To: t0.Add(2 * time.Hour)}
	cases := []struct {
		name     string
		edit     func(*AttemptFilter)
		attempts int64
	}{
		{"user", func(f *AttemptFilter) { f.UserEmail = "bob@x" }, 2},
		{"model", func(f *AttemptFilter) { f.Model = "glm-flash" }, 1},
		{"provider", func(f *AttemptFilter) { f.Provider = "Z.AI" }, 1},
		{"session", func(f *AttemptFilter) { f.SessionIDs = []string{"s1"} }, 2},
	}
	for _, c := range cases {
		f := period
		c.edit(&f)
		st, err := s.Stats(ctx, f, time.Hour)
		if err != nil || st.Total.Attempts != c.attempts {
			t.Fatalf("%s: %d attempts, %v", c.name, st.Total.Attempts, err)
		}
	}
	empty, err := s.Stats(ctx, AttemptFilter{From: t0.Add(-time.Hour), To: t0}, 30*time.Minute)
	if err != nil || len(empty.Buckets) != 2 || empty.Total.Attempts != 0 || empty.Sessions.Average != nil || empty.OldestPending != nil {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	if _, err := s.Stats(ctx, AttemptFilter{}, time.Hour); err == nil {
		t.Fatal("stats without a period")
	}
}

// Acceptance scenario 16: an additional attempt for the same logical request counts as a retry, and both
// paid attempts add to the request's cost without double counting.
func TestStatsCountExtraAttemptsOfOneRequest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.TouchSession(ctx, "s1", "a@x", nil, "", t0); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"r1-first", "r1-second", "r2"} {
		request := "r1"
		if id == "r2" {
			request = "r2"
		}
		at := t0.Add(time.Duration(i) * time.Minute)
		if err := s.StartAttempt(ctx, Attempt{ID: id, RequestID: request, UserEmail: "a@x", SessionID: "s1", RequestedModel: "glm", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishAttempt(ctx, id, AttemptResult{FinishedAt: at.Add(time.Second), Outcome: OutcomeSuccess, Cost: ptr(money.Amount(100 * (i + 1)))}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.Stats(ctx, AttemptFilter{From: t0, To: t0.Add(time.Hour)}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if tot := st.Total; tot.Attempts != 3 || tot.Requests != 2 || tot.Cost != 600 || st.Sessions.Sessions != 1 || *st.Sessions.Average != 600 {
		t.Fatalf("total = %+v, sessions %+v", tot, st.Sessions)
	}
}

// finishedAttempt journals one successful attempt of user at t0 with the given confirmed cost.
func finishedAttempt(t *testing.T, s *Store, id, user string, cost money.Amount) {
	t.Helper()
	startAttempt(t, s, id, user, "s-"+id, t0)
	err := s.FinishAttempt(context.Background(), id, AttemptResult{FinishedAt: t0.Add(time.Second), Outcome: OutcomeSuccess,
		Tokens: Tokens{Input: ptr[int64](10)}, Cost: ptr(cost)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStatsByUser(t *testing.T) {
	tests := []struct {
		name      string
		users     int
		wantUsers []string
		wantOther money.Amount // zero means no "" series
	}{
		{name: "none"},
		{name: "three users", users: 3, wantUsers: []string{"u3@x", "u2@x", "u1@x"}},
		{name: "ten users keep the top eight and sum the rest", users: 10,
			wantUsers: []string{"u10@x", "u9@x", "u8@x", "u7@x", "u6@x", "u5@x", "u4@x", "u3@x", ""}, wantOther: 300},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			for i := 1; i <= tc.users; i++ {
				finishedAttempt(t, s, fmt.Sprintf("a%d", i), fmt.Sprintf("u%d@x", i), money.Amount(i*100))
			}
			f := AttemptFilter{From: t0, To: t0.Add(2 * time.Hour)}
			got, err := s.StatsByUser(context.Background(), f, time.Hour, 8)
			if err != nil {
				t.Fatal(err)
			}
			var users []string
			for _, series := range got {
				users = append(users, series.User)
				if len(series.Buckets) != 2 {
					t.Fatalf("%q has %d buckets, want a continuous axis of 2", series.User, len(series.Buckets))
				}
			}
			if !slices.Equal(users, tc.wantUsers) {
				t.Fatalf("users = %v, want %v", users, tc.wantUsers)
			}
			for _, series := range got {
				if series.User == "" {
					continue
				}
				if a0, a1 := series.Buckets[0].ActiveSessions, series.Buckets[1].ActiveSessions; a0 == nil || *a0 != 1 || a1 == nil || *a1 != 0 {
					t.Fatalf("%q active sessions = %v %v, want 1 0", series.User, a0, a1)
				}
			}
			if tc.wantOther == 0 {
				return
			}
			other := got[len(got)-1].Buckets[0]
			if other.ActiveSessions == nil || *other.ActiveSessions != 2 {
				t.Fatalf("others active sessions = %v, want 2", other.ActiveSessions)
			}
			if other.Cost != tc.wantOther || other.Attempts != 2 || other.FirstToken.P50 != nil || other.Duration.P95 != nil ||
				other.CacheRead.Sum != nil || other.CacheableInput != 0 {
				t.Fatalf("others = %+v", other)
			}
		})
	}
}
