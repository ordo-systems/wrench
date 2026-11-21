package store

import (
	"context"
	"testing"
	"time"
)

func dueKeys(t *testing.T, s *Store, at time.Time) []string {
	t.Helper()
	due, err := s.DueIncidents(context.Background(), at, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, i := range due {
		state := "open"
		if i.ResolvedAt != nil {
			state = "resolved"
		}
		keys = append(keys, i.Key+":"+state)
		if err := s.MarkNotified(context.Background(), i.ID, at); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestIncidentsNotifyOncePerPeriodAndOnResolution(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	budget := Condition{Key: "budget:user:a@x:day", Severity: SeverityWarning, UserEmail: "a@x", Message: "80%"}
	errorsHigh := Condition{Key: "errors", Severity: SeverityCritical, Message: "30% errors"}

	sync := func(at time.Time, cs ...Condition) {
		t.Helper()
		if _, err := s.SyncIncidents(ctx, at, cs); err != nil {
			t.Fatal(err)
		}
	}
	sync(t0, budget, errorsHigh)
	if got := dueKeys(t, s, t0); len(got) != 2 {
		t.Fatalf("opening = %v", got)
	}
	// The same continuing incidents stay quiet until the repeat period passed.
	budget.Message = "95%"
	sync(t0.Add(time.Minute), budget, errorsHigh)
	if got := dueKeys(t, s, t0.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("repeat too early = %v", got)
	}
	sync(t0.Add(61*time.Minute), budget)
	got := dueKeys(t, s, t0.Add(61*time.Minute))
	if len(got) != 2 || got[0] != "budget:user:a@x:day:open" || got[1] != "errors:resolved" {
		t.Fatalf("after an hour = %v", got)
	}
	if got := dueKeys(t, s, t0.Add(62*time.Minute)); len(got) != 0 {
		t.Fatalf("resolution announced twice = %v", got)
	}
	// A condition that returns after resolution is a new incident.
	sync(t0.Add(63*time.Minute), budget, errorsHigh)
	if got := dueKeys(t, s, t0.Add(63*time.Minute)); len(got) != 1 || got[0] != "errors:open" {
		t.Fatalf("reopened = %v", got)
	}

	list, err := s.ListIncidents(ctx, "", 10)
	if err != nil || len(list) != 3 || list[0].ResolvedAt != nil {
		t.Fatalf("list = %+v, %v", list, err)
	}
	// Users see their own incidents and system-wide ones, not other users'.
	if _, err := s.SyncIncidents(ctx, t0.Add(64*time.Minute), []Condition{budget, errorsHigh, {Key: "budget:user:b@x:day", UserEmail: "b@x", Severity: SeverityWarning, Message: "b"}}); err != nil {
		t.Fatal(err)
	}
	mine, _ := s.ListIncidents(ctx, "a@x", 10)
	if len(mine) != 3 || mine[0].UserEmail == "b@x" || mine[1].UserEmail == "b@x" || mine[2].UserEmail == "b@x" {
		t.Fatalf("mine = %+v", mine)
	}
}

func TestUnannouncedIncidentsResolveSilently(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.SyncIncidents(ctx, t0, []Condition{{Key: "flap", Severity: SeverityWarning, Message: "x"}}); err != nil {
		t.Fatal(err)
	}
	// Delivery failed, then the condition cleared: nothing was announced, so nothing is resolved aloud.
	if _, err := s.SyncIncidents(ctx, t0.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if got := dueKeys(t, s, t0.Add(time.Minute)); len(got) != 0 {
		t.Fatalf("due = %v", got)
	}
}

func TestSyncIncidentsReportsChange(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	c := Condition{Key: "k", Severity: SeverityWarning, Message: "m"}
	tests := []struct {
		name   string
		active []Condition
		want   bool
	}{
		{"opens an incident", []Condition{c}, true},
		{"same condition continues", []Condition{c}, false},
		{"resolves the incident", nil, true},
		{"nothing to resolve", nil, false},
	}
	for i, tt := range tests {
		got, err := s.SyncIncidents(ctx, t0.Add(time.Duration(i)*time.Minute), tt.active)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("%s: changed = %v, want %v", tt.name, got, tt.want)
		}
	}
}
