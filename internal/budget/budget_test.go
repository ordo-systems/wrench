package budget

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
)

const usd = money.Amount(1_000_000_000)

var now = time.Date(2026, 11, 10, 12, 30, 0, 0, time.UTC)

// limits sets the fixed windows; the month limit is 20 times the week, out of the way unless a test sets it.
func limits(fiveHours, day, week money.Amount) config.Limits {
	return config.Limits{FiveHours: fiveHours, Day: day, Week: week, Month: 20 * week}
}

// spend sets the fixed windows in store.Windows order; month spend is zero.
func spend(user, global [3]money.Amount) store.Spend {
	s := store.Spend{User: map[store.Window]money.Amount{}, Global: map[store.Window]money.Amount{}}
	for i, w := range store.Windows[:3] {
		s.User[w], s.Global[w] = user[i], global[i]
	}
	s.User[store.WindowMonth], s.Global[store.WindowMonth] = 0, 0
	return s
}

var (
	userLimits   = limits(usd/2, usd, 5*usd)
	globalLimits = limits(8*usd, 16*usd, 80*usd)
	warnAt       = []int{80, 95}
)

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name     string
		spend    store.Spend
		allowed  bool
		code     string
		scope    Scope
		window   store.Window
		warnings []string
	}{
		{name: "fresh", spend: spend([3]money.Amount{}, [3]money.Amount{}), allowed: true},
		{name: "below warning", spend: spend([3]money.Amount{usd * 39 / 100, usd / 2, usd}, [3]money.Amount{}), allowed: true},
		{name: "warn at 80%", spend: spend([3]money.Amount{usd * 40 / 100, usd / 2, usd}, [3]money.Amount{}), allowed: true,
			warnings: []string{"user 5_hours 80%"}},
		{name: "highest threshold only", spend: spend([3]money.Amount{usd * 48 / 100, usd * 96 / 100, usd}, [3]money.Amount{}), allowed: true,
			warnings: []string{"user 5_hours 95%", "user day 95%"}},
		{name: "global warning", spend: spend([3]money.Amount{}, [3]money.Amount{0, 0, 64 * usd}), allowed: true,
			warnings: []string{"global week 80%"}},
		{name: "user window exhausted exactly", spend: spend([3]money.Amount{usd / 2, usd / 2, usd}, [3]money.Amount{}),
			code: CodeBudgetExhausted, scope: ScopeUser, window: store.WindowFiveHours},
		{name: "overrun from earlier requests", spend: spend([3]money.Amount{0, 3 * usd, 3 * usd}, [3]money.Amount{}),
			code: CodeBudgetExhausted, scope: ScopeUser, window: store.WindowDay},
		{name: "global exhausted", spend: spend([3]money.Amount{}, [3]money.Amount{0, 0, 80 * usd}),
			code: CodeBudgetExhausted, scope: ScopeGlobal, window: store.WindowWeek},
		{name: "unknown costs do not pause", spend: unsettled(spend([3]money.Amount{}, [3]money.Amount{}), 1), allowed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := evaluate(userLimits, globalLimits, c.spend, warnAt, now)
			if d.Allowed != c.allowed || d.Code != c.code {
				t.Fatalf("decision = %+v", d)
			}
			if c.code == CodeBudgetExhausted && !strings.HasPrefix(d.Reason, string(c.scope)+" "+string(c.window)+" budget exhausted") {
				t.Fatalf("reason = %q", d.Reason)
			}
			if c.code == CodeBudgetExhausted && (d.Refused == nil || d.Refused.Scope != c.scope || d.Refused.Window != c.window) {
				t.Fatalf("refused = %+v", d.Refused)
			}
			if c.code != CodeBudgetExhausted && d.Refused != nil {
				t.Fatalf("refused = %+v without an exhausted window", d.Refused)
			}
			if !c.allowed && d.Reason == "" {
				t.Fatal("refusal without a reason")
			}
			var got []string
			for _, w := range d.Warnings {
				got = append(got, string(w.Scope)+" "+string(w.Window)+" "+itoa(w.Threshold)+"%")
			}
			if len(got) != len(c.warnings) {
				t.Fatalf("warnings = %v, want %v", got, c.warnings)
			}
			for i := range got {
				if got[i] != c.warnings[i] {
					t.Fatalf("warnings = %v, want %v", got, c.warnings)
				}
			}
		})
	}
}

