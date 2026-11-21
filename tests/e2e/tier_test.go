//go:build integration

package e2e

import (
	"context"
	"net/http"
	"testing"
)

// The e2e key is user1@example.com, a basic-tier user: glm-5.3 (default) and three smaller models.
func TestCatalogFollowsTier(t *testing.T) {
	resp, err := client(t).ListModelsWithResponse(context.Background())
	if err != nil || resp.JSON200 == nil {
		t.Fatalf("models: %v %v", resp, err)
	}
	out := resp.JSON200
	if out.Default == nil || *out.Default != "glm-5.3" || len(out.Models) != 4 || out.Models[0].Name != "glm-5.3" || out.Models[0].InputTokenLimit != 128000 {
		t.Fatalf("catalog = %+v", out)
	}
}

func TestModelOutsideTierIsRefused(t *testing.T) {
	code, _ := api(t, http.MethodPost, "/v1/chat/completions", `{"model":"opus-5.5","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"X-Wrench-Session-Id": session(t)})
	if code != http.StatusForbidden {
		t.Fatalf("opus-5.5 for a basic user: %d", code)
	}
}
