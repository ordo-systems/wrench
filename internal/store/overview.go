package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// UserState is the stored access state of one user. Users who never signed in have no state.
type UserState struct {
	Email           string     `json:"email"`
	Status          UserStatus `json:"status"`
	StatusCheckedAt *time.Time `json:"status_checked_at"`
	ActiveKeys      int64      `json:"active_keys"`
	LastKeyUseAt    *time.Time `json:"last_key_use_at"`
}

// UserStates returns stored access state by email; keys count as active until revoked or expired at now.
func (s *Store) UserStates(ctx context.Context, now time.Time) (map[string]UserState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.email, u.status, u.status_checked_at,
			COUNT(k.id) FILTER (WHERE k.revoked_at IS NULL AND k.expires_at > $1), MAX(k.last_used_at)
		FROM users u LEFT JOIN client_keys k ON k.user_id = u.id
		GROUP BY u.id ORDER BY u.email`, now)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (UserState, error) {
		var u UserState
		err := r.Scan(&u.Email, &u.Status, &u.StatusCheckedAt, &u.ActiveKeys, &u.LastKeyUseAt)
		u.StatusCheckedAt, u.LastKeyUseAt = utcPtr(u.StatusCheckedAt), utcPtr(u.LastKeyUseAt)
		return u, err
	})
	out := map[string]UserState{}
	for _, u := range list {
		out[u.Email] = u
	}
	return out, err
}

// ModelHealth summarizes recent attempts of one catalog model, the availability of its route.
type ModelHealth struct {
	Model           string     `json:"model"`
	Attempts        int64      `json:"attempts"`
	Errors          int64      `json:"errors"`
	FirstTokenP95   *float64   `json:"first_token_p95_ms"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	LastErrorAt     *time.Time `json:"last_error_at"`
	LastErrorReason string     `json:"last_error_reason"`
}

// ModelHealth aggregates attempts started at or after from, per requested model.
func (s *Store) ModelHealth(ctx context.Context, from time.Time) ([]ModelHealth, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT requested_model, COUNT(*), COUNT(*) FILTER (WHERE outcome = 'error'),
			percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM first_token_at - started_at) * 1000)
				FILTER (WHERE first_token_at IS NOT NULL),
			MAX(finished_at) FILTER (WHERE outcome = 'success'), MAX(finished_at) FILTER (WHERE outcome = 'error'),
			COALESCE((array_agg(error_reason ORDER BY finished_at DESC) FILTER (WHERE outcome = 'error'))[1], '')
		FROM attempts WHERE started_at >= $1 GROUP BY requested_model ORDER BY requested_model`, from)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ModelHealth, error) {
		var m ModelHealth
		err := r.Scan(&m.Model, &m.Attempts, &m.Errors, &m.FirstTokenP95, &m.LastSuccessAt, &m.LastErrorAt, &m.LastErrorReason)
		m.LastSuccessAt, m.LastErrorAt = utcPtr(m.LastSuccessAt), utcPtr(m.LastErrorAt)
		return m, err
	})
}
