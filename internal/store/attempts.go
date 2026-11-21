package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ordo-systems/wrench/internal/money"
)

// Outcome is the terminal result of an attempt. An empty value means the attempt is still running.
type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomeError     Outcome = "error"
	OutcomeCancelled Outcome = "cancelled"
)

// CostState tracks whether a charge is confirmed. A pending cost is never treated as free.
type CostState string

const (
	CostPending   CostState = "pending"
	CostConfirmed CostState = "confirmed"
)

// Tokens holds provider-reported token counts. Nil means the field was not reported, not zero.
type Tokens struct {
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	Reasoning  *int64 `json:"reasoning"`
	CacheRead  *int64 `json:"cache_read"`
	CacheWrite *int64 `json:"cache_write"`
}

// Attempt is one upstream call of a logical request: the unit of charging and of every aggregate.
// Each paid attempt is charged separately. StartedAt is persisted before forwarding
// and decides which budget windows the charge belongs to.
type Attempt struct {
	ID                string        `json:"id"`
	RequestID         string        `json:"request_id"`
	UserEmail         string        `json:"user_email"`
	SessionID         string        `json:"session_id"`
	RequestedModel    string        `json:"requested_model"`
	UpstreamModel     string        `json:"upstream_model"`
	Provider          string        `json:"provider"`
	InferenceProvider string        `json:"inference_provider"`
	GenerationID      string        `json:"generation_id"`
	StartedAt         time.Time     `json:"started_at"`
	FirstTokenAt      *time.Time    `json:"first_token_at"`
	FinishedAt        *time.Time    `json:"finished_at"`
	Outcome           Outcome       `json:"outcome"`
	ErrorReason       string        `json:"error_reason"`
	Tokens            Tokens        `json:"tokens"`
	Cost              *money.Amount `json:"cost"`
	CostState         CostState     `json:"cost_state"`
}

// AttemptResult is what the proxy learned once the upstream call ended.
type AttemptResult struct {
	UpstreamModel     string
	Provider          string
	InferenceProvider string
	GenerationID      string
	FirstTokenAt      *time.Time
	FinishedAt        time.Time
	Outcome           Outcome
	ErrorReason       string
	Tokens            Tokens
	Cost              *money.Amount
	// NotSent means the request provably never reached OpenRouter, so nothing can have been charged.
	NotSent bool
}

// StartAttempt inserts a pending attempt before the call is forwarded, so a crash cannot lose the charge.
func (s *Store) StartAttempt(ctx context.Context, a Attempt) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO attempts (id, request_id, user_email, session_id, requested_model, started_at, cost_state)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending')`,
		a.ID, a.RequestID, a.UserEmail, a.SessionID, a.RequestedModel, a.StartedAt)
	return err
}

// FinishAttempt stores the result once; repeating it for a finished attempt is a no-op.
// A reported cost is confirmed and journaled; every attempt with a generation id is scheduled for
// reconciliation, which settles unknown costs and verifies reported ones against provider metadata.
func (s *Store) FinishAttempt(ctx context.Context, id string, r AttemptResult) error {
	cost, source := r.Cost, ChargeStream
	if r.NotSent {
		zero := money.Amount(0)
		cost, source = &zero, ChargeNotSent
	}
	var generation *string
	if r.GenerationID != "" {
		generation = &r.GenerationID
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE attempts SET
				upstream_model = $2, provider = $3, inference_provider = $4, generation_id = COALESCE($5, generation_id),
				first_token_at = $6, finished_at = $7, outcome = $8, error_reason = $9,
				input_tokens = $10, output_tokens = $11, reasoning_tokens = $12, cache_read_tokens = $13, cache_write_tokens = $14,
				cost = $15::numeric, cost_state = CASE WHEN $15::numeric IS NULL THEN 'pending' ELSE 'confirmed' END,
				reconcile_at = CASE WHEN COALESCE($5, generation_id) IS NOT NULL THEN $16::timestamptz END
			WHERE id = $1 AND finished_at IS NULL`,
			id, r.UpstreamModel, r.Provider, r.InferenceProvider, generation,
			r.FirstTokenAt, r.FinishedAt, string(r.Outcome), r.ErrorReason,
			r.Tokens.Input, r.Tokens.Output, r.Tokens.Reasoning, r.Tokens.CacheRead, r.Tokens.CacheWrite,
			amountText(cost), r.FinishedAt.Add(ReconcileDelay(0)))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return existsOrNotFound(ctx, tx, id)
		}
		if cost == nil {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO charge_events (attempt_id, source, cost, recorded_at) VALUES ($1, $2, $3::numeric, $4)`,
			id, source, cost.String(), r.FinishedAt)
		return err
	})
}

func existsOrNotFound(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string) error {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM attempts WHERE id = $1)", id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("attempt %s: %w", id, ErrNotFound)
	}
	return nil
}

func amountText(a *money.Amount) *string {
	if a == nil {
		return nil
	}
	v := a.String()
	return &v
}

const attemptColumns = `id, request_id, user_email, session_id, requested_model,
	upstream_model, provider, inference_provider, generation_id, started_at, first_token_at, finished_at,
	outcome, error_reason, input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_write_tokens,
	cost::text, cost_state`

// ListAttempts returns matching attempts, newest first.
func (s *Store) ListAttempts(ctx context.Context, f AttemptFilter, limit int) ([]Attempt, error) {
	where, args := f.where()
	rows, err := s.pool.Query(ctx, "SELECT "+attemptColumns+" FROM attempts "+where+
		fmt.Sprintf(" ORDER BY started_at DESC, id LIMIT $%d", len(args)+1), append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanAttempt)
}

func scanAttempt(r pgx.CollectableRow) (Attempt, error) {
	var (
		a                    Attempt
		generation, outcome  *string
		cost                 *string
		firstToken, finished *time.Time
	)
	err := r.Scan(&a.ID, &a.RequestID, &a.UserEmail, &a.SessionID, &a.RequestedModel,
		&a.UpstreamModel, &a.Provider, &a.InferenceProvider, &generation, &a.StartedAt, &firstToken, &finished,
		&outcome, &a.ErrorReason, &a.Tokens.Input, &a.Tokens.Output, &a.Tokens.Reasoning, &a.Tokens.CacheRead, &a.Tokens.CacheWrite,
		&cost, &a.CostState)
	if err != nil {
		return a, err
	}
	a.StartedAt = a.StartedAt.UTC()
	a.FirstTokenAt, a.FinishedAt = utcPtr(firstToken), utcPtr(finished)
	if generation != nil {
		a.GenerationID = *generation
	}
	if outcome != nil {
		a.Outcome = Outcome(*outcome)
	}
	a.Cost, err = parseAmount(cost)
	return a, err
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

func parseAmount(s *string) (*money.Amount, error) {
	if s == nil {
		return nil, nil
	}
	v, err := money.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
