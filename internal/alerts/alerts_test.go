package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

var (
	now    = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	policy = &config.Alerts{Window: 15 * time.Minute, RepeatAfter: time.Hour,
		LowBalance: &config.LowBalance{Threshold: money.Amount(5_000_000_000)},
		APIErrors:  &config.APIErrors{Threshold: 20, MinAttempts: 5},
		FirstToken: &config.FirstToken{ThresholdP95: 30 * time.Second, MinAttempts: 5}}
)

func ptr[T any](v T) *T { return &v }

func keys(cs []store.Condition) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Key)
	}
	return out
}

func TestEvaluate(t *testing.T) {
	day := budget.Usage{Scope: budget.ScopeUser, Window: store.WindowDay, Spent: 900, Limit: 1000, Percent: 90, Threshold: 80, ResetsAt: now}
	full := budget.Usage{Scope: budget.ScopeGlobal, Window: store.WindowWeek, Spent: 1000, Limit: 1000, Percent: 100, Threshold: 95, ResetsAt: now}
	calm := budget.Usage{Scope: budget.ScopeUser, Window: store.WindowFiveHours, Spent: 1, Limit: 1000, Percent: 0}
	cases := []struct {
		name string
		s    snapshot
		want []string
	}{
		{"quiet", snapshot{usage: map[string][]budget.Usage{"a@x": {calm}}, balance: ptr(money.Amount(6_000_000_000))}, []string{}},
		{"budgets", snapshot{usage: map[string][]budget.Usage{"a@x": {day, calm}, "": {full}}},
			[]string{"budget:global::week:exhausted", "budget:user:a@x:day:80"}},
		{"low balance", snapshot{balance: ptr(money.Amount(4_999_999_999))}, []string{"openrouter:balance"}},
		{"openrouter down", snapshot{upstream: errors.New("refused")}, []string{"provider:openrouter"}},
		{"error rate at threshold", snapshot{recent: store.StatsBucket{Success: 4, Errors: 1}}, []string{"provider:errors"}},
		{"error rate below threshold", snapshot{recent: store.StatsBucket{Success: 5, Errors: 1}}, []string{}},
		{"too few attempts", snapshot{recent: store.StatsBucket{Success: 1, Errors: 3}}, []string{}},
		{"slow first token", snapshot{recent: store.StatsBucket{FirstToken: store.Latency{Observed: 5, P95: ptr(30_001.0)}}}, []string{"provider:latency"}},
		{"latency at limit", snapshot{recent: store.StatsBucket{FirstToken: store.Latency{Observed: 5, P95: ptr(30_000.0)}}}, []string{}},
		{"backup problem", snapshot{backup: "the last backup failed: s3: refused"}, []string{"backup"}},
		{"model unavailable", snapshot{catalogFetched: now, unavailable: []catalog.ModelStatus{{Name: "glm", UpstreamID: "z/glm", Status: catalog.StatusUnknownUpstream}}},
			[]string{"models:catalog"}},
		{"stale catalog", snapshot{catalogFetched: now.Add(-25 * time.Hour)}, []string{"models:catalog"}},
		{"fresh catalog", snapshot{catalogFetched: now.Add(-23 * time.Hour)}, []string{}},
		{"invalid configuration", snapshot{configError: "config.yaml: unknown tier"}, []string{"config:invalid"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keys(evaluate(policy, c.s, now)); strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Fatalf("conditions = %v, want %v", got, c.want)
			}
		})
	}
	msg := evaluate(policy, snapshot{usage: map[string][]budget.Usage{"a@x": {day}}}, now)[0]
	if msg.UserEmail != "a@x" || msg.Message != "a@x's day budget reached 90%: $0.0000009 of $0.000001, resets 2026-11-10T12:00:00Z" {
		t.Fatalf("message = %+v", msg)
	}
}

