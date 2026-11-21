// Package backup runs inside the backup container. Loop runs scheduled backups of the Wrench database
// and policy files to a local and an S3 restic repository and writes a status file that Wrench alerts on.
// Once runs one backup now; Restore restores a snapshot into an empty database.
package backup

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/ordo-systems/wrench/internal/backupstatus"
)

// New reads WRENCH_BACKUP_* settings and returns a Backuper that shells out to pg_dump, pg_restore and restic.
func New(lookup func(string) (string, bool)) (*Backuper, error) {
	cfg, err := loadConfig(lookup)
	if err != nil {
		return nil, err
	}
	return &Backuper{cfg: cfg, cmd: execCommander{}, now: func() time.Time { return time.Now().UTC() },
		client: &http.Client{Timeout: 10 * time.Second}, log: slog.Default()}, nil
}

func (b *Backuper) previous() backupstatus.Status {
	s, err := backupstatus.Read(b.cfg.statusFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		b.log.Warn("previous backup status unreadable; starting fresh", "err", err)
	}
	return s
}

// Once runs one backup and records its status; a failed backup is returned as an error.
func (b *Backuper) Once(ctx context.Context) error {
	s := b.run(ctx, b.previous())
	if err := backupstatus.Write(b.cfg.statusFile, s); err != nil {
		return err
	}
	if s.LastError != "" {
		return errors.New(s.LastError)
	}
	return nil
}

// Loop announces the next run at startup, so a fresh install is not reported missing, then runs on schedule.
func (b *Backuper) Loop(ctx context.Context) error {
	s := b.previous()
	b.schedule(&s, b.now())
	if err := backupstatus.Write(b.cfg.statusFile, s); err != nil {
		return err
	}
	for {
		wait := time.Until(s.NextRunAt)
		b.log.Info("next backup", "at", s.NextRunAt.Format(time.RFC3339))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		s = b.run(ctx, s)
		if s.LastError != "" {
			b.log.Error("backup failed", "err", s.LastError)
		} else {
			b.log.Info("backup completed")
		}
		if err := backupstatus.Write(b.cfg.statusFile, s); err != nil {
			b.log.Error("backup status not written", "err", err)
		}
	}
}
