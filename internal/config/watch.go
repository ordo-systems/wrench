package config

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// settle is how long the directory must stay quiet after a file event before reloading, so one save (truncate,
// write, chmod, rename) is read once, finished.
const settle = 200 * time.Millisecond

// Watch reloads h shortly after files in the configuration directory stop changing and every period, until ctx
// ends. Events make reloads fast; the period covers lost events and mounts that deliver none. onChange runs after a
// valid change is applied. A rejected candidate is logged once per distinct error and kept in h.Status.
func Watch(ctx context.Context, h *Holder, path string, every time.Duration, onChange func()) {
	watch(ctx, h, path, every, onChange, true)
}

// watch is Watch with file events optional, so tests can exercise polling alone.
func watch(ctx context.Context, h *Holder, path string, every time.Duration, onChange func(), events bool) {
	var fileEvents <-chan fsnotify.Event
	var fileErrors <-chan error
	if events {
		w, err := fsnotify.NewWatcher()
		if err == nil {
			if err = w.Add(filepath.Dir(path)); err != nil {
				_ = w.Close()
			}
		}
		if err != nil {
			slog.WarnContext(ctx, "configuration file events unavailable; polling only", "err", err)
		} else {
			defer func() { _ = w.Close() }()
			fileEvents, fileErrors = w.Events, w.Errors
		}
	}
	t := time.NewTicker(every)
	defer t.Stop()
	quiet := time.NewTimer(settle)
	quiet.Stop()
	defer quiet.Stop()
	lastErr := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-quiet.C:
		case <-fileEvents:
			quiet.Reset(settle)
			continue
		case err := <-fileErrors:
			slog.WarnContext(ctx, "configuration file events failed", "err", err)
			continue
		}
		changed, err := h.Reload()
		switch {
		case err != nil && err.Error() != lastErr:
			lastErr = err.Error()
			slog.ErrorContext(ctx, "configuration rejected", "err", err)
		case err == nil:
			lastErr = ""
		}
		if changed && onChange != nil {
			onChange()
		}
	}
}