func TestConfigInvalidMessage(t *testing.T) {
	cs := evaluate(policy, snapshot{configError: `users: a@x has unknown tier "gold"`}, now)
	if len(cs) != 1 || cs[0].Severity != store.SeverityCritical ||
		cs[0].Message != `configuration change rejected; the previous configuration stays in force: users: a@x has unknown tier "gold"` {
		t.Fatalf("conditions = %+v", cs)
	}
}

func TestModelCatalogMessage(t *testing.T) {
	cs := evaluate(policy, snapshot{catalogFetched: now, unavailable: []catalog.ModelStatus{
		{Name: "glm", Status: catalog.StatusUnknownUpstream}, {Name: "opus", Status: catalog.StatusNoCatalog}}}, now)
	if len(cs) != 1 || cs[0].Severity != store.SeverityCritical || cs[0].Message != "models unavailable: glm (unknown_upstream), opus (no_catalog)" {
		t.Fatalf("conditions = %+v", cs)
	}
	cs = evaluate(policy, snapshot{catalogFetched: now.Add(-25 * time.Hour)}, now)
	if len(cs) != 1 || !strings.Contains(cs[0].Message, "model catalog was last fetched 25h0m0s ago") {
		t.Fatalf("stale = %+v", cs)
	}
}

func TestEvaluateSubsections(t *testing.T) {
	trouble := snapshot{balance: ptr(money.Amount(1)),
		recent: store.StatsBucket{Success: 1, Errors: 2, FirstToken: store.Latency{Observed: 3, P95: ptr(60_000.0)}}}
	strict := *policy
	strict.APIErrors = &config.APIErrors{Threshold: 50, MinAttempts: 3}
	strict.FirstToken = &config.FirstToken{ThresholdP95: 30 * time.Second, MinAttempts: 3}
	if got := strings.Join(keys(evaluate(&strict, trouble, now)), " "); got != "openrouter:balance provider:errors provider:latency" {
		t.Fatalf("own min_attempts: %s", got)
	}
	none := config.Alerts{Window: policy.Window, RepeatAfter: policy.RepeatAfter}
	if got := keys(evaluate(&none, trouble, now)); len(got) != 0 {
		t.Fatalf("omitted subsections still alert: %v", got)
	}
}

type fakeBudgets map[string][]budget.Usage

func (f fakeBudgets) Status(_ context.Context, email string) ([]budget.Usage, error) {
	return f[email], nil
}

type recorder struct {
	texts []string
	err   error
}

func (r *recorder) Notify(_ context.Context, text string) error {
	if r.err != nil {
		return r.err
	}
	r.texts = append(r.texts, text)
	return nil
}

// pingStore makes Ping fail on demand around a real store.
type pingStore struct {
	*store.Store
	down bool
}

func (p *pingStore) Ping(ctx context.Context) error {
	if p.down {
		return errors.New("connection refused")
	}
	return p.Store.Ping(ctx)
}

