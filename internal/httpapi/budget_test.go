package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/catalog/catalogtest"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/env"
	"github.com/ordo-systems/wrench/internal/proxy"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

// Alice may spend $0.001 per day; each fake call costs $0.0006 unless the upstream omits the cost.
const budgetPolicy = `models:
  glm:
    upstream_id: z-ai/glm-5.3
budget:
  currency: USD
  timezone: UTC
  global_limits: {5_hours: 1, day: 1, week: 1, month: 100, warn_at_percent: [50]}
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
  - email: admin@example.com
    role: admin
  - email: alice@example.com
    tier: tight
  - email: bob@example.com
`

type budgetStack struct {
	t      *testing.T
	store  *store.Store
	server *httptest.Server
	// omitCost makes the fake upstream report no cost, as after a broken stream.
	omitCost atomic.Bool
	// hold blocks upstream responses until closed, to keep requests in flight.
	hold chan struct{}
	// fail makes the fake upstream answer 500 and echo the request body, like a verbose provider error.
	fail atomic.Bool
}

func newBudgetStack(t *testing.T) *budgetStack {
	t.Helper()
	return newBudgetStackWith(t, budgetPolicy, "k")
}

// newBudgetStackWith runs the full proxy and API stack on a real store with the given policy and OpenRouter key.
func newBudgetStackWith(t *testing.T, policy, openrouterKey string) *budgetStack {
	t.Helper()
	b := &budgetStack{t: t, store: storetest.Open(t)}
	var n atomic.Int64
	openrouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b.hold != nil {
			<-b.hold
		}
		if b.fail.Load() {
			body, _ := io.ReadAll(r.Body)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(append([]byte(`{"error":{"message":"bad request: `), body...))
			return
		}
		cost := `,"cost":0.0006`
		if b.omitCost.Load() {
			cost = ""
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"gen-`+time.Now().Format("150405.000000")+`-`+string(rune('a'+n.Add(1)%26))+`","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1`+cost+`}}`)
	}))
	t.Cleanup(openrouter.Close)
	cfg := loadPolicy(t, policy)
	u, _ := url.Parse(openrouter.URL)
	checker := budget.NewChecker(config.Fixed(cfg), b.store)
	p := proxy.New(config.Fixed(cfg), catalogtest.For(t, cfg), env.UpstreamEnv{URL: u, Key: env.Secret(openrouterKey), TimeoutConnect: time.Second, TimeoutFirstToken: 5 * time.Second,
		TimeoutStreamIdle: 5 * time.Second, TimeoutTotal: 10 * time.Second, ConcurrencyPerUser: 5, ConcurrencyGlobal: 10}, b.store, checker)
	b.server = httptest.NewServer(New(Deps{Store: b.store, Auth: budgetUsers, Proxy: p, Dashboard: http.NotFoundHandler(),
		Config: config.Fixed(cfg), Catalog: catalogtest.For(t, cfg), Budget: checker, UpstreamReady: func(context.Context) error { return nil },
		PublicURL: "https://wrench.example.com", OrgName: "Acme"}))
	t.Cleanup(b.server.Close)
	return b
}

