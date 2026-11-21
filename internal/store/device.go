package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// DeviceStatus is where a device login stands.
type DeviceStatus string

const (
	DevicePending  DeviceStatus = "pending"
	DeviceApproved DeviceStatus = "approved"
	DeviceDenied   DeviceStatus = "denied"
	// DeviceConsumed is an approved code whose key was handed out; it is never returned again.
	DeviceConsumed DeviceStatus = "consumed"
)

// DeviceCode is one device login as a poll sees it. UserID is set once a user decided.
type DeviceCode struct {
	Status     DeviceStatus
	UserID     int64
	ExpiresAt  time.Time
	LastPollAt *time.Time
}

// CreateDeviceCode stores a new device login by the hashes of its codes and drops expired ones. With limit live
// codes already stored it is ErrDeviceCodesFull: the endpoint is unauthenticated, so the table must stay bounded.
func (s *Store) CreateDeviceCode(ctx context.Context, deviceHash, userCodeHash []byte, expires time.Time, limit int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize creations so concurrent requests cannot overshoot the limit between the count and the insert.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('device_codes'))"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM device_codes WHERE expires_at <= now()"); err != nil {
			return err
		}
		var live int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM device_codes").Scan(&live); err != nil {
			return err
		}
		if live >= limit {
			return ErrDeviceCodesFull
		}
		_, err := tx.Exec(ctx, `INSERT INTO device_codes (device_code_hash, user_code_hash, expires_at) VALUES ($1, $2, $3)`,
			deviceHash, userCodeHash, expires)
		return err
	})
}

// DecideDeviceCode records a signed-in user's approval or denial of a pending, unexpired code; ErrNotFound otherwise.
func (s *Store) DecideDeviceCode(ctx context.Context, userCodeHash []byte, userEmail string, approve bool, now time.Time) error {
	status := DeviceDenied
	if approve {
		status = DeviceApproved
	}
	return s.execAffecting(ctx, ErrNotFound, `
		UPDATE device_codes SET status = $3, user_id = (SELECT id FROM users WHERE email = $2)
		WHERE user_code_hash = $1 AND status = 'pending' AND expires_at > $4`, userCodeHash, userEmail, status, now)
}

// PollDeviceCode records a client's poll and returns the code's state. A poll within interval of the previous one is
// ErrSlowDown and changes nothing; an unknown, expired or consumed code is ErrNotFound. An approved code is consumed
// by this poll, so only one key is ever issued for it.
func (s *Store) PollDeviceCode(ctx context.Context, deviceHash []byte, now time.Time, interval time.Duration) (DeviceCode, error) {
	var c DeviceCode
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var userID *int64
		err := tx.QueryRow(ctx, `
			SELECT status, user_id, expires_at, last_poll_at FROM device_codes
			WHERE device_code_hash = $1 AND expires_at > $2 AND status <> 'consumed' FOR UPDATE`, deviceHash, now).
			Scan(&c.Status, &userID, &c.ExpiresAt, &c.LastPollAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if c.LastPollAt != nil && now.Sub(*c.LastPollAt) < interval {
			return ErrSlowDown
		}
		if userID != nil {
			c.UserID = *userID
		}
		next := c.Status
		if next == DeviceApproved {
			next = DeviceConsumed
		}
		_, err = tx.Exec(ctx, `UPDATE device_codes SET last_poll_at = $2, status = $3 WHERE device_code_hash = $1`,
			deviceHash, now, next)
		return err
	})
	if err != nil {
		return DeviceCode{}, err
	}
	return c, nil
}

// UserByID returns a user by id, or ErrNotFound.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// IssueDeviceKey is IssueClientKey for the device login: the key also gets a refresh token.
func (s *Store) IssueDeviceKey(ctx context.Context, userID int64, deviceID, deviceName string, tokenHash, refreshHash []byte,
	now, expires time.Time) (ClientKey, error) {
	var k ClientKey
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		k, err = issueKey(ctx, tx, userID, deviceID, deviceName, tokenHash, refreshHash, now, expires)
		return err
	})
	return k, err
}

// RefreshClientKey replaces the active, unexpired key holding refreshHash with a new key and refresh token for the
// same device, in one transaction, and returns it with its owner. A used, revoked or expired refresh token is
// ErrNotFound.
func (s *Store) RefreshClientKey(ctx context.Context, refreshHash, tokenHash, newRefreshHash []byte, now, expires time.Time) (ClientKey, User, error) {
	var (
		k ClientKey
		u User
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var userID int64
		var deviceID, deviceName string
		err := tx.QueryRow(ctx, `
			SELECT user_id, device_id, device_name FROM client_keys
			WHERE refresh_token_hash = $1 AND revoked_at IS NULL AND expires_at > $2 FOR UPDATE`, refreshHash, now).
			Scan(&userID, &deviceID, &deviceName)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if k, err = issueKey(ctx, tx, userID, deviceID, deviceName, tokenHash, newRefreshHash, now, expires); err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE id = $1", userID))
		return err
	})
	return k, u, err
}
