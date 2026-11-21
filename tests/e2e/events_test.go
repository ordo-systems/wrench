//go:build integration

package e2e

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestEventsAnnounceUsage opens the change stream with the e2e key, sends one chat request and expects the
// stream to announce `usage` within 10 seconds.
func TestEventsAnnounceUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL(t, "WRENCH_E2E_URL")+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("events status %d: %s", resp.StatusCode, b)
	}

	found := make(chan struct{})
	go func() {
		defer close(found)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "event: usage" {
				return
			}
		}
	}()

	r := chat(ctx, t, session(t), streamBody("events"))
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()

	select {
	case <-found:
		if ctx.Err() != nil {
			t.Fatal("no usage event within 10 s")
		}
	case <-ctx.Done():
		t.Fatal("no usage event within 10 s")
	}
}
