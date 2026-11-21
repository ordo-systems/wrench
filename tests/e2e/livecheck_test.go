//go:build integration

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/tests/livecheck"
)

// The pilot's live check runs against the integration chain: every tier model through the fake OpenRouter, with
// costs confirmed in the journal.
func TestLiveCheckPassesAgainstTheChain(t *testing.T) {
	report, err := livecheck.Run(context.Background(), livecheck.Options{
		URL: baseURL(t, "WRENCH_E2E_URL"), Token: token,
		// The fake provider stalls on "slow" until the caller disconnects.
		CancelPrompt: "slow", Settle: 90 * time.Second, Session: session(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range report.Results {
		if !r.OK {
			t.Errorf("%s %s: %s", r.Model, r.Step, r.Detail)
		}
	}
	// The dev user's basic tier: four models x 3 calls.
	if len(report.Attempts) != 12 {
		t.Errorf("attempts = %d: %+v", len(report.Attempts), report.Attempts)
	}
}