func TestRunnerAnnouncesOpenRepeatAndResolve(t *testing.T) {
	cfg := &config.Config{Users: []config.User{{Email: "a@x"}}, Alerts: policy}
	st := &pingStore{Store: storetest.Open(t)}
	over := budget.Usage{Scope: budget.ScopeUser, Window: store.WindowDay, Spent: 2000, Limit: 1000, Percent: 200, ResetsAt: now}
	budgets := fakeBudgets{"a@x": {over}}
	n := &recorder{}
	upstream := error(nil)
	r := New(config.Fixed(cfg), st, budgets, nil, nil, func(context.Context) error { return upstream }, n, "", nil)
	clock := now
	r.now = func() time.Time { return clock }
	r.log = slog.New(slog.NewTextHandler(io.Discard, nil))

	tick := func() {
		t.Helper()
		if err := r.Tick(context.Background()); err != nil && !st.down {
			t.Fatal(err)
		}
	}
	tick()
	tick()
	if len(n.texts) != 1 || !strings.HasPrefix(n.texts[0], "[WRENCH CRITICAL] a@x's day budget is exhausted") {
		t.Fatalf("first checks = %q", n.texts)
	}
	// Delivery failures are retried on the next check instead of being lost.
	upstream = errors.New("down")
	n.err = errors.New("webhook down")
	clock = clock.Add(time.Minute)
	tick()
	n.err = nil
	tick()
	if len(n.texts) != 2 || n.texts[1] != "[WRENCH CRITICAL] OpenRouter is unreachable with Wrench's key; inference will fail" {
		t.Fatalf("after retry = %q", n.texts)
	}
	clock = clock.Add(time.Hour)
	upstream = nil
	budgets["a@x"] = nil
	tick()
	if len(n.texts) != 4 || !strings.HasPrefix(n.texts[2], "[WRENCH RESOLVED] a@x's day budget") || !strings.HasPrefix(n.texts[3], "[WRENCH RESOLVED] OpenRouter") {
		t.Fatalf("resolution = %q", n.texts)
	}

	// Storage down cannot be stored: it is announced directly, once per repeat period.
	st.down = true
	tick()
	tick()
	clock = clock.Add(time.Hour)
	tick()
	if len(n.texts) != 6 || n.texts[4] != "[WRENCH CRITICAL] accounting storage is unavailable; inference is refused" {
		t.Fatalf("storage down = %q", n.texts)
	}
}

func TestWebhookPostsText(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["text"] == "fail" {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	w := NewWebhook(srv.URL + "/hooks/secret-token")
	if err := w.Notify(context.Background(), "hello"); err != nil || got["text"] != "hello" {
		t.Fatalf("notify = %v %v", got, err)
	}
	if err := w.Notify(context.Background(), "fail"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("status error = %v", err)
	}
	srv.Close()
	if err := w.Notify(context.Background(), "x"); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unreachable error leaks the URL: %v", err)
	}
}

// The readiness probe reads GET <base>/key: unlike /credits it accepts a regular (non-management) API key.
func TestOpenRouterKeyCheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{{"valid key", http.StatusOK, false}, {"rejected key", http.StatusUnauthorized, true}, {"rate limited", http.StatusTooManyRequests, true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var auth, path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth, path = r.Header.Get("Authorization"), r.URL.Path
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, `{"data":{"label":"sk-or-v1-0e6...1c96","limit":null,"usage":1.5}}`)
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL + "/api/v1")
			err := NewOpenRouterKey(u, "or-key").Check(context.Background())
			if (err != nil) != tt.wantErr || path != "/api/v1/key" || auth != "Bearer or-key" {
				t.Fatalf("err %v, path %q, auth %q", err, path, auth)
			}
		})
	}
}

// The balance is the key's remaining credit limit from GET <base>/key, which a regular API key may read
// (/credits needs a management key).
func TestOpenRouterBalance(t *testing.T) {
	tests := []struct {
		name, body, want, errText string
	}{
		{"remaining limit", `{"data":{"limit":12.5,"limit_remaining":2.25,"usage":10.25}}`, "2.25", ""},
		{"no credit limit", `{"data":{"limit":null,"limit_remaining":null,"usage":10.25}}`, "", "has no credit limit"},
		{"incomplete", `{"data":{}}`, "", "has no credit limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var auth, path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				auth, path = r.Header.Get("Authorization"), r.URL.Path
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			u, _ := url.Parse(srv.URL + "/api/v1")
			b, err := NewOpenRouterBalance(u, "or-key").Balance(context.Background())
			if auth != "Bearer or-key" || path != "/api/v1/key" {
				t.Fatalf("request %q with %q", path, auth)
			}
			if tt.errText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("err = %v, want %q", err, tt.errText)
				}
				return
			}
			if err != nil || b.String() != tt.want {
				t.Fatalf("balance = %s, %v", b, err)
			}
		})
	}
}
