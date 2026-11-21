//go:build integration

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/ordo-systems/wrench/tests/internal/wrenchapi"
)

// sessionClient signs in through the browser flow as email and returns a client with that dashboard session.
func sessionClient(t *testing.T, email string) *wrenchapi.ClientWithResponses {
	t.Helper()
	resp, page := signIn(t, email)
	session := cookie(resp, sessionCookie)
	if resp.StatusCode != http.StatusFound || session == "" {
		t.Fatalf("sign-in as %s: %d %s", email, resp.StatusCode, page)
	}
	return client(t, withCookie(sessionCookie, session))
}

// TestRolesSeeTheirScope checks the role boundary on a running server: a user reads only their own data and
// status, an administrator also the system and everyone's lists.
func TestRolesSeeTheirScope(t *testing.T) {
	ctx := context.Background()
	everyone := wrenchapi.User("*")
	for _, tc := range []struct {
		email       string
		system, all int
	}{
		{"user1@example.com", http.StatusForbidden, http.StatusForbidden},
		{"user3@example.com", http.StatusOK, http.StatusOK},
	} {
		t.Run(tc.email, func(t *testing.T) {
			c := sessionClient(t, tc.email)
			status, err := c.GetStatusWithResponse(ctx)
			if err != nil || status.StatusCode() != http.StatusOK || status.JSON200 == nil {
				t.Fatalf("/api/status: %v %v", status, err)
			}
			system, err := c.GetSystemWithResponse(ctx)
			if err != nil || system.StatusCode() != tc.system {
				t.Errorf("/api/system: %d %v, want %d", system.StatusCode(), err, tc.system)
			}
			all, err := c.ListRequestsWithResponse(ctx, &wrenchapi.ListRequestsParams{User: &everyone})
			if err != nil || all.StatusCode() != tc.all {
				t.Errorf("/api/requests?user=*: %d %v, want %d", all.StatusCode(), err, tc.all)
			}
			own, err := c.ListRequestsWithResponse(ctx, &wrenchapi.ListRequestsParams{})
			if err != nil || own.JSON200 == nil {
				t.Fatalf("/api/requests: %v %v", own, err)
			}
			for _, a := range own.JSON200.Attempts {
				if a.UserEmail != tc.email {
					t.Errorf("own requests include %s", a.UserEmail)
				}
			}
		})
	}
}
