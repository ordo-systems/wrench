package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// reasoningEfforts are the values accepted for models.<name>.reasoning_effort; empty omits the parameter.
var reasoningEfforts = map[string]bool{"": true, "none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true}

// name is the form of model and tier names.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validate rejects configurations that could silently remove access or accounting limits.
// It expects normalize to have run.
func validate(c Config) error {
	return errors.Join(validateModels(c.Models), validateBudget(c.Budget), validateTiers(c), validateUsers(c),
		validateAlerts(c.Alerts))
}

func validateModels(models []Model) error {
	if len(models) == 0 {
		return errors.New("models must not be empty")
	}
	var errs []error
	for _, m := range models {
		if !name.MatchString(m.Name) {
			errs = append(errs, fmt.Errorf("models: invalid model name %q", m.Name))
		}
		if m.UpstreamID == "" {
			errs = append(errs, fmt.Errorf("models.%s.upstream_id must not be empty", m.Name))
		}
		if !reasoningEfforts[m.ReasoningEffort] {
			errs = append(errs, fmt.Errorf("models.%s.reasoning_effort %q is not supported", m.Name, m.ReasoningEffort))
		}
	}
	return errors.Join(errs...)
}

func validateBudget(b Budget) error {
	var errs []error
	if b.Currency != "USD" {
		errs = append(errs, fmt.Errorf("budget.currency must be USD, got %q", b.Currency))
	}
	if b.Timezone != "UTC" {
		errs = append(errs, fmt.Errorf("budget.timezone must be UTC, got %q", b.Timezone))
	}
	if !b.GlobalLimits.present {
		errs = append(errs, errors.New("budget.global_limits is required"))
	}
	prev := 0
	for _, p := range b.WarnAtPercent {
		if p <= prev || p >= 100 {
			errs = append(errs, fmt.Errorf("budget.global_limits.warn_at_percent must be strictly ascending within 1..99, got %v", b.WarnAtPercent))
			break
		}
		prev = p
	}
	return errors.Join(errs...)
}

func validateTiers(c Config) error {
	if len(c.Tiers) == 0 {
		return errors.New("tiers must not be empty")
	}
	var errs []error
	defaults := 0
	for _, t := range c.Tiers {
		if t.Default {
			defaults++
		}
		errs = append(errs, validateTier(c, t))
	}
	if defaults != 1 {
		errs = append(errs, fmt.Errorf("exactly one tier must have default: true, found %d", defaults))
	}
	return errors.Join(errs...)
}

func validateTier(c Config, t Tier) error {
	var errs []error
	path := "tiers." + t.Name
	if !name.MatchString(t.Name) {
		errs = append(errs, fmt.Errorf("tiers: invalid tier name %q", t.Name))
	}
	if !t.Budget.present {
		errs = append(errs, fmt.Errorf("%s.budget is required", path))
	}
	if len(t.Models) == 0 {
		errs = append(errs, fmt.Errorf("%s.models must not be empty", path))
	}
	defaults, seen := 0, map[string]bool{}
	for _, m := range t.Models {
		if m.Default {
			defaults++
		}
		if seen[m.Name] {
			errs = append(errs, fmt.Errorf("%s.models: duplicate model %q", path, m.Name))
		}
		seen[m.Name] = true
		if _, ok := c.Model(m.Name); !ok {
			errs = append(errs, fmt.Errorf("%s.models: unknown model %q", path, m.Name))
		}
	}
	if len(t.Models) > 0 && defaults != 1 {
		errs = append(errs, fmt.Errorf("%s.models must have exactly one default model, found %d", path, defaults))
	}
	return errors.Join(errs...)
}

func validateUsers(c Config) error {
	if len(c.Users) == 0 {
		return errors.New("users must not be empty")
	}
	var errs []error
	seen := map[string]bool{}
	for _, u := range c.Users {
		if !strings.Contains(u.Email, "@") || strings.ContainsAny(u.Email, " \t") {
			errs = append(errs, fmt.Errorf("users: invalid email %q", u.Email))
		}
		if seen[u.Email] {
			errs = append(errs, fmt.Errorf("users: duplicate user %q", u.Email))
		}
		seen[u.Email] = true
		if u.Role != RoleUser && u.Role != RoleAdmin {
			errs = append(errs, fmt.Errorf("users: %s has unknown role %q", u.Email, u.Role))
		}
		if _, ok := c.Tier(u.Tier); !ok && u.Tier != "" {
			errs = append(errs, fmt.Errorf("users: %s has unknown tier %q", u.Email, u.Tier))
		}
	}
	return errors.Join(errs...)
}

func validateAlerts(a *Alerts) error {
	if a == nil {
		return nil
	}
	durations := map[string]time.Duration{"alerts.window": a.Window, "alerts.repeat_after": a.RepeatAfter}
	var errs []error
	if e := a.APIErrors; e != nil {
		if e.Threshold <= 0 || e.Threshold > 100 {
			errs = append(errs, errors.New("alerts.api_errors.threshold must be a percentage within (0, 100]"))
		}
		if e.MinAttempts < 1 {
			errs = append(errs, errors.New("alerts.api_errors.min_attempts must be positive"))
		}
	}
	if f := a.FirstToken; f != nil {
		durations["alerts.llm_first_token.threshold_p95"] = f.ThresholdP95
		if f.MinAttempts < 1 {
			errs = append(errs, errors.New("alerts.llm_first_token.min_attempts must be positive"))
		}
	}
	for path, d := range durations {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", path))
		}
	}
	slices.SortFunc(errs, func(x, y error) int { return strings.Compare(x.Error(), y.Error()) })
	return errors.Join(errs...)
}