func itoa(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func TestZeroLimitIsExhaustedAndReasonNamesReset(t *testing.T) {
	d := evaluate(limits(usd, 0, usd), globalLimits, spend([3]money.Amount{}, [3]money.Amount{}), warnAt, now)
	if d.Allowed || !strings.HasPrefix(d.Reason, "user day budget exhausted") || !strings.HasSuffix(d.Reason, "resets at 2026-11-11T00:00:00Z") {
		t.Fatalf("decision = %+v", d)
	}
	want := "user day budget exhausted: spent $0 of $0; resets at 2026-11-11T00:00:00Z"
	if d.Reason != want {
		t.Fatalf("reason %q, want %q", d.Reason, want)
	}
}

// unsettled marks n finished attempts of unknown cost in every user and global window.
func unsettled(s store.Spend, n int64) store.Spend {
	s.UserUnsettled, s.GlobalUnsettled = map[store.Window]int64{}, map[store.Window]int64{}
	for _, w := range store.Windows {
		s.UserUnsettled[w], s.GlobalUnsettled[w] = n, n
	}
	return s
}

// Each window reports the unsettled attempts of its own scope.
func TestReportCarriesUnsettledCounts(t *testing.T) {
	sp := spend([3]money.Amount{}, [3]money.Amount{})
	sp.UserUnsettled = map[store.Window]int64{store.WindowDay: 2}
	sp.GlobalUnsettled = map[store.Window]int64{store.WindowDay: 5, store.WindowMonth: 7}
	r := report(userLimits, globalLimits, sp, nil, now)
	got := map[string]int64{}
	for _, u := range r {
		got[string(u.Scope)+" "+string(u.Window)] = u.Unsettled
	}
	want := map[string]int64{"user day": 2, "global day": 5, "global month": 7, "user 5_hours": 0, "user month": 0}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s unsettled = %d, want %d", k, got[k], v)
		}
	}
}

// Four windows for the user, then four global ones.
func TestReportListsEveryWindow(t *testing.T) {
	r := report(userLimits, globalLimits, spend([3]money.Amount{usd / 4, usd, 0}, [3]money.Amount{}), nil, now)
	if len(r) != 8 {
		t.Fatalf("report = %+v", r)
	}
	if r[0].Scope != ScopeUser || r[0].Window != store.WindowFiveHours || r[0].Percent != 50 || !r[0].ResetsAt.Equal(time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("first = %+v", r[0])
	}
	if r[1].Percent != 100 || r[5].Scope != ScopeGlobal {
		t.Fatalf("report = %+v", r)
	}
}

type fakeStore struct {
	spend store.Spend
	err   error
	user  string
}

func (f *fakeStore) Spend(_ context.Context, email string, _ time.Time) (store.Spend, error) {
	f.user = email
	return f.spend, f.err
}

func TestCheckerUsesTierLimits(t *testing.T) {
	cfg := &config.Config{
		Users:  []config.User{{Email: "a@x", Role: config.RoleUser, Tier: "t"}},
		Tiers:  []config.Tier{{Name: "t", Default: true, Budget: config.Limits{FiveHours: userLimits.FiveHours, Week: userLimits.Week}}},
		Budget: config.Budget{GlobalLimits: globalLimits, WarnAtPercent: warnAt},
	}
	fs := &fakeStore{spend: spend([3]money.Amount{}, [3]money.Amount{})}
	c := NewChecker(config.Fixed(cfg), fs)
	c.now = func() time.Time { return now }
	d, err := c.Check(context.Background(), "a@x")
	if err != nil || d.Allowed || !strings.HasPrefix(d.Reason, "user day budget exhausted") || fs.user != "a@x" {
		t.Fatalf("decision = %+v, %v", d, err)
	}
	fs.err = errors.New("db down")
	if _, err := c.Check(context.Background(), "a@x"); err == nil {
		t.Fatal("storage failure must not admit")
	}
	if _, err := c.Status(context.Background(), "a@x"); err == nil {
		t.Fatal("storage failure hidden in status")
	}
}

