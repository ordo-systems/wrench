// Package budget decides whether a new inference request may start, from recorded spend in the
// fixed UTC windows. Nothing is reserved: admitted requests may overrun a limit, the full cost is
// charged, and later requests are refused until the window resets.
package budget

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
)

// Refusal codes, also sent to clients.
const (
	CodeBudgetExhausted = "budget_exhausted"
)

// Scope separates a user's own limits from the limits shared by everyone.
type Scope string

const (
	ScopeUser   Scope = "user"
	ScopeGlobal Scope = "global"
)

// Usage is spend against one limit in the current window.
type Usage struct {
	Scope    Scope        `json:"scope"`
	Window   store.Window `json:"window"`
	Spent    money.Amount `json:"spent"`
	Limit    money.Amount `json:"limit"`
	Percent  int          `json:"percent"`
	ResetsAt time.Time    `json:"resets_at"`
	// Threshold is the highest warn_at_percent value reached, for warnings.
	Threshold int `json:"threshold,omitempty"`
	// Unsettled counts finished attempts in this window whose cost is still unknown and so not in Spent.
	Unsettled int64 `json:"unsettled"`
}

// Decision is the admission outcome for one request.
type Decision struct {
	Allowed  bool
	Code     string
	Reason   string
	Warnings []Usage
	// Refused is the exhausted window behind a budget_exhausted refusal, so callers can word it per audience.
	Refused *Usage
}

// report returns every user and global window with its spend and the highest warn_at_percent value it reached,
// in a stable order. The dashboard and the alert runner read thresholds from it.
func report(user, global config.Limits, sp store.Spend, warnAt []int, now time.Time) []Usage {
	var out []Usage
	for _, scope := range []struct {
		name      Scope
		limits    config.Limits
		spent     map[store.Window]money.Amount
		unsettled map[store.Window]int64
	}{{ScopeUser, user, sp.User, sp.UserUnsettled}, {ScopeGlobal, global, sp.Global, sp.GlobalUnsettled}} {
		for _, w := range store.Windows {
			_, end := w.Bounds(now)
			limit := limitFor(scope.limits, w)
			spent := scope.spent[w]
			u := Usage{Scope: scope.name, Window: w, Spent: spent, Limit: limit, Percent: percent(spent, limit), ResetsAt: end,
				Unsettled: scope.unsettled[w]}
			for _, t := range warnAt {
				if u.Percent >= t {
					u.Threshold = t
				}
			}
			out = append(out, u)
		}
	}
	return out
}

// evaluate is the pure admission rule: any exhausted window refuses; reached thresholds become warnings.
// Unknown costs do not refuse: a budget may be overrun slightly by requests whose cost arrives later.
func evaluate(user, global config.Limits, sp store.Spend, warnAt []int, now time.Time) Decision {
	var d Decision
	for _, u := range report(user, global, sp, warnAt, now) {
		if u.Spent >= u.Limit {
			return Decision{Code: CodeBudgetExhausted, Refused: &u,
				Reason: fmt.Sprintf("%s %s budget exhausted: spent $%s of $%s; resets at %s",
					u.Scope, u.Window, u.Spent, u.Limit, u.ResetsAt.Format(time.RFC3339))}
		}
		if u.Threshold > 0 {
			d.Warnings = append(d.Warnings, u)
		}
	}
	d.Allowed = true
	return d
}

func limitFor(l config.Limits, w store.Window) money.Amount {
	switch w {
	case store.WindowFiveHours:
		return l.FiveHours
	case store.WindowDay:
		return l.Day
	case store.WindowWeek:
		return l.Week
	default:
		return l.Month
	}
}

// percent rounds down; a zero limit counts as fully used. It saturates instead of overflowing.
func percent(spent, limit money.Amount) int {
	if limit <= 0 {
		return 100
	}
	p := new(big.Int).Mul(big.NewInt(int64(spent)), big.NewInt(100))
	p.Quo(p, big.NewInt(int64(limit)))
	if !p.IsInt64() || p.Int64() > math.MaxInt {
		return math.MaxInt
	}
	return int(p.Int64())
}

// Store is the spend data the checker reads.
type Store interface {
	Spend(ctx context.Context, userEmail string, now time.Time) (store.Spend, error)
}

// Checker applies the policy from config.yaml to recorded spend.
type Checker struct {
	cfg   *config.Holder
	store Store
	now   func() time.Time
}

// NewChecker builds a checker.
func NewChecker(cfg *config.Holder, s Store) *Checker {
	return &Checker{cfg: cfg, store: s, now: func() time.Time { return time.Now().UTC() }}
}

// Check decides admission for an allowlisted user. An error means accounting is unavailable,
// and the caller must refuse the request.
func (c *Checker) Check(ctx context.Context, email string) (Decision, error) {
	cfg := config.For(ctx, c.cfg)
	user, sp, now, err := c.load(ctx, cfg, email)
	if err != nil {
		return Decision{}, err
	}
	return evaluate(cfg.UserLimits(user), cfg.Budget.GlobalLimits, sp, cfg.Budget.WarnAtPercent, now), nil
}

// Status reports every window for the dashboard, each with its unsettled count.
func (c *Checker) Status(ctx context.Context, email string) ([]Usage, error) {
	cfg := config.For(ctx, c.cfg)
	user, sp, now, err := c.load(ctx, cfg, email)
	if err != nil {
		return nil, err
	}
	return report(cfg.UserLimits(user), cfg.Budget.GlobalLimits, sp, cfg.Budget.WarnAtPercent, now), nil
}

func (c *Checker) load(ctx context.Context, cfg *config.Config, email string) (config.User, store.Spend, time.Time, error) {
	now := c.now()
	user, _ := cfg.User(email)
	sp, err := c.store.Spend(ctx, user.Email, now)
	if err != nil {
		return user, sp, now, fmt.Errorf("budget: spend: %w", err)
	}
	return user, sp, now, nil
}
