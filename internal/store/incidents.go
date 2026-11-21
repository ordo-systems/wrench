package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Severity orders alert importance.
type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Condition is an alert condition that currently holds. Key identifies the condition across checks,
// so one continuing problem stays one incident. UserEmail scopes per-user conditions; empty is global.
type Condition struct {
	Key       string
	Severity  Severity
	UserEmail string
	Message   string
}

// Incident is a stored condition with its notification state.
type Incident struct {
	ID              int64      `json:"id"`
	Key             string     `json:"key"`
	Severity        Severity   `json:"severity"`
	UserEmail       string     `json:"user_email"`
	Message         string     `json:"message"`
	OpenedAt        time.Time  `json:"opened_at"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	LastNotifiedAt  *time.Time `json:"last_notified_at"`
	ResolvedAt      *time.Time `json:"resolved_at"`
	ResolveNotified bool       `json:"resolve_notified"`
}

const incidentColumns = `id, key, severity, user_email, message, opened_at, last_seen_at, last_notified_at,
	resolved_at, resolve_notified`

func scanIncident(r pgx.CollectableRow) (Incident, error) {
	var i Incident
	err := r.Scan(&i.ID, &i.Key, &i.Severity, &i.UserEmail, &i.Message, &i.OpenedAt, &i.LastSeenAt,
		&i.LastNotifiedAt, &i.ResolvedAt, &i.ResolveNotified)
	i.OpenedAt, i.LastSeenAt = i.OpenedAt.UTC(), i.LastSeenAt.UTC()
	i.LastNotifiedAt, i.ResolvedAt = utcPtr(i.LastNotifiedAt), utcPtr(i.ResolvedAt)
	return i, err
}

// SyncIncidents makes the open incidents equal the given conditions: new conditions open incidents,
// continuing ones refresh their message and last-seen time, and open incidents whose condition no
// longer holds are resolved. It reports whether an incident opened or resolved.
func (s *Store) SyncIncidents(ctx context.Context, at time.Time, active []Condition) (bool, error) {
	changed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		keys := make([]string, 0, len(active))
		for _, c := range active {
			keys = append(keys, c.Key)
			// xmax is zero only on a freshly inserted row, not on one the conflict clause updated.
			var opened bool
			err := tx.QueryRow(ctx, `
				INSERT INTO alert_incidents (key, severity, user_email, message, opened_at, last_seen_at)
				VALUES ($1, $2, $3, $4, $5, $5)
				ON CONFLICT (key) WHERE resolved_at IS NULL
				DO UPDATE SET severity = EXCLUDED.severity, message = EXCLUDED.message, last_seen_at = EXCLUDED.last_seen_at
				RETURNING (xmax = 0)`,
				c.Key, c.Severity, c.UserEmail, c.Message, at).Scan(&opened)
			if err != nil {
				return err
			}
			changed = changed || opened
		}
		tag, err := tx.Exec(ctx, `UPDATE alert_incidents SET resolved_at = $1
			WHERE resolved_at IS NULL AND NOT (key = ANY($2))`, at, keys)
		changed = changed || tag.RowsAffected() > 0
		return err
	})
	return changed && err == nil, err
}

// DueIncidents returns open incidents never notified or last notified at least repeatAfter ago, and
// resolved incidents whose resolution was not announced yet, provided their opening was.
func (s *Store) DueIncidents(ctx context.Context, at time.Time, repeatAfter time.Duration) ([]Incident, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+incidentColumns+` FROM alert_incidents
		WHERE (resolved_at IS NULL AND (last_notified_at IS NULL OR last_notified_at <= $1::timestamptz - $2::interval))
			OR (resolved_at IS NOT NULL AND NOT resolve_notified AND last_notified_at IS NOT NULL)
		ORDER BY opened_at, id`, at, repeatAfter)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanIncident)
}

// MarkNotified records a delivered notification: the opening or a repeat for open incidents, the
// resolution for resolved ones.
func (s *Store) MarkNotified(ctx context.Context, id int64, at time.Time) error {
	return s.execAffecting(ctx, ErrNotFound, `UPDATE alert_incidents
		SET last_notified_at = CASE WHEN resolved_at IS NULL THEN $2 ELSE last_notified_at END,
			resolve_notified = resolved_at IS NOT NULL
		WHERE id = $1`, id, at)
}

// ListIncidents returns open incidents first, then newest first. A non-empty userEmail returns that
// user's incidents and system-wide ones; an empty one returns all.
func (s *Store) ListIncidents(ctx context.Context, userEmail string, limit int) ([]Incident, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+incidentColumns+` FROM alert_incidents
		WHERE ($1 = '' OR user_email IN ($1, '')) ORDER BY resolved_at IS NULL DESC, opened_at DESC, id DESC LIMIT $2`,
		userEmail, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanIncident)
}
