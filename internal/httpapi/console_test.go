package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// consoleGet reads a Console endpoint with a device key and returns the status, a header and the decoded body.
func (b *budgetStack) consoleGet(path, token, header string, out any) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, b.server.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, out); err != nil {
			b.t.Fatalf("%s: %v: %s", path, err, raw)
		}
	}
	return resp.StatusCode, resp.Header.Get(header)
}

func TestConsoleIdentity(t *testing.T) {
	b := newBudgetStack(t)
	var user struct{ ID, Email string }
	if code, _ := b.consoleGet("/api/user", "alice", "", &user); code != http.StatusOK || user.Email != "alice@example.com" || user.ID == "" {
		t.Fatalf("user: %d %+v", code, user)
	}
	var orgs []struct{ ID, Name string }
	if code, _ := b.consoleGet("/api/orgs", "alice", "", &orgs); code != http.StatusOK || len(orgs) != 1 || orgs[0].ID != "wrench" || orgs[0].Name != "Acme" {
		t.Fatalf("orgs: %d %+v", code, orgs)
	}
}

func TestConsoleConfig(t *testing.T) {
	b := newBudgetStack(t)
	var catalog struct {
		Models []struct {
			Name            string `json:"name"`
			InputTokenLimit int    `json:"input_token_limit"`
			MaxOutputTokens int    `json:"max_output_tokens"`
		} `json:"models"`
	}
	if code, _ := b.consoleGet("/api/models", "alice", "", &catalog); code != http.StatusOK || len(catalog.Models) == 0 {
		t.Fatalf("models: %d %+v", code, catalog)
	}
	var cfg struct {
		Providers map[string]struct {
			Name     string `json:"name"`
			Package  string `json:"package"`
			Settings struct {
				BaseURL string `json:"baseURL"`
			} `json:"settings"`
			Models map[string]struct {
				Name  string `json:"name"`
				Limit struct{ Context, Output int }
			} `json:"models"`
		} `json:"providers"`
		Policies []struct{ Action, Resource, Effect string } `json:"policies"`
	}
	code, lists := b.consoleGet("/api/v2/config", "alice", "x-opencode-instructions-skills", &cfg)
	if code != http.StatusOK || lists == "" {
		t.Fatalf("config: %d, lists header %q", code, lists)
	}
	p, ok := cfg.Providers["wrench"]
	if !ok || len(cfg.Providers) != 1 || p.Name != "Wrench" || p.Package != "@opencode/ai/providers/openai-compatible" ||
		p.Settings.BaseURL != "https://wrench.example.com/v1" || len(p.Models) != len(catalog.Models) {
		t.Fatalf("provider = %+v", cfg.Providers)
	}
	for _, m := range catalog.Models {
		got := p.Models[m.Name]
		if got.Name != m.Name || got.Limit.Context != m.InputTokenLimit || got.Limit.Output != m.MaxOutputTokens {
			t.Errorf("model %s = %+v, catalog %+v", m.Name, got, m)
		}
	}
	want := []struct{ Action, Resource, Effect string }{{"provider.use", "*", "deny"}, {"provider.use", "wrench", "allow"}}
	if len(cfg.Policies) != 2 || cfg.Policies[0] != want[0] || cfg.Policies[1] != want[1] {
		t.Fatalf("policies = %+v", cfg.Policies)
	}
}

func TestConsoleInstructionsAndSkills(t *testing.T) {
	b := newBudgetStack(t)
	var in struct {
		Instructions []any `json:"instructions"`
	}
	if code, _ := b.consoleGet("/api/v2/instructions", "alice", "", &in); code != http.StatusOK || in.Instructions == nil || len(in.Instructions) != 0 {
		t.Fatalf("instructions: %d %+v", code, in)
	}
	var skills struct {
		Skills []any `json:"skills"`
	}
	if code, _ := b.consoleGet("/api/v2/skills", "alice", "", &skills); code != http.StatusOK || skills.Skills == nil || len(skills.Skills) != 0 {
		t.Fatalf("skills: %d %+v", code, skills)
	}
}

type consoleRule struct {
	RuleID       string  `json:"ruleID"`
	LimitDollars float64 `json:"limitDollars"`
	SpentDollars float64 `json:"spentDollars"`
	Scope        string  `json:"scope"`
	Per          string  `json:"per"`
	Coverage     string  `json:"coverage"`
	Models       []any   `json:"models"`
	Vendors      []any   `json:"vendors"`
	Picked       bool    `json:"picked"`
	Shared       bool    `json:"shared"`
	ResetsAt     string  `json:"resetsAt"`
}

func TestConsoleBudgets(t *testing.T) {
	b := newBudgetStack(t)
	if r := b.chat("alice"); r.status != http.StatusOK {
		t.Fatalf("chat: %+v", r)
	}
	var budget struct {
		Windows []struct {
			Scope    string `json:"scope"`
			Window   string `json:"window"`
			Spent    string `json:"spent"`
			Limit    string `json:"limit"`
			ResetsAt string `json:"resets_at"`
		} `json:"windows"`
	}
	if code := get(t, b.server, "/api/budget", "alice", &budget); code != http.StatusOK || len(budget.Windows) != 4 {
		t.Fatalf("budget: %d %+v", code, budget)
	}
	var rules []consoleRule
	if code, _ := b.consoleGet("/api/policies/current-budgets", "alice", "", &rules); code != http.StatusOK || len(rules) != 4 {
		t.Fatalf("rules: %d %+v", code, rules)
	}
	for i, w := range budget.Windows {
		r := rules[i]
		wantReset, _ := time.Parse(time.RFC3339, w.ResetsAt)
		gotReset, err := time.Parse(time.RFC3339, r.ResetsAt)
		if r.RuleID != "user-"+w.Window || r.Scope != "each" || r.Shared || r.Per != "set" || r.Coverage != "all" ||
			r.Picked || r.Models == nil || len(r.Models) != 0 || r.Vendors == nil || len(r.Vendors) != 0 ||
			err != nil || !gotReset.Equal(wantReset) || !sameDollars(r.LimitDollars, w.Limit) || !sameDollars(r.SpentDollars, w.Spent) {
			t.Errorf("rule %d = %+v, window %+v", i, r, w)
		}
	}
	// Only administrators see the shared windows, which reveal everyone's spend.
	var adminRules []consoleRule
	if code, _ := b.consoleGet("/api/policies/current-budgets", "admin", "", &adminRules); code != http.StatusOK || len(adminRules) != 8 {
		t.Fatalf("admin rules: %d %d", code, len(adminRules))
	}
	shared := 0
	for _, r := range adminRules {
		if r.Scope == "combined" && r.Shared {
			shared++
		}
	}
	if shared != 4 {
		t.Fatalf("shared rules = %d", shared)
	}
}

func sameDollars(got float64, want string) bool {
	var w float64
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		return false
	}
	return got == w
}

func TestConsoleRefusals(t *testing.T) {
	b := newBudgetStack(t)
	for _, path := range []string{"/api/user", "/api/orgs", "/api/v2/config", "/api/v2/instructions", "/api/v2/skills",
		"/api/policies/current-budgets"} {
		if code, _ := b.consoleGet(path, "", "", nil); code != http.StatusUnauthorized {
			t.Errorf("%s without a key: %d", path, code)
		}
	}
	if code, _ := b.consoleGet("/zen/go/v1/usage", "alice", "", nil); code != http.StatusForbidden {
		t.Errorf("OpenCode Go usage: %d", code)
	}
}
