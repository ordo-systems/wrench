package httpapi

import (
	"context"
	"net/url"
	"time"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/backupstatus"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/version"
)

// statsBuckets are the bucket widths a client may ask for.
var statsBuckets = map[string]time.Duration{"5m": 5 * time.Minute, "hour": time.Hour, "day": 24 * time.Hour}

const (
	maxStatsBuckets = 500
	defaultPeriod   = 24 * time.Hour
)

// statsFilter parses the common dashboard filters; user is the scope already decided by scopeOf.
func statsFilter(user string, q api.GetStatsParams, now time.Time) (store.AttemptFilter, time.Duration, string) {
	bucket, ok := statsBuckets[deref(q.Bucket)]
	if deref(q.Bucket) == "" {
		bucket, ok = time.Hour, true
	}
	if !ok {
		return store.AttemptFilter{}, 0, "bucket must be 5m, hour or day"
	}
	to, from := now.Truncate(bucket).Add(bucket), time.Time{}
	var err error
	if v := deref(q.To); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			return store.AttemptFilter{}, 0, "to must be an RFC 3339 time"
		}
	}
	from = to.Add(-defaultPeriod)
	if v := deref(q.From); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			return store.AttemptFilter{}, 0, "from must be an RFC 3339 time"
		}
	}
	if !to.After(from) || to.Sub(from)/bucket > maxStatsBuckets {
		return store.AttemptFilter{}, 0, "the period must be positive and span at most 500 buckets"
	}
	f := store.AttemptFilter{UserEmail: user, From: from.UTC(), To: to.UTC(), Model: deref(q.Model),
		Provider: deref(q.Provider)}
	if s := deref(q.Session); s != "" {
		f.SessionIDs = []string{s}
	}
	return f, bucket, ""
}

