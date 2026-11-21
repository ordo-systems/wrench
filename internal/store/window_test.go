package store

import (
	"testing"
	"time"
)

func TestWindowBounds(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		w          Window
		at         string
		start, end string
	}{
		// README example: five-hour windows continue across midnight.
		{WindowFiveHours, "2026-11-03T00:00:00Z", "2026-11-02T20:00:00Z", "2026-11-03T01:00:00Z"},
		{WindowFiveHours, "2026-11-02T00:00:00Z", "2026-11-02T00:00:00Z", "2026-11-02T05:00:00Z"},
		{WindowFiveHours, "2026-11-02T04:59:59Z", "2026-11-02T00:00:00Z", "2026-11-02T05:00:00Z"},
		{WindowFiveHours, "2026-11-01T23:00:00Z", "2026-11-01T19:00:00Z", "2026-11-02T00:00:00Z"},
		{WindowDay, "2026-11-09T13:45:00Z", "2026-11-09T00:00:00Z", "2026-11-10T00:00:00Z"},
		{WindowDay, "2026-11-09T15:45:00+03:00", "2026-11-09T00:00:00Z", "2026-11-10T00:00:00Z"},
		{WindowWeek, "2026-11-09T00:00:00Z", "2026-11-09T00:00:00Z", "2026-11-16T00:00:00Z"},
		{WindowWeek, "2026-11-15T23:59:59Z", "2026-11-09T00:00:00Z", "2026-11-16T00:00:00Z"},
		{WindowWeek, "2026-10-28T12:00:00Z", "2026-10-26T00:00:00Z", "2026-11-02T00:00:00Z"},
		// Calendar months in UTC.
		{WindowMonth, "2026-11-15T12:00:00Z", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
		{WindowMonth, "2026-11-01T00:00:00Z", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
		{WindowMonth, "2026-11-30T23:59:59Z", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
		{WindowMonth, "2026-02-10T00:00:00Z", "2026-02-01T00:00:00Z", "2026-03-01T00:00:00Z"},
		{WindowMonth, "2026-12-31T23:00:00Z", "2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{WindowMonth, "2026-12-01T01:00:00+03:00", "2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z"},
	}
	for _, c := range cases {
		start, end := c.w.Bounds(at(c.at))
		if !start.Equal(at(c.start)) || !end.Equal(at(c.end)) || start.Location() != time.UTC {
			t.Errorf("%s at %s = [%s, %s); want [%s, %s)", c.w, c.at, start, end, c.start, c.end)
		}
	}
}

func TestParseWindow(t *testing.T) {
	for _, w := range []Window{WindowFiveHours, WindowDay, WindowWeek, WindowMonth} {
		if got, err := ParseWindow(string(w)); err != nil || got != w {
			t.Errorf("ParseWindow(%q) = %q, %v", w, got, err)
		}
	}
	if _, err := ParseWindow("year"); err == nil {
		t.Error("year accepted")
	}
}
