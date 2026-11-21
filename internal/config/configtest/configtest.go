// Package configtest provides a small valid config.yaml for tests of packages that consume the configuration.
package configtest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
)

// YAML is a minimal valid configuration. dev@example.com is a basic-tier user, admin@example.com a premium admin.
// basic: model glm (default). premium: models big (default) and glm. qwen is configured but in no tier.
// Every amount is 1.
const YAML = `models:
  glm:
    upstream_id: z-ai/glm-5.3
    reasoning_effort: low
  qwen:
    upstream_id: qwen/qwen3-coder
  big:
    upstream_id: vendor/big
budget:
  currency: USD
  timezone: UTC
  global_limits: {5_hours: 1, day: 1, week: 1, month: 100}
tiers:
  basic:
    default: true
    budget: {5_hours: 1, day: 1, week: 1, month: 100}
    models:
      - {name: glm, default: true}
  premium:
    budget: {5_hours: 1, day: 1, week: 1, month: 100}
    models:
      - {name: big, default: true}
      - {name: glm}
users:
  - email: dev@example.com
  - email: admin@example.com
    role: admin
    tier: premium
`

// Holder writes content to a temporary config.yaml and returns a reloadable holder and the file path.
func Holder(t testing.TB, content string) (*config.Holder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := config.NewHolder(path, func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatal(err)
	}
	return h, path
}

// Load writes content to a temporary config.yaml and loads it, failing the test on error.
func Load(t testing.TB, content string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
