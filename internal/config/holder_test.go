package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes content as config.yaml into dir.
func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestHolder(t *testing.T) (*Holder, string, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	path := writeConfig(t, t.TempDir(), validYAML)
	h, err := NewHolder(path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	return h, path, &clock
}

func TestHolderReload(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		changed bool
		errText string
		check   func(t *testing.T, before, after *Config)
	}{
		{name: "unchanged", config: validYAML},
		{name: "valid edit", config: strings.Replace(validYAML, "    tier: premium\n", "", 1), changed: true,
			check: func(t *testing.T, before, after *Config) {
				if u, _ := after.User("bob@example.com"); u.Tier != "basic" {
					t.Errorf("bob tier = %q", u.Tier)
				}
			}},
		{name: "invalid edit", config: strings.Replace(validYAML, "tier: premium", "tier: gold", 1),
			errText: `unknown tier "gold"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, path, clock := newTestHolder(t)
			before, status := h.Current(), h.Status()
			*clock = clock.Add(time.Minute)
			writeConfig(t, filepath.Dir(path), tt.config)
			changed, err := h.Reload()
			if changed != tt.changed || (tt.errText == "") != (err == nil) {
				t.Fatalf("Reload = %v, %v", changed, err)
			}
			after := h.Status()
			switch {
			case tt.errText != "":
				if h.Current() != before || after.Revision != status.Revision || !strings.Contains(after.Error, tt.errText) {
					t.Fatalf("after invalid edit: %+v", after)
				}
			case tt.changed:
				if h.Current() == before || after.Revision == status.Revision || !after.LoadedAt.Equal(*clock) || after.Error != "" {
					t.Fatalf("after change: %+v", after)
				}
				tt.check(t, before, h.Current())
			default:
				if h.Current() != before || after != status {
					t.Fatalf("unchanged file reloaded: %+v", after)
				}
			}
		})
	}
}

func TestHolderRecoversFromInvalidEdit(t *testing.T) {
	h, path, _ := newTestHolder(t)
	writeConfig(t, filepath.Dir(path), "models: [")
	if _, err := h.Reload(); err == nil || h.Status().Error == "" {
		t.Fatalf("broken file accepted: %+v", h.Status())
	}
	writeConfig(t, filepath.Dir(path), strings.Replace(validYAML, "    tier: premium\n", "", 1))
	if changed, err := h.Reload(); !changed || err != nil || h.Status().Error != "" {
		t.Fatalf("recovery = %v, %v, %+v", changed, err, h.Status())
	}
}

func TestNewHolderRejectsInvalid(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "models: [")
	if _, err := NewHolder(path, time.Now); err == nil {
		t.Fatal("invalid initial configuration accepted")
	}
}

func TestFixedNeverReloads(t *testing.T) {
	cfg, err := load(t, validYAML)
	if err != nil {
		t.Fatal(err)
	}
	h := Fixed(cfg)
	if changed, err := h.Reload(); changed || err != nil || h.Current() != cfg {
		t.Fatalf("Fixed reloaded: %v, %v", changed, err)
	}
}

func TestForPrefersTheRequestSnapshot(t *testing.T) {
	h, _, _ := newTestHolder(t)
	if For(context.Background(), h) != h.Current() {
		t.Fatal("without a snapshot For must return the configuration in force")
	}
	other := &Config{}
	ctx := WithSnapshot(context.Background(), other)
	if For(ctx, h) != other {
		t.Fatal("For ignored the request snapshot")
	}
}
