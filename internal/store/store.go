// Package store owns PostgreSQL access: connection, migrations, sessions and the attempt journal.
// The journal is the single source for budgets, usage aggregates and dashboard data.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound reports a missing attempt.
	ErrNotFound = errors.New("store: not found")
	// ErrAttemptRunning reports a cost statement for an attempt that has not finished yet.
	ErrAttemptRunning = errors.New("store: attempt is still running")
	// ErrSessionOwner reports a session ID that belongs to another user.
	ErrSessionOwner = errors.New("store: session belongs to another user")
	// ErrSlowDown reports a device code polled again before its interval passed.
	ErrSlowDown = errors.New("store: device code polled too fast")
	// ErrDeviceCodesFull reports that the limit of live device codes is reached.
	ErrDeviceCodesFull = errors.New("store: too many pending device logins")
)

// Store wraps the PostgreSQL connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL and verifies the connection.
// Errors never include the DSN, which carries the database password.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, errors.New("store: invalid PostgreSQL DSN")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping reports whether accounting storage is available; readiness and admission depend on it.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// execAffecting runs a statement that must change at least one row and returns missing otherwise.
func (s *Store) execAffecting(ctx context.Context, missing error, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return missing
	}
	return nil
}
