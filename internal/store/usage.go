package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ordo-systems/wrench/internal/money"
)

// Window is one of the UTC budget windows: fixed-length windows from a common anchor, and the calendar month.
type Window string

const (
	WindowFiveHours Window = "5_hours"
	WindowDay       Window = "day"
	WindowWeek      Window = "week"
	// WindowMonth is the calendar month in UTC; its length varies.
	WindowMonth Window = "month"
)

// windowAnchor is the common fixed origin of all windows, a Monday; changing it on populated data needs a migration.
var windowAnchor = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

var windowLengths = map[Window]time.Duration{
	WindowFiveHours: 5 * time.Hour,
	WindowDay:       24 * time.Hour,
	WindowWeek:      7 * 24 * time.Hour,
}

// ParseWindow validates a window name from outside input.
func ParseWindow(s string) (Window, error) {
	if _, ok := windowLengths[Window(s)]; !ok && Window(s) != WindowMonth {
		return "", fmt.Errorf("unknown window %q", s)
	}
	return Window(s), nil
}

// Bounds returns the UTC window [start, end) that contains at.
func (w Window) Bounds(at time.Time) (start, end time.Time) {
	if w == WindowMonth {
		at = at.UTC()
		start = time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
	d := windowLengths[w]
	offset := at.Sub(windowAnchor)
	n := offset / d
	if offset%d < 0 {
		n--
	}
	start = windowAnchor.Add(n * d)
	return start, start.Add(d)
}

// AttemptFilter selects attempts by StartedAt in [From, To). Empty fields do not filter.
type AttemptFilter struct {
	UserEmail string
	// SessionIDs keeps attempts of any of these sessions.
	SessionIDs []string
	From       time.Time
	To         time.Time
	// Model filters by requested catalog model, Provider by actual inference provider.
	Model    string
	Provider string
}

// where renders the filter as a SQL WHERE clause with positional arguments.
func (f AttemptFilter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.UserEmail != "" {
		add("user_email = $%d", f.UserEmail)
	}
	if len(f.SessionIDs) > 0 {
		add("session_id = ANY($%d)", f.SessionIDs)
	}
	if !f.From.IsZero() {
		add("started_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		add("started_at < $%d", f.To)
	}
	if f.Model != "" {
		add("requested_model = $%d", f.Model)
	}
	if f.Provider != "" {
		add("inference_provider = $%d", f.Provider)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// UsageTotals aggregates attempts. A token sum is nil when no attempt reported that field;
// Cost sums confirmed attempts and Pending counts attempts whose cost is still unknown.
type UsageTotals struct {
	Key      string       `json:"key,omitempty"`
	Tokens   Tokens       `json:"tokens"`
	Cost     money.Amount `json:"cost"`
	Attempts int64        `json:"attempts"`
	Pending  int64        `json:"pending"`
}

const totalsColumns = `SUM(input_tokens)::bigint, SUM(output_tokens)::bigint, SUM(reasoning_tokens)::bigint,
	SUM(cache_read_tokens)::bigint, SUM(cache_write_tokens)::bigint,
	COALESCE(SUM(cost) FILTER (WHERE cost_state = 'confirmed'), 0)::text,
	COUNT(*), COUNT(*) FILTER (WHERE cost_state = 'pending')`

// SumUsage aggregates all attempts matching the filter.
func (s *Store) SumUsage(ctx context.Context, f AttemptFilter) (UsageTotals, error) {
	where, args := f.where()
	rows, err := s.pool.Query(ctx, "SELECT '', "+totalsColumns+" FROM attempts "+where, args...)
	if err != nil {
		return UsageTotals{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanTotals)
}

// SumUsageByUser aggregates matching attempts per user; Key is the user email.
func (s *Store) SumUsageByUser(ctx context.Context, f AttemptFilter) ([]UsageTotals, error) {
	return s.sumGrouped(ctx, "user_email", f)
}

// SumUsageBySession aggregates matching attempts per session; Key is the session ID.
func (s *Store) SumUsageBySession(ctx context.Context, f AttemptFilter) ([]UsageTotals, error) {
	return s.sumGrouped(ctx, "session_id", f)
}

// SumUsageByModel aggregates matching attempts per requested catalog model; Key is the model name.
func (s *Store) SumUsageByModel(ctx context.Context, f AttemptFilter) ([]UsageTotals, error) {
	return s.sumGrouped(ctx, "requested_model", f)
}

// sumGrouped takes column only from the constants above, never from input.
func (s *Store) sumGrouped(ctx context.Context, column string, f AttemptFilter) ([]UsageTotals, error) {
	where, args := f.where()
	rows, err := s.pool.Query(ctx, "SELECT "+column+", "+totalsColumns+" FROM attempts "+where+
		" GROUP BY "+column+" ORDER BY "+column, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanTotals)
}

func scanTotals(r pgx.CollectableRow) (UsageTotals, error) {
	var (
		t    UsageTotals
		cost string
	)
	err := r.Scan(&t.Key, &t.Tokens.Input, &t.Tokens.Output, &t.Tokens.Reasoning, &t.Tokens.CacheRead, &t.Tokens.CacheWrite,
		&cost, &t.Attempts, &t.Pending)
	if err != nil {
		return t, err
	}
	t.Cost, err = money.Parse(cost)
	return t, err
}
