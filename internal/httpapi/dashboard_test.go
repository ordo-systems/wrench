package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/api"

	"github.com/ordo-systems/wrench/internal/catalog/catalogtest"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/backupstatus"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

// dashboardServer uses the repository config.yaml; "u1" is user1 (user), "u3" is user3 (admin).
func dashboardServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	holder, err := config.NewHolder("../../deploy/shared/config/config.yaml", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	cfg := holder.Current()
	st := storetest.Open(t)
	ctx := context.Background()
	clock := time.Date(2026, 11, 10, 12, 30, 0, 0, time.UTC)
	for i, email := range []string{"user1@example.com", "user2@example.com"} {
		session := "s-" + email
		if err := st.TouchSession(ctx, session, email, nil, "", clock.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		id := "a" + string(rune('1'+i))
		if err := st.StartAttempt(ctx, store.Attempt{ID: id, RequestID: id, UserEmail: email, SessionID: session, RequestedModel: "glm-5.3", StartedAt: clock.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		cost := money.Amount(1000 * (i + 1))
		if err := st.FinishAttempt(ctx, id, store.AttemptResult{FinishedAt: clock.Add(-time.Hour + time.Second), Outcome: store.OutcomeSuccess, Cost: &cost}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.BindIdentity(ctx, "user1@example.com", "https://gitlab.example.com", "1", 1, clock); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SyncIncidents(ctx, clock, []store.Condition{
		{Key: "budget:user:user2@example.com:day:80", UserEmail: "user2@example.com", Severity: store.SeverityWarning, Message: "u2"},
		{Key: "provider:openrouter", Severity: store.SeverityCritical, Message: "down"},
		{Key: "budget:global::day:80", Severity: store.SeverityWarning, Message: "global day budget reached 91%: $455 of $500"},
	}); err != nil {
		t.Fatal(err)
	}
	ids := tokens{
		"u1": {Email: "user1@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true},
		"u3": {Email: "user3@example.com", Role: config.RoleAdmin, Credential: auth.CredentialKey, StatusFresh: true},
	}
	h := New(Deps{Store: st, Auth: ids, Proxy: http.NotFoundHandler(), Dashboard: http.NotFoundHandler(), Config: holder, Catalog: catalogtest.For(t, cfg, catalog.Entry{UpstreamID: "z-ai/glm-5.3", ContextLength: 128000, MaxCompletionTokens: 16384, PricePrompt: "0.0000006", PriceCompletion: "0.0000022"}),
		Budget: budget.NewChecker(config.Fixed(cfg), st), PublicURL: publicURL, UpstreamReady: func(context.Context) error { return nil },
		now: func() time.Time { return clock }})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, st
}

type statsBody struct {
	BucketSeconds int         `json:"bucket_seconds"`
	Stats         store.Stats `json:"stats"`
}

func TestStatsAreScopedAndValidated(t *testing.T) {
	srv, _ := dashboardServer(t)
	var own statsBody
	if code := get(t, srv, "/api/stats", "u1", &own); code != http.StatusOK ||
		own.Stats.Total.Attempts != 1 || own.Stats.Total.Cost != 1000 || len(own.Stats.Buckets) != 24 || own.BucketSeconds != 3600 {
		t.Fatalf("user stats = %d %+v", code, own.Stats.Total)
	}
	for _, q := range []string{"user=user2@example.com", "user=*"} {
		if code := get(t, srv, "/api/stats?"+q, "u1", nil); code != http.StatusForbidden {
			t.Errorf("user %s: %d", q, code)
		}
	}
	var self, all, narrowed statsBody
	get(t, srv, "/api/stats", "u3", &self)
	get(t, srv, "/api/stats?user=*", "u3", &all)
	get(t, srv, "/api/stats?user=user2@example.com&bucket=day&from=2026-11-09T13:00:00Z&to=2026-11-10T13:00:00Z", "u3", &narrowed)
	if self.Stats.Total.Attempts != 0 || all.Stats.Total.Cost != 3000 || narrowed.Stats.Total.Cost != 2000 || len(narrowed.Stats.Buckets) != 1 {
		t.Fatalf("admin = %+v / %+v / %+v", self.Stats.Total, all.Stats.Total, narrowed.Stats.Total)
	}
	for _, q := range []string{"bucket=week", "from=yesterday", "to=2026-11-10T00:00:00Z&from=2026-11-11T00:00:00Z",
		"bucket=5m&from=2026-11-01T00:00:00Z&to=2026-11-10T00:00:00Z"} {
		if code := get(t, srv, "/api/stats?"+q, "u3", nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
}

func TestUsersListAllowlistWithState(t *testing.T) {
	srv, _ := dashboardServer(t)
	var out struct {
		Users []userView `json:"users"`
	}
	if code := get(t, srv, "/api/users", "u3", &out); code != http.StatusOK || len(out.Users) != 4 {
		t.Fatalf("admin users = %d %+v", code, out.Users)
	}
	if out.Users[0].Tier != "basic" || out.Users[3].Tier != "premium" {
		t.Fatalf("tiers = %q %q", out.Users[0].Tier, out.Users[3].Tier)
	}
	u1 := out.Users[0]
	if u1.Email != "user1@example.com" || u1.State == nil || u1.State.Status != store.UserPending || len(u1.Usage) != 4 ||
		u1.Limits["day"].String() != "1" || out.Users[1].State != nil || out.Users[3].Limits["week"].String() != "15" {
		t.Fatalf("users = %+v", out.Users)
	}
}

// System-wide views describe every user and the deployment, so only administrators may read them.
func TestSystemUsersAlertsAreAdminOnly(t *testing.T) {
	srv, _ := dashboardServer(t)
	for _, path := range []string{"/api/system", "/api/users", "/api/alerts"} {
		if code := get(t, srv, path, "u1", nil); code != http.StatusForbidden {
			t.Errorf("user %s: %d", path, code)
		}
		if code := get(t, srv, path, "u3", nil); code != http.StatusOK {
			t.Errorf("admin %s: %d", path, code)
		}
	}
}

func TestSystemShowsModels(t *testing.T) {
	srv, _ := dashboardServer(t)
	var out struct {
		Checks map[string]string `json:"checks"`
		Models []modelView       `json:"models"`
		Backup map[string]string `json:"backup"`
	}
	if code := get(t, srv, "/api/system", "u3", &out); code != http.StatusOK || out.Checks["openrouter"] != "ok" || len(out.Models) != 5 {
		t.Fatalf("system = %d %+v", code, out)
	}
	if m := out.Models[1]; m.Name != "glm-5.3" || m.UpstreamID != "z-ai/glm-5.3" || m.Status != catalog.StatusOK ||
		m.Health == nil || m.Health.Attempts != 2 || out.Models[0].Health != nil || out.Backup["status"] != "not_configured" {
		t.Fatalf("models = %+v", out.Models)
	}
}

func TestSystemShowsConfigurationStatus(t *testing.T) {
	srv, _ := dashboardServer(t)
	var out struct {
		Configuration *config.ReloadStatus `json:"configuration"`
	}
	if get(t, srv, "/api/system", "u3", &out); out.Configuration == nil || !strings.HasPrefix(out.Configuration.Revision, "sha256:") {
		t.Fatalf("admin configuration = %+v", out.Configuration)
	}
}

func TestSystemShowsTiers(t *testing.T) {
	srv, _ := dashboardServer(t)
	var out struct {
		Tiers []tierView `json:"tiers"`
	}
	if code := get(t, srv, "/api/system", "u3", &out); code != http.StatusOK || len(out.Tiers) != 2 {
		t.Fatalf("tiers = %d %+v", code, out.Tiers)
	}
	basic := out.Tiers[0]
	if basic.Name != "basic" || !basic.Default || basic.Models[0].Name != "glm-5.3" || !basic.Models[0].Default ||
		basic.Budget["day"].String() != "1" {
		t.Fatalf("basic = %+v", basic)
	}
}

func TestAlertsAreScoped(t *testing.T) {
	srv, _ := dashboardServer(t)
	var out struct {
		Alerts  []store.Incident `json:"alerts"`
		Enabled bool             `json:"enabled"`
	}
	// By default an administrator gets their own and system-wide incidents; user2's budget incident needs ?user=*.
	if get(t, srv, "/api/alerts", "u3", &out); len(out.Alerts) != 2 || !out.Enabled {
		t.Fatalf("admin own alerts = %+v", out.Alerts)
	}
	if get(t, srv, "/api/alerts?user=*", "u3", &out); len(out.Alerts) != 3 {
		t.Fatalf("admin alerts = %+v", out.Alerts)
	}
}

func TestSystemShowsGlobalLimits(t *testing.T) {
	srv, _ := dashboardServer(t)
	var out struct {
		Budgets map[string]json.RawMessage `json:"budgets"`
	}
	if code := get(t, srv, "/api/system", "u3", &out); code != http.StatusOK || out.Budgets["warn_at_percent"] == nil || out.Budgets["global"] == nil {
		t.Fatalf("budgets = %d %+v", code, out.Budgets)
	}
}

func TestBackupView(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if v := backupView("", now); v.Status != api.BackupStatusNotConfigured {
		t.Fatalf("not deployed = %v", v)
	}
	path := filepath.Join(t.TempDir(), "backup.json")
	if v := backupView(path, now); v.Status != api.BackupStatusAttention {
		t.Fatalf("no report = %v", v)
	}
	last := now.Add(-10 * time.Hour)
	ok := backupstatus.Status{LastRunAt: &last, LastSuccessAt: &last, NextRunAt: now.Add(4 * time.Hour), StaleAfter: now.Add(6 * time.Hour)}
	if err := backupstatus.Write(path, ok); err != nil {
		t.Fatal(err)
	}
	if v := backupView(path, now); v.Status != api.BackupStatusOk || v.LastSuccessAt == nil {
		t.Fatalf("healthy = %v", v)
	}
	if v := backupView(path, now.Add(7*time.Hour)); v.Status != api.BackupStatusAttention || v.Problem == nil || !strings.Contains(*v.Problem, "no successful backup") {
		t.Fatalf("overdue = %v", v)
	}
}

func repoConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../deploy/shared/config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The views below decode the wire format the dashboard tests assert; they mirror the pre-OpenAPI handler types.
type userView struct {
	Email  string                  `json:"email"`
	Role   config.Role             `json:"role"`
	Tier   string                  `json:"tier"`
	Limits map[string]money.Amount `json:"limits"`
	State  *store.UserState        `json:"state"`
	Usage  []budget.Usage          `json:"usage"`
}

type modelView struct {
	Name            string             `json:"name"`
	UpstreamID      string             `json:"upstream_id,omitempty"`
	InputTokenLimit int                `json:"input_token_limit"`
	MaxOutputTokens int                `json:"max_output_tokens"`
	ReasoningEffort string             `json:"reasoning_effort"`
	Status          catalog.Status     `json:"status,omitempty"`
	Health          *store.ModelHealth `json:"health"`
	PriceInput      *string            `json:"price_input_per_mtok"`
	PriceOutput     *string            `json:"price_output_per_mtok"`
}

type tierView struct {
	Name    string                  `json:"name"`
	Default bool                    `json:"default"`
	Budget  map[string]money.Amount `json:"budget"`
	Models  []config.TierModel      `json:"models"`
}

// Every list refuses a user who asks for anyone else, and lets an administrator ask for one user or everyone.
func TestListsFollowTheScopeRule(t *testing.T) {
	srv, _ := dashboardServer(t)
	paths := []string{"/api/requests?", "/api/sessions?", "/api/usage?window=day&", "/api/stats?", "/api/keys?"}
	for _, path := range paths {
		for _, c := range []struct {
			token, user string
			want        int
		}{
			{"u1", "", http.StatusOK},
			{"u1", "user1@example.com", http.StatusOK},
			{"u1", "user2@example.com", http.StatusForbidden},
			{"u1", "*", http.StatusForbidden},
			{"u3", "", http.StatusOK},
			{"u3", "user2@example.com", http.StatusOK},
			{"u3", "*", http.StatusOK},
		} {
			if code := get(t, srv, path+"user="+url.QueryEscape(c.user), c.token, nil); code != c.want {
				t.Errorf("%s %suser=%s: %d, want %d", c.token, path, c.user, code, c.want)
			}
		}
	}
}

func TestStatsGroupByUserForbidden(t *testing.T) {
	srv, _ := dashboardServer(t)
	if code := get(t, srv, "/api/stats?group_by=user", "u1", nil); code != http.StatusForbidden {
		t.Fatalf("user: %d", code)
	}
	var out struct {
		Stats struct {
			Series []store.UserSeries `json:"series"`
		} `json:"stats"`
	}
	if code := get(t, srv, "/api/stats?group_by=user&user=*", "u3", &out); code != http.StatusOK || len(out.Stats.Series) != 2 ||
		out.Stats.Series[0].User != "user2@example.com" || len(out.Stats.Series[0].Buckets) != 24 {
		t.Fatalf("admin: %d %+v", code, out.Stats.Series)
	}
	if code := get(t, srv, "/api/stats?group_by=model", "u3", nil); code != http.StatusBadRequest {
		t.Fatalf("unknown group_by: %d", code)
	}
}

func TestUsageGroupsByModelWithinScope(t *testing.T) {
	srv, st := dashboardServer(t)
	ctx := context.Background()
	clock := time.Date(2026, 11, 10, 12, 30, 0, 0, time.UTC)
	// user1 adds a second model; user2 already has one glm-5.3 attempt from the fixture.
	for i, model := range []string{"opus-5.5", "opus-5.5"} {
		id := "extra" + string(rune('a'+i))
		if err := st.StartAttempt(ctx, store.Attempt{ID: id, RequestID: id, UserEmail: "user1@example.com", SessionID: "s-user1@example.com",
			RequestedModel: model, StartedAt: clock.Add(-30 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		cost := money.Amount(5000)
		if err := st.FinishAttempt(ctx, id, store.AttemptResult{FinishedAt: clock.Add(-29 * time.Minute), Outcome: store.OutcomeSuccess, Cost: &cost}); err != nil {
			t.Fatal(err)
		}
	}
	keys := func(path, token string) map[string]int64 {
		t.Helper()
		var out struct {
			Groups []struct {
				Key      string `json:"key"`
				Attempts int64  `json:"attempts"`
			} `json:"groups"`
		}
		if code := get(t, srv, path, token, &out); code != http.StatusOK {
			t.Fatalf("%s: %d", path, code)
		}
		m := map[string]int64{}
		for _, g := range out.Groups {
			m[g.Key] = g.Attempts
		}
		return m
	}
	if got := keys("/api/usage?window=day&group=model", "u1"); len(got) != 2 || got["glm-5.3"] != 1 || got["opus-5.5"] != 2 {
		t.Fatalf("user1 sees %v", got)
	}
	if got := keys("/api/usage?window=day&group=model&user=*", "u3"); len(got) != 2 || got["glm-5.3"] != 2 || got["opus-5.5"] != 2 {
		t.Fatalf("admin sees %v", got)
	}
}

func TestSystemShowsModelPrices(t *testing.T) {
	srv, _ := dashboardServer(t)
	var out struct {
		Models []modelView `json:"models"`
	}
	if code := get(t, srv, "/api/system", "u3", &out); code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	glm := out.Models[1]
	if glm.PriceInput == nil || *glm.PriceInput != "0.6" || glm.PriceOutput == nil || *glm.PriceOutput != "2.2" {
		t.Fatalf("glm prices = %v / %v", glm.PriceInput, glm.PriceOutput)
	}
	if out.Models[0].PriceInput != nil || out.Models[0].PriceOutput != nil {
		t.Fatalf("unpriced model has prices: %+v", out.Models[0])
	}
}
