package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ReloadStatus describes the configuration in force and the last reload attempt. Revision is the sha256 of the
// configuration bytes; Error is the last rejected candidate's
// error, empty once a valid configuration is in force again.
type ReloadStatus struct {
	Revision string    `json:"revision"`
	LoadedAt time.Time `json:"loaded_at"`
	Error    string    `json:"error,omitempty"`
}

// Holder keeps the configuration in force and swaps it atomically on a valid reload. Readers take one snapshot
// with Current per request, so a request never mixes two configurations.
type Holder struct {
	path string
	now  func() time.Time
	cfg  atomic.Pointer[Config]

	mu     sync.Mutex
	status ReloadStatus
}

// NewHolder loads the configuration at path; an invalid configuration is an error.
func NewHolder(path string, now func() time.Time) (*Holder, error) {
	h := &Holder{path: path, now: now}
	// Hash before loading, as Reload does: a file replaced in between then has a newer revision than the one
	// recorded, so the next check reloads it instead of skipping it.
	rev, err := revision(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	h.cfg.Store(cfg)
	h.status = ReloadStatus{Revision: rev, LoadedAt: now()}
	return h, nil
}

// Fixed holds cfg without a file; Reload never changes it. It serves tests and callers that build a Config in code.
func Fixed(cfg *Config) *Holder {
	h := &Holder{}
	h.cfg.Store(cfg)
	return h
}

// Current returns the configuration in force.
func (h *Holder) Current() *Config {
	return h.cfg.Load()
}

// Status returns the revision in force and the last reload error.
func (h *Holder) Status() ReloadStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.status
}

// Reload loads the file when its revision differs from the one in force. A valid candidate replaces the
// configuration; an invalid one is recorded in Status and returned, and the configuration in force stays.
func (h *Holder) Reload() (changed bool, err error) {
	if h.path == "" {
		return false, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rev, err := revision(h.path)
	if err != nil {
		h.status.Error = err.Error()
		return false, err
	}
	if rev == h.status.Revision {
		h.status.Error = ""
		return false, nil
	}
	cfg, err := Load(h.path)
	if err != nil {
		h.status.Error = err.Error()
		return false, err
	}
	h.cfg.Store(cfg)
	h.status = ReloadStatus{Revision: rev, LoadedAt: h.now()}
	return true, nil
}

// revision hashes the configuration file. A configuration that does not parse is hashed too; Load reports why it
// is invalid.
func revision(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// snapshotKey carries a request's configuration snapshot in its context.
type snapshotKey struct{}

// WithSnapshot pins cfg as the configuration for everything that runs under ctx, so one request never mixes two
// configurations when a reload lands mid-request.
func WithSnapshot(ctx context.Context, cfg *Config) context.Context {
	return context.WithValue(ctx, snapshotKey{}, cfg)
}

// For returns the snapshot pinned in ctx, or the configuration in force when there is none.
func For(ctx context.Context, h *Holder) *Config {
	if cfg, ok := ctx.Value(snapshotKey{}).(*Config); ok {
		return cfg
	}
	return h.Current()
}
