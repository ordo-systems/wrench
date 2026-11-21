package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ordo-systems/wrench/internal/money"
)

// ChargeSource says who stated an attempt's cost.
type ChargeSource string

const (
	ChargeStream     ChargeSource = "stream"     // usage.cost in the provider response
	ChargeGeneration ChargeSource = "generation" // provider generation metadata, fetched later
	ChargeNotSent    ChargeSource = "not_sent"   // the request never left Wrench
)

// reconcileOffsets are the delays after an attempt finished at which provider metadata is fetched;
// after the last one, retries continue hourly.
var reconcileOffsets = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}

// ReconcileDelay is the offset from the finish time for the given zero-based try.
func ReconcileDelay(try int) time.Duration {
	if try < len(reconcileOffsets) {
		return reconcileOffsets[try]
	}
	return reconcileOffsets[len(reconcileOffsets)-1] + time.Duration(try-len(reconcileOffsets)+1)*time.Hour
}

// RecordGeneration stores the provider generation id as soon as the stream reveals it,
// so an attempt interrupted by a crash can still be reconciled.
func (s *Store) RecordGeneration(ctx context.Context, attemptID, generationID string) error {
	_, err := s.pool.Exec(ctx, "UPDATE attempts SET generation_id = $2 WHERE id = $1 AND generation_id IS NULL", attemptID, generationID)
	return err
}

// ChargeEvent is one journaled cost statement.
type ChargeEvent struct {
	ID         int64
	AttemptID  string
	Source     ChargeSource
	Cost       money.Amount
	Previous   *money.Amount
	Actor      string
	RecordedAt time.Time
}

// ChargeEvents returns the journal of one attempt, oldest first.
func (s *Store) ChargeEvents(ctx context.Context, attemptID string) ([]ChargeEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, attempt_id, source, cost::text, previous::text, actor, recorded_at
		FROM charge_events WHERE attempt_id = $1 ORDER BY id`, attemptID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ChargeEvent, error) {
		var (
			e        ChargeEvent
			cost     string
			previous *string
		)
		if err := r.Scan(&e.ID, &e.AttemptID, &e.Source, &cost, &previous, &e.Actor, &e.RecordedAt); err != nil {
			return e, err
		}
		e.RecordedAt = e.RecordedAt.UTC()
		var err error
		if e.Cost, err = money.Parse(cost); err != nil {
			return e, err
		}
		e.Previous, err = parseAmount(previous)
		return e, err
	})
}

// Settlement is a cost statement from reconciliation.
type Settlement struct {
	AttemptID string
	Cost      money.Amount
	Source    ChargeSource
	// Provider is the actual inference provider when known.
	Provider string
	Actor    string
	At       time.Time
}

// SettleAttempt confirms or corrects a finished attempt's cost. Restating the current cost changes
// nothing (but still marks reconciliation done); a different cost is journaled with the previous value.
// It reports whether the charge changed.
func (s *Store) SettleAttempt(ctx context.Context, st Settlement) (bool, error) {
	changed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			current  *string
			finished *time.Time
		)
		err := tx.QueryRow(ctx, "SELECT cost::text, finished_at FROM attempts WHERE id = $1 FOR UPDATE", st.AttemptID).Scan(&current, &finished)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("attempt %s: %w", st.AttemptID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if finished == nil {
			return fmt.Errorf("attempt %s: %w", st.AttemptID, ErrAttemptRunning)
		}
		previous, err := parseAmount(current)
		if err != nil {
			return err
		}
		changed = previous == nil || *previous != st.Cost
		if changed {
			if _, err := tx.Exec(ctx, `
				INSERT INTO charge_events (attempt_id, source, cost, previous, actor, recorded_at)
				VALUES ($1, $2, $3::numeric, $4::numeric, $5, $6)`,
				st.AttemptID, st.Source, st.Cost.String(), current, st.Actor, st.At); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `
			UPDATE attempts SET cost = $2::numeric, cost_state = 'confirmed',
				inference_provider = COALESCE(NULLIF($3, ''), inference_provider),
				reconciled_at = $4, reconcile_error = ''
			WHERE id = $1`, st.AttemptID, st.Cost.String(), st.Provider, st.At)
		return err
	})
	return changed, err
}

// Reconciliation is an attempt due for a provider metadata lookup.
type Reconciliation struct {
	ID           string
	GenerationID string
	UserEmail    string
	Tries        int
	FinishedAt   time.Time
}

// DueReconciliations returns finished, unreconciled attempts whose next lookup is due.
func (s *Store) DueReconciliations(ctx context.Context, now time.Time, limit int) ([]Reconciliation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, generation_id, user_email, reconcile_tries, finished_at FROM attempts
		WHERE reconciled_at IS NULL AND reconcile_at IS NOT NULL AND reconcile_at <= $1
		ORDER BY reconcile_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Reconciliation, error) {
		var x Reconciliation
		err := r.Scan(&x.ID, &x.GenerationID, &x.UserEmail, &x.Tries, &x.FinishedAt)
		x.FinishedAt = x.FinishedAt.UTC()
		return x, err
	})
}

// RescheduleReconciliation records a failed lookup and when to try again.
func (s *Store) RescheduleReconciliation(ctx context.Context, attemptID string, next time.Time, reason string) error {
	return s.execAffecting(ctx, ErrNotFound, `
		UPDATE attempts SET reconcile_tries = reconcile_tries + 1, reconcile_at = $2, reconcile_error = $3
		WHERE id = $1 AND reconciled_at IS NULL`, attemptID, next, reason)
}

// RecoverInterrupted closes attempts that started before staleBefore and never finished, as after a
// crash. They stay pending; those with a generation id are scheduled for reconciliation now.
func (s *Store) RecoverInterrupted(ctx context.Context, staleBefore, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE attempts SET finished_at = $2, outcome = 'error',
			error_reason = 'interrupted before the result was recorded',
			reconcile_at = CASE WHEN generation_id IS NOT NULL THEN $2::timestamptz END
		WHERE finished_at IS NULL AND started_at < $1`, staleBefore, now)
	return tag.RowsAffected(), err
}

