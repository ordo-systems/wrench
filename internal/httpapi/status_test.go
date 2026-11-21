package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/catalog/catalogtest"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/store"
)

func codes(reasons []api.PauseReason) []string {
	out := []string{}
	for _, r := range reasons {
		out = append(out, string(r.Code))
	}
	return out
}

func TestPauseReasons(t *testing.T) {
	window := func(scope budget.Scope, percent int) budget.Usage {
		return budget.Usage{Scope: scope, Window: "day", Percent: percent}
	}
	all := []string{"storage_unavailable", "upstream_unavailable", "global_budget_exhausted",
		"own_budget_exhausted", "gitlab_status_unconfirmed"}
	for _, tc := range []struct {
		name string
		in   statusInputs
		want []string
	}{
		{"all fine", statusInputs{StatusFresh: true, Windows: []budget.Usage{window(budget.ScopeUser, 50)}}, []string{}},
		{"storage down", statusInputs{StorageDown: true, UpstreamDown: true, StatusFresh: true,
			Windows: []budget.Usage{window(budget.ScopeGlobal, 100)}}, []string{"storage_unavailable"}},
		{"storage down and stale", statusInputs{StorageDown: true}, []string{"storage_unavailable", "gitlab_status_unconfirmed"}},
		{"upstream down", statusInputs{UpstreamDown: true, StatusFresh: true}, []string{"upstream_unavailable"}},
		{"global exhausted", statusInputs{StatusFresh: true, Windows: []budget.Usage{window(budget.ScopeGlobal, 100)}},
			[]string{"global_budget_exhausted"}},
		{"global almost", statusInputs{StatusFresh: true, Windows: []budget.Usage{window(budget.ScopeGlobal, 99)}}, []string{}},
		{"two global windows", statusInputs{StatusFresh: true,
			Windows: []budget.Usage{window(budget.ScopeGlobal, 100), window(budget.ScopeGlobal, 130)}},
			[]string{"global_budget_exhausted"}},
		{"own exhausted", statusInputs{StatusFresh: true, Windows: []budget.Usage{window(budget.ScopeUser, 120)}},
			[]string{"own_budget_exhausted"}},
		{"stale", statusInputs{}, []string{"gitlab_status_unconfirmed"}},
		{"everything", statusInputs{UpstreamDown: true,
			Windows: []budget.Usage{window(budget.ScopeUser, 100), window(budget.ScopeGlobal, 100)}},
			all[1:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pauseReasons(tc.in)
			if got == nil || !slices.Equal(codes(got), tc.want) {
				t.Fatalf("pauseReasons = %v, want %v", codes(got), tc.want)
			}
		})
	}
}

func TestStatusForUser(t *testing.T) {
	srv, _ := dashboardServer(t)
	var status api.Status
	if code := get(t, srv, "/api/status", "u1", &status); code != http.StatusOK || status.Inference.State != "ok" ||
		len(status.Inference.Reasons) != 0 || status.Version == "" {
		t.Fatalf("status = %d %+v", code, status)
	}
	var models api.ListModels200JSONResponse
	get(t, srv, "/api/models", "u1", &models)
	if len(status.Models) == 0 || !reflect.DeepEqual(status.Models, models.Models) {
		t.Fatalf("status models = %+v, /api/models = %+v", status.Models, models.Models)
	}
	body := rawGet(t, srv, "/api/status", "u1")
	if strings.Contains(body, "upstream_id") || strings.Contains(body, "z-ai/") || strings.Contains(body, "$") {
		t.Fatalf("status leaks system data: %s", body)
	}
}