// GetStats serves the aggregates behind the dashboard charts.
func (s *server) GetStats(ctx context.Context, req api.GetStatsRequestObject) (api.GetStatsResponseObject, error) {
	user, err := scopeOf(ctx, req.Params.User)
	if err != nil {
		return api.GetStats403JSONResponse{ForbiddenJSONResponse: forbiddenBody(err.Error())}, nil
	}
	f, bucket, problem := statsFilter(user, req.Params, s.deps.now())
	if problem != "" {
		return api.GetStats400JSONResponse{BadRequestJSONResponse: badRequestBody(problem)}, nil
	}
	view, refusal, err := s.statsWithSeries(ctx, req.Params.GroupBy, f, bucket)
	switch {
	case refusal != nil:
		return refusal, nil
	case err != nil:
		return api.GetStats503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	return api.GetStats200JSONResponse{From: f.From, To: f.To, BucketSeconds: int(bucket / time.Second), Stats: view}, nil
}

// seriesUsers is how many users get their own series; the rest are summed.
const seriesUsers = 8

// statsWithSeries loads the aggregates and, for group_by=user, the per-user series. The series list every user's
// spend, so only administrators may ask; a refusal or a bad value comes back as a response.
func (s *server) statsWithSeries(ctx context.Context, groupBy *api.GetStatsParamsGroupBy, f store.AttemptFilter,
	bucket time.Duration) (api.Stats, api.GetStatsResponseObject, error) {
	if groupBy != nil && *groupBy != api.GetStatsParamsGroupByUser {
		return api.Stats{}, api.GetStats400JSONResponse{BadRequestJSONResponse: badRequestBody("group_by must be user")}, nil
	}
	if id, _ := auth.FromContext(ctx); groupBy != nil && id.Role != config.RoleAdmin {
		return api.Stats{}, api.GetStats403JSONResponse{ForbiddenJSONResponse: forbiddenBody("only administrators may group by user")}, nil
	}
	st, err := s.deps.Store.Stats(ctx, f, bucket)
	if err != nil {
		return api.Stats{}, nil, err
	}
	view := statsView(st)
	if groupBy != nil {
		series, err := s.deps.Store.StatsByUser(ctx, f, bucket, seriesUsers)
		if err != nil {
			return api.Stats{}, nil, err
		}
		out := views(series, func(x store.UserSeries) api.UserSeries {
			return api.UserSeries{User: x.User, Buckets: views(x.Buckets, statsBucketView)}
		})
		view.Series = &out
	}
	return view, nil, nil
}

func limitsView(l config.Limits) api.Limits {
	return api.Limits{N5Hours: l.FiveHours.String(), Day: l.Day.String(), Week: l.Week.String(), Month: l.Month.String()}
}

// ListUsers shows the allowlist with stored access state and spend. The role is checked by guard.
func (s *server) ListUsers(ctx context.Context, _ api.ListUsersRequestObject) (api.ListUsersResponseObject, error) {
	cfg := config.For(ctx, s.deps.Config)
	states, err := s.deps.Store.UserStates(ctx, s.deps.now())
	if err != nil {
		return api.ListUsers503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	out := []api.UserRow{}
	for _, u := range cfg.Users {
		v := api.UserRow{Email: u.Email, Role: api.Role(u.Role), Tier: u.Tier, Limits: limitsView(cfg.UserLimits(u)),
			Usage: []api.BudgetWindow{}}
		if st, ok := states[u.Email]; ok {
			v.State = &api.UserState{Email: st.Email, Status: api.UserStateStatus(st.Status), StatusCheckedAt: st.StatusCheckedAt,
				ActiveKeys: st.ActiveKeys, LastKeyUseAt: st.LastKeyUseAt}
		}
		if s.deps.Budget != nil {
			usage, err := s.deps.Budget.Status(ctx, u.Email)
			if err != nil {
				return api.ListUsers503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
			}
			for _, x := range usage {
				if x.Scope == budget.ScopeUser {
					v.Usage = append(v.Usage, budgetWindowView(x))
				}
			}
		}
		out = append(out, v)
	}
	return api.ListUsers200JSONResponse{Users: out}, nil
}

func tierViews(tiers []config.Tier) []api.Tier {
	return views(tiers, func(t config.Tier) api.Tier {
		return api.Tier{Name: t.Name, Default: t.Default, Budget: limitsView(t.Budget),
			Models: views(t.Models, func(m config.TierModel) api.TierModel { return api.TierModel{Name: m.Name, Default: m.Default} })}
	})
}

// healthWindow is the recent period behind per-model route health; the API reports it as "1h".
const healthWindow = time.Hour

// GetSystem shows readiness, the read-only policy, route health per model and accounting quality.
// The role is checked by guard.
func (s *server) GetSystem(ctx context.Context, _ api.GetSystemRequestObject) (api.GetSystemResponseObject, error) {
	cfg := config.For(ctx, s.deps.Config)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	checks := api.Checks{"postgres": "ok", "openrouter": "ok"}
	if err := s.deps.Store.Ping(ctx); err != nil {
		checks["postgres"] = "unavailable"
		return api.GetSystem200JSONResponse{Checks: checks, Version: version.String()}, nil
	}
	if err := s.deps.UpstreamReady(ctx); err != nil {
		checks["openrouter"] = "unavailable"
	}
	health, err := s.deps.Store.ModelHealth(ctx, s.deps.now().Add(-healthWindow))
	if err != nil {
		return api.GetSystem503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	byModel := map[string]store.ModelHealth{}
	for _, h := range health {
		byModel[h.Model] = h
	}
	models := views(cfg.Models, func(m config.Model) api.SystemModel {
		e, status := s.deps.Catalog.Lookup(m.UpstreamID)
		v := api.SystemModel{Name: m.Name, InputTokenLimit: e.ContextLength, MaxOutputTokens: e.MaxCompletionTokens,
			ReasoningEffort: m.ReasoningEffort, PriceInputPerMtok: pricePerMillion(ctx, m.Name, e.PricePrompt),
			PriceOutputPerMtok: pricePerMillion(ctx, m.Name, e.PriceCompletion)}
		upstream, st := m.UpstreamID, api.SystemModelStatus(status)
		v.UpstreamId, v.Status = &upstream, &st
		if h, ok := byModel[m.Name]; ok {
			v.Health = &api.ModelHealth{Model: h.Model, Attempts: h.Attempts, Errors: h.Errors, FirstTokenP95Ms: h.FirstTokenP95,
				LastSuccessAt: h.LastSuccessAt, LastErrorAt: h.LastErrorAt, LastErrorReason: h.LastErrorReason}
		}
		return v
	})
	window, host := api.N1h, publicHost(s.deps.PublicURL)
	budgets := api.BudgetPolicy{WarnAtPercent: append([]int{}, cfg.Budget.WarnAtPercent...)}
	out := api.GetSystem200JSONResponse{
		Checks:       checks,
		Version:      version.String(),
		Models:       &models,
		HealthWindow: &window,
		Budgets:      &budgets,
		Alerts:       alertsView(cfg.Alerts),
		Backup:       backupView(s.deps.BackupStatusFile, s.deps.now()),
		PublicUrl:    &host,
	}
	st := s.deps.Config.Status()
	out.Configuration = &api.ReloadStatus{Revision: st.Revision, LoadedAt: st.LoadedAt}
	if st.Error != "" {
		out.Configuration.Error = &st.Error
	}
	tiers := tierViews(cfg.Tiers)
	out.Tiers = &tiers
	global := limitsView(cfg.Budget.GlobalLimits)
	budgets.Global = &global
	return out, nil
}

func alertsView(a *config.Alerts) *api.Alerts {
	if a == nil {
		return nil
	}
	v := &api.Alerts{Window: int64(a.Window), RepeatAfter: int64(a.RepeatAfter)}
	if a.LowBalance != nil {
		v.LowBalance = &api.LowBalanceAlert{Threshold: a.LowBalance.Threshold.String()}
	}
	if a.APIErrors != nil {
		v.ApiErrors = &api.APIErrorsAlert{Threshold: a.APIErrors.Threshold, MinAttempts: a.APIErrors.MinAttempts}
	}
	if a.FirstToken != nil {
		v.LlmFirstToken = &api.FirstTokenAlert{ThresholdP95: int64(a.FirstToken.ThresholdP95), MinAttempts: a.FirstToken.MinAttempts}
	}
	return v
}

// backupView reports the backup container's status: not_configured without a status file, attention with
// the problem when the last run failed or a backup is overdue, ok otherwise.
func backupView(path string, now time.Time) *api.Backup {
	if path == "" {
		return &api.Backup{Status: api.BackupStatusNotConfigured}
	}
	problem, err := backupstatus.Problem(path, now)
	if err != nil {
		problem = err.Error()
	}
	out := &api.Backup{Status: api.BackupStatusOk}
	if problem != "" {
		out = &api.Backup{Status: api.BackupStatusAttention, Problem: &problem}
	}
	if st, err := backupstatus.Read(path); err == nil {
		repos := map[string]api.BackupRepository{}
		for name, r := range st.Repositories {
			v := api.BackupRepository{Ok: r.OK}
			if r.Snapshot != "" {
				v.Snapshot = &r.Snapshot
			}
			if r.Error != "" {
				v.Error = &r.Error
			}
			repos[name] = v
		}
		next := st.NextRunAt
		out.LastSuccessAt, out.NextRunAt, out.Repositories = st.LastSuccessAt, &next, &repos
	}
	return out
}

func publicHost(public string) string {
	u, err := url.Parse(public)
	if err != nil {
		return ""
	}
	return u.Host
}

// ListAlerts returns open incidents first, then recent ones, scoped like the other lists. The role is checked by
// guard.
func (s *server) ListAlerts(ctx context.Context, req api.ListAlertsRequestObject) (api.ListAlertsResponseObject, error) {
	n, ok := parseLimit(req.Params.Limit)
	if !ok {
		return api.ListAlerts400JSONResponse{BadRequestJSONResponse: badRequestBody("limit must be 1..1000")}, nil
	}
	user, err := scopeOf(ctx, req.Params.User)
	if err != nil {
		return api.ListAlerts403JSONResponse{ForbiddenJSONResponse: forbiddenBody(err.Error())}, nil
	}
	incidents, err := s.deps.Store.ListIncidents(ctx, user, n)
	if err != nil {
		return api.ListAlerts503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	return api.ListAlerts200JSONResponse{Alerts: views(incidents, incidentView),
		Enabled: config.For(ctx, s.deps.Config).Alerts != nil}, nil
}
