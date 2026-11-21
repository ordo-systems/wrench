//go:build integration

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The Console API answers opencode with the demo configuration: the e2e key is user1, a basic-tier user.
func TestConsoleAgainstTheDemo(t *testing.T) {
	ctx := context.Background()
	c := client(t)
	cfg, err := c.GetConsoleConfigWithResponse(ctx)
	if err != nil || cfg.JSON200 == nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.HTTPResponse.Header.Get("x-opencode-instructions-skills") == "" {
		t.Error("config lacks the instructions header")
	}
	p, ok := cfg.JSON200.Providers["wrench"]
	if !ok || len(cfg.JSON200.Providers) != 1 || !strings.HasSuffix(p.Settings.BaseURL, "/v1") || len(p.Models) != 4 {
		t.Fatalf("providers = %+v", cfg.JSON200.Providers)
	}
	if pol := cfg.JSON200.Policies; len(pol) != 2 || pol[0].Resource != "*" || pol[0].Effect != "deny" ||
		pol[1].Resource != "wrench" || pol[1].Effect != "allow" {
		t.Fatalf("policies = %+v", pol)
	}

	in, err := c.ListConsoleInstructionsWithResponse(ctx)
	if err != nil || in.JSON200 == nil || len(in.JSON200.Instructions) != 0 {
		t.Fatalf("instructions: %v %+v", err, in.JSON200)
	}

	budgets, err := c.ListConsoleBudgetsWithResponse(ctx)
	if err != nil || budgets.JSON200 == nil || len(*budgets.JSON200) != 4 {
		t.Fatalf("budgets: %v %+v", err, budgets.JSON200)
	}
	for _, r := range *budgets.JSON200 {
		if r.Scope != "each" || r.Shared || r.LimitDollars <= 0 {
			t.Errorf("rule = %+v", r)
		}
	}

	orgs, err := c.ListConsoleOrgsWithResponse(ctx)
	if err != nil || orgs.JSON200 == nil || len(*orgs.JSON200) != 1 || (*orgs.JSON200)[0].Id != "wrench" {
		t.Fatalf("orgs: %v %+v", err, orgs.JSON200)
	}
	if code, _ := api(t, http.MethodGet, "/zen/go/v1/usage", "", nil); code != http.StatusForbidden {
		t.Fatalf("OpenCode Go usage: %d", code)
	}
}