var budgetUsers = tokens{
	"admin": {Email: "admin@example.com", Role: config.RoleAdmin, Credential: auth.CredentialKey, StatusFresh: true},
	"alice": {Email: "alice@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true},
	"bob":   {Email: "bob@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true},
}

type chatResult struct {
	status  int
	code    string
	message string
	warning string
}

func (b *budgetStack) chat(token string) chatResult {
	b.t.Helper()
	return b.chatAs(token, "s-"+token, `{"model":"glm","messages":[]}`)
}

// chatAs sends a request in a session.
func (b *budgetStack) chatAs(token, session, payload string) chatResult {
	b.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, b.server.URL+"/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(proxy.SessionHeader, session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	code, _ := body.Error.Code.(string)
	return chatResult{resp.StatusCode, code, body.Error.Message, resp.Header.Get(proxy.BudgetWarningHeader)}
}

func TestBudgetAcceptance(t *testing.T) {
	b := newBudgetStack(t)

	if r := b.chat("alice"); r.status != http.StatusOK || r.warning != "" {
		t.Fatalf("first: %+v", r)
	}
	// $0.0006 of $0.001 spent: still admitted, with a warning, and allowed to overrun.
	if r := b.chat("alice"); r.status != http.StatusOK || !strings.Contains(r.warning, "user day 60%") {
		t.Fatalf("second: %+v", r)
	}
	r := b.chat("alice")
	if r.status != http.StatusPaymentRequired || r.code != budget.CodeBudgetExhausted || !strings.Contains(r.message, "spent $0.0012 of $0.001") {
		t.Fatalf("third: %+v", r)
	}
	// Other users have their own budgets.
	if r := b.chat("bob"); r.status != http.StatusOK {
		t.Fatalf("bob: %+v", r)
	}

	var status struct {
		Windows []budget.Usage `json:"windows"`
	}
	if code := get(t, b.server, "/api/budget", "alice", &status); code != http.StatusOK || len(status.Windows) != 4 {
		t.Fatalf("budget: %d %+v", code, status)
	}
	day := status.Windows[1]
	if day.Window != store.WindowDay || day.Spent.String() != "0.0012" || day.Limit.String() != "0.001" || day.Percent != 120 {
		t.Fatalf("alice day = %+v", day)
	}
	if code := get(t, b.server, "/api/budget", "admin", &status); code != http.StatusOK || len(status.Windows) != 8 {
		t.Fatalf("admin budget: %d %+v", code, status)
	}
	if global := status.Windows[4]; global.Scope != budget.ScopeGlobal || global.Spent.String() != "0.0018" {
		t.Fatalf("global day = %+v", global)
	}
}

// TestBudgetStatusByRole checks that global windows, which show everyone's spend, reach administrators only.
func TestBudgetStatusByRole(t *testing.T) {
	b := newBudgetStack(t)
	for _, c := range []struct {
		token  string
		scopes []budget.Scope
	}{
		{"admin", []budget.Scope{budget.ScopeUser, budget.ScopeGlobal}},
		{"alice", []budget.Scope{budget.ScopeUser}},
	} {
		t.Run(c.token, func(t *testing.T) {
			var status map[string]json.RawMessage
			if code := get(t, b.server, "/api/budget", c.token, &status); code != http.StatusOK {
				t.Fatalf("status %d", code)
			}
			var windows []budget.Usage
			if err := json.Unmarshal(status["windows"], &windows); err != nil {
				t.Fatal(err)
			}
			var scopes []budget.Scope
			for _, w := range windows {
				if !slices.Contains(scopes, w.Scope) {
					scopes = append(scopes, w.Scope)
				}
			}
			if !slices.Equal(scopes, c.scopes) {
				t.Fatalf("scopes = %v, want %v", scopes, c.scopes)
			}
		})
	}
}

// TestBudgetStatusOfAnotherUser checks that administrators can read a chosen user's windows and nobody else can.
func TestBudgetStatusOfAnotherUser(t *testing.T) {
	b := newBudgetStack(t)
	// Alice's tier limits the day to $0.001; the default tier allows $1.
	tests := []struct {
		name, token, query string
		status             int
		scopes             []budget.Scope
		dayLimit           string
	}{
		{"admin reads alice", "admin", "?user=alice@example.com", http.StatusOK, []budget.Scope{budget.ScopeUser, budget.ScopeGlobal}, "0.001"},
		{"admin without user", "admin", "", http.StatusOK, []budget.Scope{budget.ScopeUser, budget.ScopeGlobal}, "1"},
		{"user without user", "alice", "", http.StatusOK, []budget.Scope{budget.ScopeUser}, "0.001"},
		{"user names herself", "alice", "?user=alice@example.com", http.StatusOK, []budget.Scope{budget.ScopeUser}, "0.001"},
		{"user names another", "alice", "?user=bob@example.com", http.StatusForbidden, nil, ""},
		{"user names everyone", "alice", "?user=*", http.StatusForbidden, nil, ""},
		{"admin names everyone", "admin", "?user=*", http.StatusBadRequest, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var status struct {
				Windows []budget.Usage `json:"windows"`
			}
			if code := get(t, b.server, "/api/budget"+tt.query, tt.token, &status); code != tt.status {
				t.Fatalf("status %d, want %d", code, tt.status)
			}
			if tt.status != http.StatusOK {
				return
			}
			var scopes []budget.Scope
			for _, w := range status.Windows {
				if !slices.Contains(scopes, w.Scope) {
					scopes = append(scopes, w.Scope)
				}
				if w.Scope == budget.ScopeUser && w.Window == store.WindowDay && w.Limit.String() != tt.dayLimit {
					t.Fatalf("user day limit = %s, want %s", w.Limit, tt.dayLimit)
				}
			}
			if !slices.Equal(scopes, tt.scopes) {
				t.Fatalf("scopes = %v, want %v", scopes, tt.scopes)
			}
		})
	}
}

func TestConcurrentRequestsAreNotReserved(t *testing.T) {
	b := newBudgetStack(t)
	b.hold = make(chan struct{})
	var wg sync.WaitGroup
	results := make([]chatResult, 3)
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = b.chat("alice") }()
	}
	// All three are admitted while nothing is recorded yet; they overrun together.
	time.Sleep(300 * time.Millisecond)
	close(b.hold)
	wg.Wait()
	for i, r := range results {
		if r.status != http.StatusOK {
			t.Fatalf("request %d: %+v", i, r)
		}
	}
	if r := b.chat("alice"); r.code != budget.CodeBudgetExhausted || !strings.Contains(r.message, "spent $0.0018") {
		t.Fatalf("after overrun: %+v", r)
	}
}