func TestStatusWithStorageDown(t *testing.T) {
	holder, err := config.NewHolder("../../deploy/shared/config/config.yaml", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ids := tokens{"u1": {Email: "user1@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true}}
	srv := httptest.NewServer(New(Deps{Store: fakeStore{pingErr: errors.New("down")}, Auth: ids, Proxy: http.NotFoundHandler(),
		Dashboard: http.NotFoundHandler(), Config: holder, Catalog: catalogtest.For(t, holder.Current()), PublicURL: publicURL,
		UpstreamReady: func(context.Context) error { return nil }}))
	t.Cleanup(srv.Close)
	var status api.Status
	if code := get(t, srv, "/api/status", "u1", &status); code != http.StatusOK || status.Inference.State != "paused" ||
		!slices.Equal(codes(status.Inference.Reasons), []string{"storage_unavailable"}) {
		t.Fatalf("status = %d %+v", code, status)
	}
}

func rawGet(t *testing.T, srv *httptest.Server, path, token string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// ctxBudget answers like a healthy budget checker while its context is alive.
type ctxBudget struct{}

func (ctxBudget) Status(ctx context.Context, _ string) ([]budget.Usage, error) {
	return nil, ctx.Err()
}

// A probe that never answers must not hang the status, nor make the next probe report a false reason: the caller gets the reason within the probe timeout.
func TestStatusDoesNotHangOnAStuckUpstream(t *testing.T) {
	holder, err := config.NewHolder("../../deploy/shared/config/config.yaml", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ids := tokens{"u1": {Email: "user1@example.com", Role: config.RoleUser, Credential: auth.CredentialKey, StatusFresh: true}}
	srv := httptest.NewServer(New(Deps{Store: fakeStore{}, Auth: ids, Proxy: http.NotFoundHandler(),
		Dashboard: http.NotFoundHandler(), Config: holder, Catalog: catalogtest.For(t, holder.Current()), PublicURL: publicURL,
		Budget: ctxBudget{}, UpstreamReady: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}))
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer u1")
	resp, err := (&http.Client{Timeout: 2 * statusTimeout}).Do(req)
	if err != nil {
		t.Fatalf("status hangs: %v", err)
	}
	defer resp.Body.Close()
	var status api.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(codes(status.Inference.Reasons), []string{"upstream_unavailable"}) {
		t.Fatalf("reasons = %v", codes(status.Inference.Reasons))
	}
}

func TestBannerAlerts(t *testing.T) {
	open := func(key, user, message string) store.Incident {
		return store.Incident{Key: key, UserEmail: user, Severity: store.SeverityCritical, Message: message}
	}
	resolved := time.Now()
	incidents := []store.Incident{
		open("provider:openrouter", "", "OpenRouter is unreachable with Wrench's key; inference will fail"),
		open("openrouter:balance", "", "OpenRouter balance is $1.20, below $5"),
		open("config:invalid", "", "configuration change rejected: secret path /etc/wrench"),
		open("budget:global::day:exhausted", "", "global day budget is exhausted: $16 of $16"),
		open("budget:user:b@x:day:exhausted", "b@x", "b@x's day budget is exhausted: $1 of $1"),
		open("budget:user:a@x:week:exhausted", "a@x", "a@x's week budget is exhausted: $5 of $5"),
		{Key: "budget:user:a@x:day:80", UserEmail: "a@x", Severity: store.SeverityWarning, Message: "a@x's day budget reached 80%: $1"},
		{Key: "backup", Severity: store.SeverityCritical, Message: "backup: disk full", ResolvedAt: &resolved},
	}
	user := []string{
		"OpenRouter is unreachable; requests may fail",
		"The shared day budget is exhausted; requests are paused until it resets",
		"Your week budget is exhausted; requests are paused until it resets",
	}
	tests := []struct {
		name   string
		viewer string
		admin  bool
		in     []store.Incident
		want   []string
	}{
		{name: "user sees only the fixed texts", viewer: "a@x", in: incidents, want: user},
		{name: "other user", viewer: "b@x", in: incidents, want: []string{user[0], user[1],
			"Your day budget is exhausted; requests are paused until it resets"}},
		{name: "a leftover unsettled incident is not shown", viewer: "a@x",
			in: []store.Incident{open("accounting:unsettled", "", "unknown since 2026-11-10")}},
		{name: "prefix of another email is not the viewer", viewer: "a@x", in: []store.Incident{
			open("budget:user:aa@x:day:exhausted", "aa@x", "x")}},
		{name: "admin gets every open critical incident verbatim", viewer: "root@x", admin: true, in: incidents, want: []string{
			incidents[0].Message, incidents[1].Message, incidents[2].Message, incidents[3].Message, incidents[4].Message, incidents[5].Message}},
		{name: "windows read as words", viewer: "a@x", in: []store.Incident{
			open("budget:global::5_hours:exhausted", "", "x"), open("budget:user:a@x:month:exhausted", "a@x", "x")},
			want: []string{"The shared 5-hour budget is exhausted; requests are paused until it resets",
				"Your month budget is exhausted; requests are paused until it resets"}},
		{name: "none", viewer: "a@x", want: []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bannerAlerts(tc.in, tc.viewer, tc.admin)
			messages := []string{}
			for _, a := range got {
				messages = append(messages, a.Message)
				if !tc.admin && strings.Contains(a.Message, "$") {
					t.Errorf("amount leaked to a user: %q", a.Message)
				}
			}
			if !slices.Equal(messages, tc.want) {
				t.Fatalf("alerts = %q, want %q", messages, tc.want)
			}
		})
	}
}

func TestStatusAlertsFollowTheViewer(t *testing.T) {
	srv, _ := dashboardServer(t)
	var user, admin api.Status
	get(t, srv, "/api/status", "u1", &user)
	get(t, srv, "/api/status", "u3", &admin)
	if len(user.Alerts) != 1 || user.Alerts[0].Message != "OpenRouter is unreachable; requests may fail" ||
		len(admin.Alerts) != 1 || admin.Alerts[0].Message != "down" {
		t.Fatalf("alerts = %+v / %+v", user.Alerts, admin.Alerts)
	}
}