func TestMonthExhausted(t *testing.T) {
	sp := spend([3]money.Amount{}, [3]money.Amount{})
	sp.User[store.WindowMonth] = 100 * usd
	d := evaluate(userLimits, globalLimits, sp, warnAt, now)
	if d.Allowed || !strings.HasPrefix(d.Reason, "user month budget exhausted") {
		t.Fatalf("decision = %+v", d)
	}
	if !strings.Contains(d.Reason, "2026-12-01T00:00:00Z") {
		t.Fatalf("reset not at the next month: %s", d.Reason)
	}
}

func TestLowestThresholdAndTinyLimits(t *testing.T) {
	d := evaluate(limits(100*usd, 100*usd, 100*usd), globalLimits, spend([3]money.Amount{usd, 0, 0}, [3]money.Amount{}), []int{1}, now)
	if len(d.Warnings) != 1 || d.Warnings[0].Threshold != 1 {
		t.Fatalf("1%% threshold: %+v", d.Warnings)
	}
	if p := percent(0, 1); p != 0 {
		t.Fatalf("percent(0, 1 nano) = %d", p)
	}
	if p := percent(1, 1); p != 100 {
		t.Fatalf("percent(1, 1 nano) = %d", p)
	}
}

func TestPercentOfHugeSpendSaturates(t *testing.T) {
	cases := []struct {
		spent, limit money.Amount
		want         int
	}{
		{math.MaxInt64, 50, math.MaxInt},
		{math.MaxInt64, 1, math.MaxInt},
		{math.MaxInt64, math.MaxInt64, 100},
		{math.MaxInt64 / 2, math.MaxInt64, 49},
	}
	for _, c := range cases {
		if got := percent(c.spent, c.limit); got != c.want {
			t.Errorf("percent(%d, %d) = %d; want %d", c.spent, c.limit, got, c.want)
		}
	}
}

func TestCheckerUsesTheRequestSnapshot(t *testing.T) {
	inForce := &config.Config{
		Users:  []config.User{{Email: "a@x", Role: config.RoleUser, Tier: "t"}},
		Tiers:  []config.Tier{{Name: "t", Default: true, Budget: userLimits}},
		Budget: config.Budget{GlobalLimits: globalLimits, WarnAtPercent: warnAt},
	}
	snapshot := &config.Config{
		Users:  inForce.Users,
		Tiers:  []config.Tier{{Name: "t", Default: true, Budget: limits(0, usd, 5*usd)}},
		Budget: inForce.Budget,
	}
	fs := &fakeStore{spend: spend([3]money.Amount{}, [3]money.Amount{})}
	c := NewChecker(config.Fixed(inForce), fs)
	c.now = func() time.Time { return now }
	d, err := c.Check(config.WithSnapshot(context.Background(), snapshot), "a@x")
	if err != nil || d.Allowed {
		t.Fatalf("decision used the configuration in force instead of the request snapshot: %+v, %v", d, err)
	}
}

// Status feeds the alert runner, which raises warn_at_percent alerts from each window's Threshold.
func TestStatusMarksReachedThresholds(t *testing.T) {
	cfg := &config.Config{
		Users:  []config.User{{Email: "a@x", Role: config.RoleUser, Tier: "t"}},
		Tiers:  []config.Tier{{Name: "t", Default: true, Budget: userLimits}},
		Budget: config.Budget{GlobalLimits: globalLimits, WarnAtPercent: warnAt},
	}
	fs := &fakeStore{spend: spend([3]money.Amount{usd * 43 / 100, 0, 0}, [3]money.Amount{})}
	c := NewChecker(config.Fixed(cfg), fs)
	c.now = func() time.Time { return now }
	usage, err := c.Status(context.Background(), "a@x")
	if err != nil || usage[0].Window != store.WindowFiveHours || usage[0].Threshold != 80 || usage[1].Threshold != 0 {
		t.Fatalf("status = %+v, %v", usage, err)
	}
}
