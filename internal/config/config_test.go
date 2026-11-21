package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validYAML is a complete configuration: the spec example without month.
const validYAML = `models:
  opus-5.5:
    upstream_id: anthropic/claude-opus-5.5
    reasoning_effort: low
  glm-5.3:
    upstream_id: z-ai/glm-5.3
    reasoning_effort: low
  unused:
    upstream_id: x/unused
budget:
  currency: USD
  timezone: UTC
  global_limits:
    5_hours: 8.00
    day: 16.00
    week: 80.00
    month: 200.00
    warn_at_percent: [80, 95]
tiers:
  basic:
    default: true
    budget: {5_hours: 0.50, day: 1.00, week: 5.00, month: 20.00}
    models:
      - name: glm-5.3
        default: true
  premium:
    budget: {5_hours: 1.00, day: 5.00, week: 15.00, month: 50.00}
    models:
      - name: opus-5.5
        default: true
      - name: glm-5.3
users:
  - email: " Alice@Example.com "
  - email: bob@example.com
    role: admin
    tier: premium
alerts:
  window: 15m
  repeat_after: 1h
  low_balance: {threshold: 5.00}
  api_errors: {threshold: 5.5}
  llm_first_token: {threshold_p95: 30s, min_attempts: 3}
`

func load(t *testing.T, content string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadValid(t *testing.T) {
	cfg, err := load(t, validYAML)
	if err != nil {
		t.Fatal(err)
	}
	alice, ok := cfg.User("alice@example.com")
	if !ok || alice.Role != RoleUser || alice.Tier != "basic" {
		t.Errorf("alice = %+v, %v", alice, ok)
	}
	if bob, _ := cfg.User("BOB@example.com"); bob.Role != RoleAdmin || bob.Tier != "premium" {
		t.Errorf("bob = %+v", bob)
	}
	if got := cfg.UserLimits(alice); got.Day.String() != "1" {
		t.Errorf("alice limits = %+v", got)
	}
	if cfg.Budget.GlobalLimits.Week.String() != "80" || cfg.Budget.GlobalLimits.Month.String() != "200" || len(cfg.Budget.WarnAtPercent) != 2 {
		t.Errorf("budget = %+v", cfg.Budget)
	}
	if m, ok := cfg.Model("opus-5.5"); !ok || m.UpstreamID != "anthropic/claude-opus-5.5" || m.ReasoningEffort != "low" {
		t.Errorf("opus = %+v, %v", m, ok)
	}
	if got := cfg.UnreferencedModels(); len(got) != 1 || got[0] != "unused" {
		t.Errorf("unreferenced = %v", got)
	}
	a := cfg.Alerts
	if a.APIErrors.Threshold != 5.5 || a.APIErrors.MinAttempts != 5 || a.FirstToken.MinAttempts != 3 ||
		a.FirstToken.ThresholdP95 != 30*time.Second || a.LowBalance == nil {
		t.Errorf("alerts = %+v", a)
	}
}

func TestTierAccess(t *testing.T) {
	cfg, err := load(t, validYAML)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		check func() bool
	}{
		{"basic tier model", func() bool { _, ok := cfg.TierModel("alice@example.com", "glm-5.3"); return ok }},
		{"basic lacks premium model", func() bool { _, ok := cfg.TierModel("alice@example.com", "opus-5.5"); return !ok }},
		{"premium tier model", func() bool { _, ok := cfg.TierModel("bob@example.com", "opus-5.5"); return ok }},
		{"configured model outside every tier", func() bool { _, ok := cfg.TierModel("bob@example.com", "unused"); return !ok }},
		{"unknown user", func() bool { _, ok := cfg.TierModel("nobody@example.com", "glm-5.3"); return !ok }},
		{"tier default model", func() bool { t, _ := cfg.TierOf("bob@example.com"); return t.DefaultModel() == "opus-5.5" }},
	}
	for _, tt := range tests {
		if !tt.check() {
			t.Errorf("%s: failed", tt.name)
		}
	}
}

func TestLoadDefaultsAndOptionalSections(t *testing.T) {
	noAlerts := strings.Replace(validYAML, validYAML[strings.Index(validYAML, "alerts:"):], "", 1)
	cfg, err := load(t, noAlerts)
	if err != nil || cfg.Alerts != nil {
		t.Fatalf("without alerts: %+v, %v", cfg, err)
	}
	partial := strings.Replace(validYAML, "  low_balance: {threshold: 5.00}\n", "", 1)
	cfg, err = load(t, partial)
	if err != nil || cfg.Alerts.LowBalance != nil {
		t.Fatalf("without low_balance: %+v, %v", cfg, err)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name, from, to, want string
	}{
		{"unknown tier model", "      - name: opus-5.5\n", "      - name: opus-9\n", `tiers.premium.models: unknown model "opus-9"`},
		{"no default tier", "    default: true\n    budget: {5_hours: 0.50", "    budget: {5_hours: 0.50", "exactly one tier must have default: true, found 0"},
		{"two default tiers", "  premium:\n", "  premium:\n    default: true\n", "exactly one tier must have default: true, found 2"},
		{"no default model", "      - name: opus-5.5\n        default: true\n", "      - name: opus-5.5\n", "tiers.premium.models must have exactly one default model, found 0"},
		{"two default models", "      - name: glm-5.3\nusers", "      - name: glm-5.3\n        default: true\nusers", "tiers.premium.models must have exactly one default model, found 2"},
		{"unknown user tier", "    tier: premium", "    tier: gold", `bob@example.com has unknown tier "gold"`},
		{"duplicate email", `  - email: bob@example.com`, "  - email: alice@example.com", `duplicate user "alice@example.com"`},
		{"bad role", "    role: admin", "    role: root", `unknown role "root"`},
		{"negative amount", "day: 1.00, week: 5.00", "day: -1.00, week: 5.00", "negative"},
		{"missing window", "5_hours: 0.50, day: 1.00, week: 5.00", "5_hours: 0.50, day: 1.00", "budget window week is required"},
		{"warn not ascending", "[80, 95]", "[95, 80]", "warn_at_percent must be strictly ascending"},
		{"alert duration", "window: 15m", "window: 0s", "alerts.window must be positive"},
		{"min attempts zero", "min_attempts: 3", "min_attempts: -1", "alerts.llm_first_token.min_attempts must be positive"},
		{"error threshold", "threshold: 5.5", "threshold: 0", "alerts.api_errors.threshold must be a percentage"},
		{"global month missing", "    month: 200.00\n", "", "budget window month is required"},
		{"tier month missing", "week: 5.00, month: 20.00}", "week: 5.00}", "budget window month is required"},
		{"unknown window", "    month: 200.00\n", "    month: 200.00\n    year: 1\n", `unknown budget window "year"`},
		{"unknown key", "budget:\n", "budget:\n  extra: 1\n", "field extra not found"},
		{"bad reasoning", "reasoning_effort: low\n  glm-5.3:", "reasoning_effort: max\n  glm-5.3:", `reasoning_effort "max" is not supported`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(validYAML, tt.from) {
				t.Fatalf("fixture lacks %q", tt.from)
			}
			_, err := load(t, strings.Replace(validYAML, tt.from, tt.to, 1))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
