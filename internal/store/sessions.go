package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session is server-side metadata of one client session; conversation history stays on the client.
// The device is the key that authenticated the session's requests; empty when unknown.
type Session struct {
	ID         string    `json:"id"`
	UserEmail  string    `json:"user_email"`
	StartedAt  time.Time `json:"started_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name"`
	// Agent is the client product token from the latest request that sent one, e.g. "opencode/2.3.4".
	Agent string `json:"agent"`
}

// TouchSession creates the session on first use and advances LastSeenAt afterwards. keyID is the device key that
// authenticated the request, nil for other credentials; the first key seen names the session's device.
// agent is the client product token; an empty one keeps the stored value.
// A session ID already owned by another user is rejected with ErrSessionOwner.
func (s *Store) TouchSession(ctx context.Context, id, userEmail string, keyID *int64, agent string, at time.Time) error {
	if err := s.execAffecting(ctx, ErrSessionOwner, `
		INSERT INTO sessions (id, user_email, started_at, last_seen_at, client_key_id, agent) VALUES ($1, $2, $3, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET last_seen_at = GREATEST(sessions.last_seen_at, EXCLUDED.last_seen_at),
			client_key_id = COALESCE(sessions.client_key_id, EXCLUDED.client_key_id),
			agent = CASE WHEN EXCLUDED.agent = '' THEN sessions.agent ELSE EXCLUDED.agent END
		WHERE sessions.user_email = EXCLUDED.user_email`, id, userEmail, at, keyID, agent); err != nil {
		return err
	}
	return s.markPresent(ctx, id, at)
}

// presenceBucket is the granularity of session activity charts.
const presenceBucket = 5 * time.Minute

// markPresent records that the session was live in the bucket containing at.
func (s *Store) markPresent(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO session_presence (session_id, bucket) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, id, at.UTC().Truncate(presenceBucket))
	return err
}

// ListSessions returns sessions ordered by LastSeenAt, newest first; an empty userEmail means all users.
// status is "", "active" or "offline"; a session is offline when last seen before offlineBefore, the single
// cutoff the caller derives from the offline timeout.
func (s *Store) ListSessions(ctx context.Context, userEmail, status string, offlineBefore time.Time, limit int) ([]Session, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.user_email, s.started_at, s.last_seen_at, COALESCE(k.device_id, ''), COALESCE(k.device_name, ''), s.agent
		FROM sessions s LEFT JOIN client_keys k ON k.id = s.client_key_id
		WHERE ($1 = '' OR s.user_email = $1)
		  AND ($2 = '' OR ($2 = 'offline') = (s.last_seen_at < $3))
		ORDER BY s.last_seen_at DESC, s.id LIMIT $4`, userEmail, status, offlineBefore, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Session, error) {
		var v Session
		err := r.Scan(&v.ID, &v.UserEmail, &v.StartedAt, &v.LastSeenAt, &v.DeviceID, &v.DeviceName, &v.Agent)
		v.StartedAt, v.LastSeenAt = v.StartedAt.UTC(), v.LastSeenAt.UTC()
		return v, err
	})
}
