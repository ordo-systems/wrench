package backup

import (
	"testing"
	"time"
)

func TestScheduleNext(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		expr, from, want string
	}{
		{"0 2 * * *", "2026-11-10T01:59:00Z", "2026-11-10T02:00:00Z"},
		{"0 2 * * *", "2026-11-10T02:00:00Z", "2026-11-11T02:00:00Z"},
		{"*/15 * * * *", "2026-11-10T10:07:30Z", "2026-11-10T10:15:00Z"},
		{"30 23 31 12 *", "2026-11-10T00:00:00Z", "2026-12-31T23:30:00Z"},
		{"0 0 * * 0", "2026-11-10T00:00:00Z", "2026-11-15T00:00:00Z"}, // next Sunday
		{"0 0 * * 7", "2026-11-10T00:00:00Z", "2026-11-15T00:00:00Z"},
		{"0 3 1-7 * 1", "2026-11-10T00:00:00Z", "2026-11-16T03:00:00Z"}, // dom or dow when both are set
		{"0 9-17/4 * * 1-5", "2026-11-14T00:00:00Z", "2026-11-16T09:00:00Z"},
		{"0 0 29 2 *", "2026-11-10T00:00:00Z", "2028-02-29T00:00:00Z"},
	}
	for _, c := range cases {
		s, err := parseSchedule(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := s.next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s after %s = %s, want %s", c.expr, c.from, got.Format(time.RFC3339), c.want)
		}
	}
}

func TestScheduleRejects(t *testing.T) {
	for _, expr := range []string{"", "0 2 * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8",
		"5-1 * * * *", "*/0 * * * *", "a * * * *", "0 2 * * * *"} {
		if _, err := parseSchedule(expr); err == nil {
			t.Errorf("%q accepted", expr)
		}
	}
	// A valid expression that never matches yields no next run.
	s, err := parseSchedule("0 0 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Fatalf("impossible schedule ran at %s", got)
	}
}
