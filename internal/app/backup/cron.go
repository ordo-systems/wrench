package backup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// schedule is a parsed 5-field cron expression evaluated in UTC: minute, hour, day of month, month,
// day of week (0 or 7 is Sunday). As in cron, when both day fields are restricted either may match.
type schedule struct {
	minute, hour, dom, month, dow [61]bool
	domRestricted, dowRestricted  bool
}

var cronFields = []struct {
	name     string
	min, max int
}{{"minute", 0, 59}, {"hour", 0, 23}, {"day of month", 1, 31}, {"month", 1, 12}, {"day of week", 0, 7}}

func parseSchedule(expr string) (*schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("schedule %q must have 5 fields", expr)
	}
	s := &schedule{}
	sets := []*[61]bool{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, part := range parts {
		f := cronFields[i]
		if err := parseField(part, f.min, f.max, sets[i]); err != nil {
			return nil, fmt.Errorf("schedule %s field %q: %w", f.name, part, err)
		}
	}
	s.domRestricted, s.dowRestricted = parts[2] != "*", parts[4] != "*"
	if s.dow[7] {
		s.dow[0] = true
	}
	return s, nil
}

// parseField accepts *, n, a-b, lists of those, and /step on * or ranges.
func parseField(field string, min, max int, set *[61]bool) error {
	for _, item := range strings.Split(field, ",") {
		rng, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n <= 0 {
				return fmt.Errorf("invalid step %q", stepText)
			}
			step = n
		}
		lo, hi := min, max
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = strconv.Atoi(a); err != nil {
				return fmt.Errorf("invalid value %q", a)
			}
			hi = lo
			if isRange {
				if hi, err = strconv.Atoi(b); err != nil {
					return fmt.Errorf("invalid value %q", b)
				}
			}
		}
		if lo < min || hi > max || lo > hi {
			return fmt.Errorf("values must be within %d-%d", min, max)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	return nil
}

// dayMatches applies cron's day rule: with both day fields restricted, either may match.
func (s *schedule) dayMatches(t time.Time) bool {
	dom, dow := s.dom[t.Day()], s.dow[int(t.Weekday())]
	if s.domRestricted && s.dowRestricted {
		return dom || dow
	}
	return dom && dow
}

// next returns the first matching minute strictly after t, or the zero time if none within five years.
// It skips whole months, days and hours that cannot match.
func (s *schedule) next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	for end := t.AddDate(5, 0, 0); t.Before(end); {
		switch {
		case !s.month[int(t.Month())]:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !s.hour[t.Hour()]:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case !s.minute[t.Minute()]:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
