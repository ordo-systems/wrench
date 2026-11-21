// Package backupstatus is the contract between the backup container, which writes a status file after
// every run, and Wrench, which shows it and alerts on it. The backup container computes StaleAfter from its
// own schedule, so Wrench needs no knowledge of the schedule.
package backupstatus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Repository is the outcome of the last run for one restic repository.
type Repository struct {
	OK       bool   `json:"ok"`
	Snapshot string `json:"snapshot,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Status is the backup state. LastError is empty after a fully successful run. A run succeeds only when
// every repository succeeded; LastSuccessAt keeps the previous success after a failure.
type Status struct {
	LastRunAt     *time.Time            `json:"last_run_at"`
	LastSuccessAt *time.Time            `json:"last_success_at"`
	LastError     string                `json:"last_error"`
	NextRunAt     time.Time             `json:"next_run_at"`
	StaleAfter    time.Time             `json:"stale_after"`
	Repositories  map[string]Repository `json:"repositories"`
}

// Grace is how long after a scheduled run a missing success becomes an alert.
const Grace = 2 * time.Hour

// Read loads the status file; a missing file is fs.ErrNotExist.
func Read(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Status{}, err
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("backup status %s: %w", path, err)
	}
	return s, nil
}

// Write replaces the status file atomically, so readers never see a partial file.
func Write(path string, s Status) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".status-*")
	if err != nil {
		return err
	}
	// After a successful rename the temporary name is gone and removing it is a no-op.
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, werr := tmp.Write(append(data, '\n'))
	if werr == nil {
		werr = tmp.Chmod(0o644)
	}
	if err := errors.Join(werr, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Problem describes why the backup needs attention at now, or returns "" when it is healthy.
// A missing file means the backup container never reported.
func Problem(path string, now time.Time) (string, error) {
	s, err := Read(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "the backup container has not reported a status", nil
	case err != nil:
		return "", err
	case s.LastError != "":
		return "the last backup failed: " + s.LastError, nil
	case now.After(s.StaleAfter):
		last := "never"
		if s.LastSuccessAt != nil {
			last = s.LastSuccessAt.Format(time.RFC3339)
		}
		return fmt.Sprintf("no successful backup since %s; one was due by %s", last, s.StaleAfter.Format(time.RFC3339)), nil
	}
	return "", nil
}
