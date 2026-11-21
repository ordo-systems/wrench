package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrIdentityConflict reports a login whose email and GitLab subject disagree with the recorded binding.
// Resolving it is a deliberate operator migration, never an automatic rebind.
var ErrIdentityConflict = errors.New("store: identity conflicts with the recorded binding")

// UserStatus is the last confirmed GitLab account state. Pending means never confirmed.
type UserStatus string

const (
	UserPending UserStatus = "pending"
	UserActive  UserStatus = "active"
	UserBlocked UserStatus = "blocked"
)

// User is an allowlisted email bound to one GitLab identity.
type User struct {
	ID              int64
	Email           string
	Issuer          string
	Subject         string
	GitLabID        int64
	Status          UserStatus
	StatusCheckedAt *time.Time
}

const userColumns = `id, email, issuer, subject, gitlab_id, status, status_checked_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Issuer, &u.Subject, &u.GitLabID, &u.Status, &u.StatusCheckedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	u.StatusCheckedAt = utcPtr(u.StatusCheckedAt)
	return u, err
}

// BindIdentity returns the user for a verified login, creating the binding on first login.
// The email and the (issuer, subject) pair must either both be new or both match one existing row.
func (s *Store) BindIdentity(ctx context.Context, email, issuer, subject string, gitlabID int64, at time.Time) (User, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (email, issuer, subject, gitlab_id, status, created_at)
		VALUES ($1, $2, $3, $4, 'pending', $5) ON CONFLICT DO NOTHING`, email, issuer, subject, gitlabID, at)
	if err != nil {
		return User{}, err
	}
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE email = $1", email))
	if errors.Is(err, ErrNotFound) {
		return User{}, fmt.Errorf("%w: GitLab account already bound to another email", ErrIdentityConflict)
	}
	if err != nil {
		return User{}, err
	}
	if u.Issuer != issuer || u.Subject != subject {
		return User{}, fmt.Errorf("%w: email already bound to another GitLab account", ErrIdentityConflict)
	}
	return u, nil
}

// ListUsers returns every bound user for status synchronization.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+userColumns+" FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) { return scanUser(r) })
}

// RecordStatus stores a confirmed GitLab account state. A confirmed disable blocks the user
// and revokes every key and session in the same transaction.
func (s *Store) RecordStatus(ctx context.Context, userID int64, active bool, at time.Time) error {
	status := UserActive
	if !active {
		status = UserBlocked
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE users SET status = $2, status_checked_at = $3 WHERE id = $1", userID, status, at); err != nil {
			return err
		}
		if active {
			return nil
		}
		_, err := revokeAll(ctx, tx, userID, at)
		return err
	})
}

func revokeAll(ctx context.Context, tx pgx.Tx, userID int64, at time.Time) (int64, error) {
	keys, err := tx.Exec(ctx, "UPDATE client_keys SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL", userID, at)
	if err != nil {
		return 0, err
	}
	sessions, err := tx.Exec(ctx, "UPDATE web_sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL", userID, at)
	return keys.RowsAffected() + sessions.RowsAffected(), err
}

// RevokeUserCredentials revokes every key and session of a user and returns how many were active.
func (s *Store) RevokeUserCredentials(ctx context.Context, email string, at time.Time) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", email).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var err error
		n, err = revokeAll(ctx, tx, id, at)
		return err
	})
	return n, err
}

// Credential is a valid key or session together with its owner's account state.
type Credential struct {
	ID              int64
	UserID          int64
	UserEmail       string
	UserStatus      UserStatus
	StatusCheckedAt *time.Time
	ExpiresAt       time.Time
}

func scanCredential(row pgx.Row) (Credential, error) {
	var c Credential
	err := row.Scan(&c.ID, &c.UserID, &c.UserEmail, &c.UserStatus, &c.StatusCheckedAt, &c.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	c.StatusCheckedAt = utcPtr(c.StatusCheckedAt)
	c.ExpiresAt = c.ExpiresAt.UTC()
	return c, err
}

// CreateWebSession stores a dashboard session by token hash.
func (s *Store) CreateWebSession(ctx context.Context, userID int64, tokenHash []byte, now, expires time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO web_sessions (token_hash, user_id, created_at, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, userID, now, expires)
	return err
}

// LookupWebSession returns an unexpired, unrevoked session or ErrNotFound.
func (s *Store) LookupWebSession(ctx context.Context, tokenHash []byte, now time.Time) (Credential, error) {
	return scanCredential(s.pool.QueryRow(ctx, `
		SELECT 0, u.id, u.email, u.status, u.status_checked_at, w.expires_at
		FROM web_sessions w JOIN users u ON u.id = w.user_id
		WHERE w.token_hash = $1 AND w.revoked_at IS NULL AND w.expires_at > $2`, tokenHash, now))
}

// RevokeWebSession ends one session; an unknown or already revoked session is not an error.
func (s *Store) RevokeWebSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := s.pool.Exec(ctx, "UPDATE web_sessions SET revoked_at = $2 WHERE token_hash = $1 AND revoked_at IS NULL", tokenHash, now)
	return err
}

