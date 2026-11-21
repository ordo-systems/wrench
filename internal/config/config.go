// Package config loads and validates the application policy from config.yaml: models, budgets, tiers, users,
// and alerts. Runtime settings and secrets live in package env.
// Loading is all-or-nothing: any error rejects the whole candidate configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ordo-systems/wrench/internal/money"
)

// Config mirrors config.yaml. Models and Tiers are sorted by name.
type Config struct {
	Models []Model
	Budget Budget
	Tiers  []Tier
	Users  []User
	// Alerts is nil when the section is omitted, which disables alert detection.
	Alerts *Alerts
}

// Model is one catalog entry. Token limits come from the OpenRouter catalog, not from the file.
// ReasoningEffort is empty when the model must not receive a reasoning parameter.
type Model struct {
	Name            string `json:"name"`
	UpstreamID      string `json:"upstream_id"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// Budget sets the currency, the accounting time zone and the installation-wide limits.
type Budget struct {
	Currency     string
	Timezone     string
	GlobalLimits Limits
	// WarnAtPercent are the usage thresholds that warn users and raise budget alerts.
	WarnAtPercent []int
}

// Tier is a plan users belong to: their budget and the models they may call.
type Tier struct {
	Name    string      `json:"name"`
	Default bool        `json:"default"`
	Budget  Limits      `json:"budget"`
	Models  []TierModel `json:"models"`
}

// TierModel is one model a tier's users may call.
type TierModel struct {
	Name    string `json:"name" yaml:"name"`
	Default bool   `json:"default" yaml:"default"`
}

// DefaultModel returns the name of the tier's default model.
func (t Tier) DefaultModel() string {
	for _, m := range t.Models {
		if m.Default {
			return m.Name
		}
	}
	return ""
}

// Role is a dashboard/API permission level. An omitted role means RoleUser.
type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// User is one allowlisted account, matched by verified OIDC email after normalization.
// An omitted tier means the default tier.
type User struct {
	Email string `yaml:"email"`
	Role  Role   `yaml:"role"`
	Tier  string `yaml:"tier"`
}

// Alerts sets alert thresholds. Window is the recent period for error-rate and latency checks;
// RepeatAfter limits repeated notifications about one continuing incident. A nil subsection disables its alert.
type Alerts struct {
	Window      time.Duration `yaml:"window" json:"window"`
	RepeatAfter time.Duration `yaml:"repeat_after" json:"repeat_after"`
	LowBalance  *LowBalance   `yaml:"low_balance" json:"low_balance,omitempty"`
	APIErrors   *APIErrors    `yaml:"api_errors" json:"api_errors,omitempty"`
	FirstToken  *FirstToken   `yaml:"llm_first_token" json:"llm_first_token,omitempty"`
}

// LowBalance alerts when the remaining credit limit of Wrench's OpenRouter key drops below Threshold.
type LowBalance struct {
	Threshold money.Amount `yaml:"threshold" json:"threshold"`
}

// APIErrors alerts when the percentage of failed requests in the window reaches Threshold, once at least
// MinAttempts requests finished.
type APIErrors struct {
	Threshold   float64 `yaml:"threshold" json:"threshold"`
	MinAttempts int     `yaml:"min_attempts" json:"min_attempts"`
}

// FirstToken alerts when the p95 time to first token exceeds ThresholdP95, once at least MinAttempts
// requests produced a first token.
type FirstToken struct {
	ThresholdP95 time.Duration `yaml:"threshold_p95" json:"threshold_p95"`
	MinAttempts  int           `yaml:"min_attempts" json:"min_attempts"`
}

// defaultMinAttempts applies to api_errors and llm_first_token when min_attempts is omitted.
const defaultMinAttempts = 5

// file is the YAML shape; Load converts it to Config.
type file struct {
	Models map[string]struct {
		UpstreamID      string `yaml:"upstream_id"`
		ReasoningEffort string `yaml:"reasoning_effort"`
	} `yaml:"models"`
	Budget struct {
		Currency     string       `yaml:"currency"`
		Timezone     string       `yaml:"timezone"`
		GlobalLimits globalLimits `yaml:"global_limits"`
	} `yaml:"budget"`
	Tiers map[string]struct {
		Default bool        `yaml:"default"`
		Budget  Limits      `yaml:"budget"`
		Models  []TierModel `yaml:"models"`
	} `yaml:"tiers"`
	Users  []User  `yaml:"users"`
	Alerts *Alerts `yaml:"alerts"`
}

// Load reads config.yaml, rejecting duplicate and unknown keys without interpolation, and validates the result
// as a whole. User emails, roles and tiers are normalized.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cfg := fromFile(f)
	normalize(&cfg)
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &cfg, nil
}

func fromFile(f file) Config {
	cfg := Config{Users: f.Users, Alerts: f.Alerts,
		Budget: Budget{Currency: f.Budget.Currency, Timezone: f.Budget.Timezone,
			GlobalLimits: f.Budget.GlobalLimits.Limits, WarnAtPercent: f.Budget.GlobalLimits.WarnAtPercent}}
	for name, m := range f.Models {
		cfg.Models = append(cfg.Models, Model{Name: name, UpstreamID: m.UpstreamID, ReasoningEffort: m.ReasoningEffort})
	}
	slices.SortFunc(cfg.Models, func(a, b Model) int { return strings.Compare(a.Name, b.Name) })
	for name, t := range f.Tiers {
		cfg.Tiers = append(cfg.Tiers, Tier{Name: name, Default: t.Default, Budget: t.Budget, Models: t.Models})
	}
	slices.SortFunc(cfg.Tiers, func(a, b Tier) int { return strings.Compare(a.Name, b.Name) })
	return cfg
}

// NormalizeEmail is the single email comparison form used by the allowlist.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func normalize(cfg *Config) {
	def := ""
	if t, ok := cfg.defaultTier(); ok {
		def = t.Name
	}
	for i := range cfg.Users {
		u := &cfg.Users[i]
		u.Email = NormalizeEmail(u.Email)
		if u.Role == "" {
			u.Role = RoleUser
		}
		if u.Tier == "" {
			u.Tier = def
		}
	}
	if a := cfg.Alerts; a != nil {
		if a.APIErrors != nil && a.APIErrors.MinAttempts == 0 {
			a.APIErrors.MinAttempts = defaultMinAttempts
		}
		if a.FirstToken != nil && a.FirstToken.MinAttempts == 0 {
			a.FirstToken.MinAttempts = defaultMinAttempts
		}
	}
}

// defaultTier returns the first tier marked default; validation ensures there is exactly one.
func (c *Config) defaultTier() (Tier, bool) {
	for _, t := range c.Tiers {
		if t.Default {
			return t, true
		}
	}
	return Tier{}, false
}

// User returns the allowlisted user for an email in any letter case.
func (c *Config) User(email string) (User, bool) {
	email = NormalizeEmail(email)
	for _, u := range c.Users {
		if u.Email == email {
			return u, true
		}
	}
	return User{}, false
}

// Tier returns a tier by name.
func (c *Config) Tier(name string) (Tier, bool) {
	for _, t := range c.Tiers {
		if t.Name == name {
			return t, true
		}
	}
	return Tier{}, false
}

// TierOf returns the tier of an allowlisted user; false when the email is not allowlisted.
func (c *Config) TierOf(email string) (Tier, bool) {
	u, ok := c.User(email)
	if !ok {
		return Tier{}, false
	}
	return c.Tier(u.Tier)
}

// Model returns a configured model by name.
func (c *Config) Model(name string) (Model, bool) {
	for _, m := range c.Models {
		if m.Name == name {
			return m, true
		}
	}
	return Model{}, false
}

// TierModel returns a model of the user's tier.
func (c *Config) TierModel(email, name string) (Model, bool) {
	t, ok := c.TierOf(email)
	if !ok || !slices.ContainsFunc(t.Models, func(m TierModel) bool { return m.Name == name }) {
		return Model{}, false
	}
	return c.Model(name)
}

// UnreferencedModels lists models no tier uses, for a startup warning.
func (c *Config) UnreferencedModels() []string {
	used := map[string]bool{}
	for _, t := range c.Tiers {
		for _, m := range t.Models {
			used[m.Name] = true
		}
	}
	var out []string
	for _, m := range c.Models {
		if !used[m.Name] {
			out = append(out, m.Name)
		}
	}
	return out
}
