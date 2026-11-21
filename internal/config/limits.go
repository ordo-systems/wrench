package config

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ordo-systems/wrench/internal/money"
)

// Limits holds spending ceilings for the UTC budget windows.
// Zero is an exhausted budget, not unlimited access, so every window key is required.
type Limits struct {
	FiveHours money.Amount
	Day       money.Amount
	Week      money.Amount
	Month     money.Amount
	// present distinguishes a missing section from an all-zero one.
	present bool
}

// windows is a parsed window mapping; nil means the key is absent.
type windows struct {
	FiveHours *money.Amount
	Day       *money.Amount
	Week      *money.Amount
	Month     *money.Amount
}

// UnmarshalYAML requires a mapping with exactly the keys 5_hours, day, week and month.
func (l *Limits) UnmarshalYAML(n *yaml.Node) error {
	w, err := decodeWindows(n, nil)
	if err != nil {
		return err
	}
	return l.fill(n, w)
}

// fill requires every window.
func (l *Limits) fill(n *yaml.Node, w windows) error {
	var missing []error
	for _, win := range []struct {
		name  string
		value *money.Amount
	}{{"5_hours", w.FiveHours}, {"day", w.Day}, {"week", w.Week}, {"month", w.Month}} {
		if win.value == nil {
			missing = append(missing, fmt.Errorf("line %d: budget window %s is required", n.Line, win.name))
		}
	}
	if err := errors.Join(missing...); err != nil {
		return err
	}
	*l = Limits{FiveHours: *w.FiveHours, Day: *w.Day, Week: *w.Week, Month: *w.Month, present: true}
	return nil
}

// globalLimits is budget.global_limits: the four windows plus warn_at_percent.
type globalLimits struct {
	Limits
	WarnAtPercent []int
}

// UnmarshalYAML takes warn_at_percent out of the window mapping.
func (g *globalLimits) UnmarshalYAML(n *yaml.Node) error {
	var warn *yaml.Node
	w, err := decodeWindows(n, map[string]**yaml.Node{"warn_at_percent": &warn})
	if err != nil {
		return err
	}
	if err := g.fill(n, w); err != nil {
		return err
	}
	if warn != nil {
		return warn.Decode(&g.WarnAtPercent)
	}
	return nil
}

// decodeWindows parses a window mapping strictly, because custom unmarshalers bypass KnownFields. Keys in extra
// are not windows; their value nodes are handed back.
func decodeWindows(n *yaml.Node, extra map[string]**yaml.Node) (windows, error) {
	var w windows
	if n.Kind != yaml.MappingNode {
		return w, fmt.Errorf("line %d: budget limits must be a mapping", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if dst, ok := extra[key.Value]; ok {
			*dst = val
			continue
		}
		var dst **money.Amount
		switch key.Value {
		case "5_hours":
			dst = &w.FiveHours
		case "day":
			dst = &w.Day
		case "week":
			dst = &w.Week
		case "month":
			dst = &w.Month
		default:
			return w, fmt.Errorf("line %d: unknown budget window %q", key.Line, key.Value)
		}
		if *dst != nil {
			return w, fmt.Errorf("line %d: budget window %q already defined", key.Line, key.Value)
		}
		var a money.Amount
		if err := a.UnmarshalYAML(val); err != nil {
			return w, err
		}
		*dst = &a
	}
	return w, nil
}

// UserLimits returns a user's limits: their tier's budget.
func (c *Config) UserLimits(u User) Limits {
	t, _ := c.Tier(u.Tier)
	return t.Budget
}