// TestUnknownCostIsMarkedWithoutPausing checks that a finished request of unknown cost refuses nobody and is
// counted in the windows of its scope: its user's own windows and the global ones, not other users' windows.
func TestUnknownCostIsMarkedWithoutPausing(t *testing.T) {
	b := newBudgetStack(t)
	b.omitCost.Store(true)
	if r := b.chat("alice"); r.status != http.StatusOK {
		t.Fatalf("costless: %+v", r)
	}
	b.omitCost.Store(false)
	if r := b.chat("bob"); r.status != http.StatusOK {
		t.Fatalf("bob during unsettled: %+v", r)
	}
	for _, c := range []struct {
		token string
		scope budget.Scope
		want  int64
	}{
		{"alice", budget.ScopeUser, 1},
		{"bob", budget.ScopeUser, 0},
		{"admin", budget.ScopeGlobal, 1},
	} {
		var status struct {
			Windows []budget.Usage `json:"windows"`
		}
		if code := get(t, b.server, "/api/budget", c.token, &status); code != http.StatusOK {
			t.Fatalf("%s budget: %d", c.token, code)
		}
		for _, w := range status.Windows {
			if w.Scope == c.scope && w.Unsettled != c.want {
				t.Errorf("%s %s %s unsettled = %d, want %d", c.token, w.Scope, w.Window, w.Unsettled, c.want)
			}
		}
	}
}

func TestAccountingOutageRefusesInference(t *testing.T) {
	b := newBudgetStack(t)
	b.store.Close()
	if r := b.chat("alice"); r.status != http.StatusServiceUnavailable {
		t.Fatalf("with storage down: %+v", r)
	}
	if code := get(t, b.server, "/api/budget", "alice", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("budget with storage down: %d", code)
	}
}

// TestBudgetJSONShape pins the wire shape the agent's /usage parser relies on.
func TestBudgetJSONShape(t *testing.T) {
	b := newBudgetStack(t)
	var status map[string]json.RawMessage
	if code := get(t, b.server, "/api/budget", "alice", &status); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if _, ok := status["unsettled"]; ok {
		t.Errorf("the budget still carries a global unsettled count: %v", status)
	}
	var windows []map[string]json.RawMessage
	if err := json.Unmarshal(status["windows"], &windows); err != nil || len(windows) == 0 {
		t.Fatalf("windows: %s (%v)", status["windows"], err)
	}
	for _, w := range windows {
		if _, ok := w["threshold"]; ok {
			t.Errorf("an untouched window carries threshold: %v", w)
		}
		if s := string(w["spent"]); s == "" || s[0] != '"' {
			t.Errorf("spent is not a decimal string: %s", s)
		}
		var unsettled int64
		if err := json.Unmarshal(w["unsettled"], &unsettled); err != nil {
			t.Errorf("window unsettled is not a number: %s", w["unsettled"])
		}
	}
}

func TestEmptyLimitMeansDefault(t *testing.T) {
	b := newBudgetStack(t)
	if code := get(t, b.server, "/api/requests?limit=", "alice", nil); code != http.StatusOK {
		t.Fatalf("GET /api/requests?limit=: %d", code)
	}
}

func TestBudgetWarnAtPercent(t *testing.T) {
	srv, _ := dashboardServer(t)
	var out api.BudgetStatus
	if code := get(t, srv, "/api/budget", "u1", &out); code != http.StatusOK || !slices.Equal(out.WarnAtPercent, []int{80, 95}) {
		t.Fatalf("budget = %d %+v", code, out.WarnAtPercent)
	}
}
