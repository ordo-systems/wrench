//go:build integration

package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/tests/login"
)

// TestMain signs user1 in through the device login, as opencode would, before the tests run against the stack.
func TestMain(m *testing.M) {
	wrench, gitlab := os.Getenv("WRENCH_E2E_URL"), os.Getenv("WRENCH_E2E_GITLAB_URL")
	if wrench != "" && gitlab != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		key, err := login.DeviceKey(ctx, wrench, gitlab, "user1@example.com")
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e sign-in:", err)
			os.Exit(1)
		}
		token = key
	}
	os.Exit(m.Run())
}
