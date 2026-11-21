package login

import (
	"context"
	"log/slog"
	"time"

	"github.com/ordo-systems/wrench/internal/store"
)

// SyncStore is what status synchronization reads and writes.
type SyncStore interface {
	ListUsers(ctx context.Context) ([]store.User, error)
	RecordStatus(ctx context.Context, userID int64, active bool, at time.Time) error
}

// Syncer periodically confirms every bound account with GitLab. A confirmed disable or removal
// blocks the user and revokes all credentials; an unavailable check changes nothing, so the stored
// state ages and eventually blocks inference without being mistaken for a removal.
type Syncer struct {
	store    SyncStore
	gitlab   GitLab
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger
}

// NewSyncer builds a syncer for the configured interval.
func NewSyncer(s SyncStore, gl GitLab, interval time.Duration) *Syncer {
	return &Syncer{store: s, gitlab: gl, interval: interval, now: func() time.Time { return time.Now().UTC() }, log: slog.Default()}
}

// Run syncs immediately and then every interval until ctx is cancelled.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		s.SyncOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SyncResult summarizes one pass for logs and, later, alerts.
type SyncResult struct {
	Checked     int
	Unavailable int
	// Disabled counts users newly blocked in this pass.
	Disabled int
	// Failed means the user list itself could not be read.
	Failed bool
}

// SyncOnce checks every bound user once.
func (s *Syncer) SyncOnce(ctx context.Context) SyncResult {
	var r SyncResult
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		s.log.Error("status sync: users not listed", "err", err)
		return SyncResult{Failed: true}
	}
	for _, u := range users {
		active, err := s.gitlab.AccountActive(ctx, u.GitLabID)
		if err != nil {
			r.Unavailable++
			s.log.Warn("status sync: account status unavailable", "email", u.Email, "err", err)
			continue
		}
		if err := s.store.RecordStatus(ctx, u.ID, active, s.now()); err != nil {
			s.log.Error("status sync: status not stored", "email", u.Email, "err", err)
			continue
		}
		r.Checked++
		if !active && u.Status != store.UserBlocked {
			r.Disabled++
			s.log.Warn("status sync: account disabled in GitLab; credentials revoked", "email", u.Email)
		}
	}
	return r
}
