package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
)

// sharedPolicy gives alice $0.001 per day; every fake call costs $0.0006.
const sharedPolicy = `models:
  glm:
    upstream_id: z-ai/glm-5.3
budget:
  currency: USD
  timezone: UTC
  global_limits: {5_hours: 1, day: 1, week: 1, month: 100}
tiers:
  basic:
    default: true
    budget: {5_hours: 1, day: 1, week: 1, month: 100}
    models:
      - {name: glm, default: true}
  tight:
    budget: {5_hours: 1, day: "0.001", week: 1, month: 100}
    models:
      - {name: glm, default: true}
users:
  - email: alice@example.com
    tier: tight
`

// Acceptance scenario 4: several device keys spend one user budget.
func TestKeysShareOneUserBudget(t *testing.T) {
	b := newBudgetStackWith(t, sharedPolicy, "k")
	ctx := context.Background()
	alice, err := b.store.BindIdentity(ctx, "alice@example.com", "https://gitlab.example.com", "1", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Real keys: the proxy records the authenticating key on the session.
	for _, tok := range []string{"alice-laptop", "alice-desktop"} {
		k, err := b.store.IssueClientKey(ctx, alice.ID, tok, tok, []byte(tok), time.Now(), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		budgetUsers[tok] = auth.Identity{Email: "alice@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true, KeyID: k.ID}
		t.Cleanup(func() { delete(budgetUsers, tok) })
	}
	if r := b.chatAs("alice-laptop", "laptop-session", `{"model":"glm","messages":[]}`); r.status != http.StatusOK {
		t.Fatalf("laptop: %+v", r)
	}
	// $0.0006 spent of $0.001: the desktop is admitted and brings the day to $0.0012.
	if r := b.chatAs("alice-desktop", "desktop-session", `{"model":"glm","messages":[]}`); r.status != http.StatusOK {
		t.Fatalf("desktop: %+v", r)
	}
	for _, c := range []struct{ token, session string }{{"alice-laptop", "laptop-session"}, {"alice-desktop", "desktop-session"}} {
		if r := b.chatAs(c.token, c.session, `{"model":"glm","messages":[]}`); r.status != http.StatusPaymentRequired || r.code != budget.CodeBudgetExhausted ||
			!strings.Contains(r.message, "spent $0.0012 of $0.001") {
			t.Fatalf("%s after the shared budget ran out: %+v", c.token, r)
		}
	}
}

// Acceptance scenario 19: provider keys, prompts and code never reach the server log, also when the
// upstream echoes the request in an error.
func TestServerLogsCarryNoSecretsOrContent(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const key, prompt, code = "sk-or-KEY-MARKER", "PROMPT-MARKER please refactor", "def SOURCE_MARKER(): pass"
	b := newBudgetStackWith(t, budgetPolicy, key)
	body, _ := json.Marshal(map[string]any{"model": "glm", "messages": []map[string]string{
		{"role": "user", "content": prompt}, {"role": "tool", "content": code}}})
	if r := b.chatAs("bob", "s-bob", string(body)); r.status != http.StatusOK {
		t.Fatalf("success: %+v", r)
	}
	b.fail.Store(true)
	if r := b.chatAs("bob", "s-bob", string(body)); r.status != http.StatusInternalServerError {
		t.Fatalf("upstream error: %+v", r)
	}
	b.fail.Store(false)
	if r := b.chatAs("nobody-token", "s-x", string(body)); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %+v", r)
	}
	// Accounting failures are logged with their error; the request content must still stay out.
	b.store.Close()
	if r := b.chatAs("bob", "s-bob", string(body)); r.status != http.StatusServiceUnavailable {
		t.Fatalf("accounting down: %+v", r)
	}
	if code := get(t, b.server, "/api/requests", "bob", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("dashboard with storage down: %d", code)
	}
	if logs.Len() == 0 {
		t.Fatal("nothing was logged; the check would prove nothing")
	}
	for _, marker := range []string{"KEY-MARKER", "PROMPT-MARKER", "SOURCE_MARKER", "nobody-token"} {
		if strings.Contains(logs.String(), marker) {
			t.Errorf("log contains %s:\n%s", marker, logs.String())
		}
	}
}
