package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// watchHolder starts Watch on a fresh holder and returns a channel that receives one value per applied change.
func watchHolder(t *testing.T, every time.Duration, events bool) (*Holder, string, chan struct{}) {
	t.Helper()
	h, path, _ := newTestHolder(t)
	changes := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watch(ctx, h, path, every, func() { changes <- struct{}{} }, events)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Watch did not stop after cancellation")
		}
	})
	time.Sleep(50 * time.Millisecond) // let the watch start before the edit
	return h, path, changes
}

func waitChange(t *testing.T, changes chan struct{}) {
	t.Helper()
	select {
	case <-changes:
	case <-time.After(2 * time.Second):
		t.Fatal("no reload")
	}
}

var editedYAML = strings.Replace(validYAML, "    tier: premium\n", "", 1)

func TestWatchReloads(t *testing.T) {
	tests := []struct {
		name   string
		every  time.Duration
		events bool
		edit   func(t *testing.T, dir string)
	}{
		{"in-place write", time.Hour, true, func(t *testing.T, dir string) { writeConfig(t, dir, editedYAML) }},
		{"rename replacement", time.Hour, true, func(t *testing.T, dir string) {
			tmp := filepath.Join(dir, ".config.yaml.tmp")
			if err := os.WriteFile(tmp, []byte(editedYAML), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(tmp, filepath.Join(dir, "config.yaml")); err != nil {
				t.Fatal(err)
			}
		}},
		{"polling without events", 20 * time.Millisecond, false, func(t *testing.T, dir string) { writeConfig(t, dir, editedYAML) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, path, changes := watchHolder(t, tt.every, tt.events)
			before := h.Status().Revision
			tt.edit(t, filepath.Dir(path))
			waitChange(t, changes)
			if h.Status().Revision == before {
				t.Fatal("revision unchanged")
			}
		})
	}
}

func TestWatchKeepsConfigurationOnInvalidEdit(t *testing.T) {
	h, path, changes := watchHolder(t, 20*time.Millisecond, true)
	before := h.Current()
	writeConfig(t, filepath.Dir(path), "models: [")
	deadline := time.Now().Add(2 * time.Second)
	for h.Status().Error == "" {
		if time.Now().After(deadline) {
			t.Fatal("invalid edit not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-changes:
		t.Fatal("invalid edit applied")
	default:
	}
	if h.Current() != before {
		t.Fatal("configuration replaced")
	}
}

// An in-place save truncates the file before writing it; the empty file in between must not be reported.
func TestWatchWaitsForTheSaveToFinish(t *testing.T) {
	h, path, changes := watchHolder(t, time.Hour, true)
	if err := os.WriteFile(path, []byte(validYAML[:len(validYAML)/2]), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	writeConfig(t, filepath.Dir(path), editedYAML)
	deadline := time.After(2 * time.Second)
	for {
		if e := h.Status().Error; e != "" {
			t.Fatalf("half-written file reported: %s", e)
		}
		select {
		case <-changes:
			return
		case <-deadline:
			t.Fatal("no reload")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