// ClientKey is the metadata of a device key; the token itself is never stored.
type ClientKey struct {
	ID         int64      `json:"id"`
	UserEmail  string     `json:"user_email"`
	DeviceID   string     `json:"device_id"`
	DeviceName string     `json:"device_name"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// IssueClientKey rotates the device's key: any active key of the same (user, device) is revoked first.
func (s *Store) IssueClientKey(ctx context.Context, userID int64, deviceID, deviceName string, tokenHash []byte, now, expires time.Time) (ClientKey, error) {
	var k ClientKey
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		k, err = issueKey(ctx, tx, userID, deviceID, deviceName, tokenHash, nil, now, expires)
		return err
	})
	return k, err
}

// issueKey revokes the device's active key and inserts a new one inside tx; refreshHash is nil for keys that cannot
// be refreshed.
func issueKey(ctx context.Context, tx pgx.Tx, userID int64, deviceID, deviceName string, tokenHash, refreshHash []byte,
	now, expires time.Time) (ClientKey, error) {
	k := ClientKey{DeviceID: deviceID, DeviceName: deviceName, CreatedAt: now, ExpiresAt: expires}
	if _, err := tx.Exec(ctx, "UPDATE client_keys SET revoked_at = $3 WHERE user_id = $1 AND device_id = $2 AND revoked_at IS NULL",
		userID, deviceID, now); err != nil {
		return k, err
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO client_keys (token_hash, user_id, device_id, device_name, created_at, expires_at, refresh_token_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, (SELECT email FROM users WHERE id = $2)`,
		tokenHash, userID, deviceID, deviceName, now, expires, refreshHash).Scan(&k.ID, &k.UserEmail)
	return k, err
}

// LookupClientKey returns an unexpired, unrevoked key and records its use, or ErrNotFound.
func (s *Store) LookupClientKey(ctx context.Context, tokenHash []byte, now time.Time) (Credential, error) {
	return scanCredential(s.pool.QueryRow(ctx, `
		WITH k AS (
			UPDATE client_keys SET last_used_at = $2
			WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $2
			RETURNING id, user_id, expires_at)
		SELECT k.id, u.id, u.email, u.status, u.status_checked_at, k.expires_at
		FROM k JOIN users u ON u.id = k.user_id`, tokenHash, now))
}

// ListClientKeys returns keys newest first; an empty userEmail means all users.
func (s *Store) ListClientKeys(ctx context.Context, userEmail string) ([]ClientKey, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT k.id, u.email, k.device_id, k.device_name, k.created_at, k.expires_at, k.last_used_at, k.revoked_at
		FROM client_keys k JOIN users u ON u.id = k.user_id
		WHERE ($1 = '' OR u.email = $1)
		ORDER BY k.created_at DESC, k.id DESC LIMIT 500`, userEmail)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClientKey, error) {
		var k ClientKey
		err := r.Scan(&k.ID, &k.UserEmail, &k.DeviceID, &k.DeviceName, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.RevokedAt)
		k.CreatedAt, k.ExpiresAt = k.CreatedAt.UTC(), k.ExpiresAt.UTC()
		k.LastUsedAt, k.RevokedAt = utcPtr(k.LastUsedAt), utcPtr(k.RevokedAt)
		return k, err
	})
}

// RevokeClientKey revokes a key owned by userEmail; an empty userEmail allows any owner (admin).
// A key that does not exist, is not owned or is already revoked yields ErrNotFound. It returns the owner's email.
func (s *Store) RevokeClientKey(ctx context.Context, id int64, userEmail string, now time.Time) (string, error) {
	var owner string
	err := s.pool.QueryRow(ctx, `
		UPDATE client_keys k SET revoked_at = $3 FROM users u
		WHERE k.id = $1 AND u.id = k.user_id AND ($2 = '' OR u.email = $2) AND k.revoked_at IS NULL
		RETURNING u.email`, id, userEmail, now).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return owner, err
}

// RevokeClientKeys revokes every active (unrevoked, unexpired) key of a user and returns how many it revoked.
// Sessions are untouched, and an unknown user simply has no keys.
func (s *Store) RevokeClientKeys(ctx context.Context, userEmail string, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE client_keys k SET revoked_at = $2 FROM users u
		WHERE u.id = k.user_id AND u.email = $1 AND k.revoked_at IS NULL AND k.expires_at > $2`, userEmail, now)
	return tag.RowsAffected(), err
}

// LoginFlow is the server-side OAuth state of one dashboard sign-in.
type LoginFlow struct {
	State          string
	Nonce          string
	Verifier       string
	BrowserBinding []byte
	// ReturnTo is the same-origin path the sign-in returns to; empty means the dashboard root.
	ReturnTo  string
	ExpiresAt time.Time
}

// CreateLoginFlow stores a new flow and drops expired ones.
func (s *Store) CreateLoginFlow(ctx context.Context, f LoginFlow) error {
	if _, err := s.pool.Exec(ctx, "DELETE FROM login_flows WHERE expires_at <= now()"); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO login_flows (state, nonce, verifier, browser_binding, return_to, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		f.State, f.Nonce, f.Verifier, f.BrowserBinding, f.ReturnTo, f.ExpiresAt)
	return err
}

// ConsumeLoginFlow deletes and returns an unexpired flow, so each state is usable once.
func (s *Store) ConsumeLoginFlow(ctx context.Context, state string, now time.Time) (LoginFlow, error) {
	var f LoginFlow
	err := s.pool.QueryRow(ctx, `
		DELETE FROM login_flows WHERE state = $1 AND expires_at > $2
		RETURNING state, nonce, verifier, browser_binding, return_to, expires_at`, state, now).
		Scan(&f.State, &f.Nonce, &f.Verifier, &f.BrowserBinding, &f.ReturnTo, &f.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}