// Spend is confirmed spend in the current fixed windows for one user and for everyone, with the number of
// finished attempts whose cost is still unknown, so the dashboard can mark windows that may be understated.
type Spend struct {
	User            map[Window]money.Amount
	Global          map[Window]money.Amount
	UserUnsettled   map[Window]int64
	GlobalUnsettled map[Window]int64
}

// Windows lists the budget windows in a stable order.
var Windows = []Window{WindowFiveHours, WindowDay, WindowWeek, WindowMonth}

// Spend sums confirmed costs of attempts that started in each window containing now and counts the finished
// attempts there whose cost is unknown. In-flight attempts are not reserved; their cost lands in the window of
// their start once known.
func (s *Store) Spend(ctx context.Context, userEmail string, now time.Time) (Spend, error) {
	args := []any{userEmail}
	var sums, counts []string
	lo, hi := now, now
	for _, scope := range []string{"user_email = $1 AND ", ""} {
		for _, w := range Windows {
			start, end := w.Bounds(now)
			args = append(args, start, end)
			in := fmt.Sprintf("%sstarted_at >= $%d AND started_at < $%d", scope, len(args)-1, len(args))
			sums = append(sums, fmt.Sprintf("COALESCE(SUM(cost) FILTER (WHERE %s AND cost_state = 'confirmed'), 0)::text", in))
			counts = append(counts, fmt.Sprintf("COUNT(*) FILTER (WHERE %s AND cost_state = 'pending' AND finished_at IS NOT NULL)", in))
			lo, hi = minTime(lo, start), maxTime(hi, end)
		}
	}
	args = append(args, lo, hi)
	query := "SELECT " + strings.Join(append(sums, counts...), ", ") +
		fmt.Sprintf(" FROM attempts WHERE cost_state IN ('confirmed', 'pending') AND started_at >= $%d AND started_at < $%d", len(args)-1, len(args))
	texts := make([]string, len(sums))
	unsettled := make([]int64, len(counts))
	dest := make([]any, 0, len(sums)+len(counts))
	for i := range texts {
		dest = append(dest, &texts[i])
	}
	for i := range unsettled {
		dest = append(dest, &unsettled[i])
	}
	if err := s.pool.QueryRow(ctx, query, args...).Scan(dest...); err != nil {
		return Spend{}, err
	}
	sp := Spend{User: map[Window]money.Amount{}, Global: map[Window]money.Amount{},
		UserUnsettled: map[Window]int64{}, GlobalUnsettled: map[Window]int64{}}
	for i, t := range texts {
		v, err := money.Parse(t)
		if err != nil {
			return Spend{}, err
		}
		w := Windows[i%len(Windows)]
		if i < len(Windows) {
			sp.User[w], sp.UserUnsettled[w] = v, unsettled[i]
		} else {
			sp.Global[w], sp.GlobalUnsettled[w] = v, unsettled[i]
		}
	}
	return sp, nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
