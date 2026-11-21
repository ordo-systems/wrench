//go:build integration

package e2e

// The end-to-end tests read Wrench only through its public API, with the client generated from the OpenAPI
// document; they never import server packages.

import (
	"context"
	"net/http"
	"testing"

	"github.com/ordo-systems/wrench/tests/internal/wrenchapi"
)

const (
	outcomeSuccess   = "success"
	outcomeError     = "error"
	outcomeCancelled = "cancelled"

	costPending   = "pending"
	costConfirmed = "confirmed"
)

// attempt is one journal row from GET /api/requests.
type attempt = wrenchapi.Attempt

// client calls the Wrench API with the e2e device key, or with the given request editors instead.
func client(t *testing.T, editors ...wrenchapi.RequestEditorFn) *wrenchapi.ClientWithResponses {
	t.Helper()
	opts := []wrenchapi.ClientOption{wrenchapi.Bearer(token)}
	if len(editors) > 0 {
		opts = nil
		for _, e := range editors {
			opts = append(opts, wrenchapi.WithRequestEditorFn(e))
		}
	}
	c, err := wrenchapi.NewClientWithResponses(baseURL(t, "WRENCH_E2E_URL"), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// withCookie authenticates a request with a dashboard session cookie.
func withCookie(name, value string) wrenchapi.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
		return nil
	}
}

// cost is an attempt's cost as the API encodes it, or <nil> while unknown.
func cost(a attempt) string {
	if a.Cost == nil {
		return "<nil>"
	}
	return *a.Cost
}
