//go:build integration

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/tests/internal/wrenchapi"
	"github.com/ordo-systems/wrench/tests/login"
)

// The device login opencode uses yields a device key, and its refresh token replaces that key.
func TestDeviceLoginAndRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	wrench, gitlab := baseURL(t, "WRENCH_E2E_URL"), baseURL(t, "WRENCH_E2E_GITLAB_URL")
	access, refresh, err := login.DeviceTokens(ctx, wrench, gitlab, "user2@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := apiAs(t, access, http.MethodGet, "/api/me", ""); code != http.StatusOK {
		t.Fatalf("me with the device key: %d", code)
	}
	api, err := wrenchapi.NewClientWithResponses(wrench)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := api.DeviceTokenWithResponse(ctx, wrenchapi.DeviceTokenRequest{GrantType: "refresh_token",
		ClientId: "opencode-cli", RefreshToken: &refresh})
	if err != nil || resp.JSON200 == nil {
		t.Fatalf("refresh: %v %s", err, resp.Body)
	}
	if code, _ := apiAs(t, resp.JSON200.AccessToken, http.MethodGet, "/api/me", ""); code != http.StatusOK {
		t.Fatalf("me with the refreshed key: %d", code)
	}
	if code, _ := apiAs(t, access, http.MethodGet, "/api/me", ""); code != http.StatusUnauthorized {
		t.Fatalf("me with the replaced key: %d", code)
	}
}
