package store

import (
	"context"
	"testing"
	"time"
)

func TestTouchSessionStoresAgent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	steps := []struct{ touch, want string }{
		{"opencode/2.3.4", "opencode/2.3.4"},
		{"", "opencode/2.3.4"},
		{"opencode/2.4.0", "opencode/2.4.0"},
	}
	for i, st := range steps {
		if err := s.TouchSession(ctx, "s1", "a@x", nil, st.touch, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListSessions(ctx, "a@x", "", time.Time{}, 10)
		if err != nil || len(list) != 1 || list[0].Agent != st.want {
			t.Fatalf("step %d: sessions = %+v, %v; want agent %q", i, list, err, st.want)
		}
	}
}

func TestListSessionsFiltersByStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := t0.Add(time.Hour)
	if err := s.TouchSession(ctx, "fresh", "a@x", nil, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, "stale", "a@x", nil, "", now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-5 * time.Minute)
	tests := []struct {
		status string
		want   []string
	}{
		{"", []string{"fresh", "stale"}},
		{"active", []string{"fresh"}},
		{"offline", []string{"stale"}},
	}
	for _, tt := range tests {
		list, err := s.ListSessions(ctx, "", tt.status, cutoff, 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, v := range list {
			got = append(got, v.ID)
		}
		if len(got) != len(tt.want) {
			t.Fatalf("status %q: %v, want %v", tt.status, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("status %q: %v, want %v", tt.status, got, tt.want)
			}
		}
	}
}
