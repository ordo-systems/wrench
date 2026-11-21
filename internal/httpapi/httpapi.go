// Package httpapi assembles the HTTP surface: health checks, the inference proxy,
// dashboard JSON endpoints and the embedded dashboard files.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/login"
	"github.com/ordo-systems/wrench/internal/proxy"
	"github.com/ordo-systems/wrench/internal/store"
)

// Store is the read side of the journal used by the dashboard and readiness checks.
type Store interface {
	Ping(ctx context.Context) error
	ListAttempts(ctx context.Context, f store.AttemptFilter, limit int) ([]store.Attempt, error)
	ListSessions(ctx context.Context, userEmail, status string, offlineBefore time.Time, limit int) ([]store.Session, error)
	Stats(ctx context.Context, f store.AttemptFilter, bucket time.Duration) (store.Stats, error)
	StatsByUser(ctx context.Context, f store.AttemptFilter, bucket time.Duration, top int) ([]store.UserSeries, error)
	UserStates(ctx context.Context, now time.Time) (map[string]store.UserState, error)
	ModelHealth(ctx context.Context, from time.Time) ([]store.ModelHealth, error)
	ListIncidents(ctx context.Context, userEmail string, limit int) ([]store.Incident, error)
	SumUsage(ctx context.Context, f store.AttemptFilter) (store.UsageTotals, error)
	SumUsageByUser(ctx context.Context, f store.AttemptFilter) ([]store.UsageTotals, error)
	SumUsageByModel(ctx context.Context, f store.AttemptFilter) ([]store.UsageTotals, error)
	SumUsageBySession(ctx context.Context, f store.AttemptFilter) ([]store.UsageTotals, error)
	ListClientKeys(ctx context.Context, userEmail string) ([]store.ClientKey, error)
	RevokeClientKey(ctx context.Context, id int64, userEmail string, now time.Time) (string, error)
	RevokeClientKeys(ctx context.Context, userEmail string, now time.Time) (int64, error)
	RevokeUserCredentials(ctx context.Context, email string, at time.Time) (int64, error)
	DecideDeviceCode(ctx context.Context, userCodeHash []byte, userEmail string, approve bool, now time.Time) error
}

// Budget reports spend against limits.
type Budget interface {
	Status(ctx context.Context, email string) ([]budget.Usage, error)
}

// Registrar mounts routes that need no credential, such as GitLab sign-in.
type Registrar interface {
	Register(mux *http.ServeMux)
}

// Login is GitLab sign-in: its browser routes, and the CLI key exchange served from the API.
type Login interface {
	Registrar
	DeviceCode(ctx context.Context, clientID string) (login.DeviceCodeResponse, error)
	DeviceToken(ctx context.Context, req login.DeviceTokenRequest) (login.DeviceTokenResponse, error)
}

// Deps are the collaborators routed by the server.
type Deps struct {
	Store     Store
	Auth      auth.Authenticator
	Proxy     http.Handler
	Dashboard http.Handler
	// Config supplies tiers, models and users; handlers read one snapshot per request.
	Config *config.Holder
	// Catalog supplies upstream token limits and model availability.
	Catalog proxy.ModelCatalog
	// Login, when set, mounts the /auth routes and redeems CLI codes.
	Login Login
	// Budget, when set, serves /api/budget.
	Budget Budget
	// Events, when set, feeds /api/events; without it that endpoint answers 404.
	Events *events.Bus
	// Sessions sets when silent sessions turn offline.
	Sessions SessionTiming
	// BackupStatusFile is the backup container's status file, shown on /api/system; empty when not deployed.
	BackupStatusFile string
	// PublicURL is the origin cookie-authenticated mutations must come from.
	PublicURL string
	// OrgName is the organization name the OpenCode Console contract reports.
	OrgName string
	// UpstreamReady reports the cached OpenRouter probe result; it must not call OpenRouter itself.
	UpstreamReady func(ctx context.Context) error

	// now is the system clock in UTC; tests may replace it.
	now func() time.Time
}

const (
	defaultLimit = 100
	maxLimit     = 1000
)

// New returns the root handler. The documented API (api/openapi.yaml) is served by the generated router: the
// document decides which operations need a credential or the administrator role, and handlers are the methods
// of server. The inference proxy, the /auth sign-in routes and the dashboard files are routed here by hand.
// Non-admin callers only ever see their own data; admins may narrow with ?user=.
//
// Inference additionally requires a recently confirmed GitLab account state. Mutations authenticated
// by the session cookie must carry the public Origin.
func New(deps Deps) http.Handler {
	if deps.now == nil {
		deps.now = func() time.Time { return time.Now().UTC() }
	}
	// The document is embedded at build time, so failing to read it is a build defect, not a runtime condition.
	ops, err := apiOperations(api.Document)
	if err != nil {
		panic(err)
	}
	apiMux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(&server{deps: deps}, nil, strictOptions)
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseRouter: apiMux, ErrorHandlerFunc: badRequest})
	documented := guard(deps, newContract(ops), apiMux)

	mux := http.NewServeMux()
	for _, op := range ops {
		mux.Handle(op.Method+" "+op.Path, documented)
	}
	mux.Handle("GET /api/", http.NotFoundHandler())
	mux.HandleFunc("GET /zen/go/v1/usage", openCodeGoUsage)
	mux.Handle("POST /v1/chat/completions", auth.Middleware(deps.Auth, auth.RequireFreshStatus(deps.Proxy)))
	if deps.Login != nil {
		deps.Login.Register(mux)
	}
	mux.Handle("GET /", deps.Dashboard)
	if deps.Config == nil {
		return mux
	}
	// Every request runs against one configuration snapshot, even if a reload lands while it is served.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(config.WithSnapshot(r.Context(), deps.Config.Current())))
	})
}

// GetHealthz reports process liveness only; it does not touch dependencies.
func (s *server) GetHealthz(context.Context, api.GetHealthzRequestObject) (api.GetHealthzResponseObject, error) {
	return api.GetHealthz200JSONResponse{Status: api.HealthStatusOk}, nil
}

// GetReadyz fails when accounting storage or OpenRouter is unavailable, because inference must not be admitted then.
func (s *server) GetReadyz(ctx context.Context, _ api.GetReadyzRequestObject) (api.GetReadyzResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	checks := api.Checks{"postgres": "ok", "openrouter": "ok"}
	ready := true
	if err := s.deps.Store.Ping(ctx); err != nil {
		checks["postgres"], ready = "unavailable", false
	}
	if err := s.deps.UpstreamReady(ctx); err != nil {
		checks["openrouter"], ready = "unavailable", false
	}
	if !ready {
		return api.GetReadyz503JSONResponse(checks), nil
	}
	return api.GetReadyz200JSONResponse(checks), nil
}

func (s *server) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	id, _ := auth.FromContext(ctx)
	return api.GetMe200JSONResponse{Email: id.Email, Role: api.Role(id.Role), Credential: api.IdentityCredential(id.Credential),
		StatusFresh: id.StatusFresh}, nil
}

// ListModels publishes the caller's tier models and its default, so clients never carry their own catalog.
// Upstream IDs stay on the server.
func (s *server) ListModels(ctx context.Context, _ api.ListModelsRequestObject) (api.ListModelsResponseObject, error) {
	cfg := config.For(ctx, s.deps.Config)
	id, _ := auth.FromContext(ctx)
	tier, _ := cfg.TierOf(id.Email)
	def := tier.DefaultModel()
	return api.ListModels200JSONResponse{Models: s.tierModels(ctx, cfg, id.Email), Default: &def}, nil
}

// tierModels lists the models of the user's tier that the upstream catalog has.
func (s *server) tierModels(ctx context.Context, cfg *config.Config, email string) []api.CatalogModel {
	tier, _ := cfg.TierOf(email)
	var models []config.Model
	for _, tm := range tier.Models {
		if m, ok := cfg.Model(tm.Name); ok {
			models = append(models, m)
		}
	}
	return s.catalogModels(ctx, models)
}

// catalogModels keeps the models the upstream catalog lists, with its token limits.
func (s *server) catalogModels(ctx context.Context, models []config.Model) []api.CatalogModel {
	out := []api.CatalogModel{}
	for _, m := range models {
		if e, status := s.deps.Catalog.Lookup(m.UpstreamID); status == catalog.StatusOK {
			out = append(out, api.CatalogModel{Name: m.Name, InputTokenLimit: e.ContextLength,
				MaxOutputTokens: e.MaxCompletionTokens, Reasoning: m.ReasoningEffort != "" && m.ReasoningEffort != "none",
				PriceInputPerMtok: pricePerMillion(ctx, m.Name, e.PricePrompt), PriceOutputPerMtok: pricePerMillion(ctx, m.Name, e.PriceCompletion)})
		}
	}
	return out
}

// ListRequests returns attempts, newest first. Unreported token fields are encoded as null, never as zero.
func (s *server) ListRequests(ctx context.Context, req api.ListRequestsRequestObject) (api.ListRequestsResponseObject, error) {
	n, ok := parseLimit(req.Params.Limit)
	if !ok {
		return api.ListRequests400JSONResponse{BadRequestJSONResponse: badRequestBody("limit must be 1..1000")}, nil
	}
	user, err := scopeOf(ctx, req.Params.User)
	if err != nil {
		return api.ListRequests403JSONResponse{ForbiddenJSONResponse: forbiddenBody(err.Error())}, nil
	}
	f := store.AttemptFilter{UserEmail: user}
	if session := deref(req.Params.Session); session != "" {
		f.SessionIDs = []string{session}
	}
	attempts, err := s.deps.Store.ListAttempts(ctx, f, n)
	if err != nil {
		return api.ListRequests503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	return api.ListRequests200JSONResponse{Attempts: views(attempts, attemptView)}, nil
}

// ListSessions returns recent sessions with their live status and usage totals.
func (s *server) ListSessions(ctx context.Context, req api.ListSessionsRequestObject) (api.ListSessionsResponseObject, error) {
	n, ok := parseLimit(req.Params.Limit)
	if !ok {
		return api.ListSessions400JSONResponse{BadRequestJSONResponse: badRequestBody("limit must be 1..1000")}, nil
	}
	status := ""
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	if status != "" && status != string(statusActive) && status != string(statusOffline) {
		return api.ListSessions400JSONResponse{BadRequestJSONResponse: badRequestBody("status must be active or offline")}, nil
	}
	user, err := scopeOf(ctx, req.Params.User)
	if err != nil {
		return api.ListSessions403JSONResponse{ForbiddenJSONResponse: forbiddenBody(err.Error())}, nil
	}
	now := s.deps.now()
	sessions, err := s.deps.Store.ListSessions(ctx, user, status, now.Add(-s.deps.Sessions.OfflineAfter), n)
	if err != nil {
		return api.ListSessions503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	byID, err := sessionTotals(ctx, s.deps.Store, user, sessions)
	if err != nil {
		return api.ListSessions503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	return api.ListSessions200JSONResponse{Sessions: views(sessions, func(x store.Session) api.Session {
		return sessionView(x, statusOf(x, now, s.deps.Sessions.OfflineAfter), byID[x.ID])
	})}, nil
}

// sessionTotals aggregates usage of the listed sessions only, keyed by session ID.
func sessionTotals(ctx context.Context, st Store, user string, sessions []store.Session) (map[string]store.UsageTotals, error) {
	byID := map[string]store.UsageTotals{}
	if len(sessions) == 0 {
		return byID, nil
	}
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	totals, err := st.SumUsageBySession(ctx, store.AttemptFilter{UserEmail: user, SessionIDs: ids})
	for _, t := range totals {
		byID[t.Key] = t
	}
	return byID, err
}

// GetUsage returns totals for the current fixed window, optionally grouped by user, session or model.
// Cost sums confirmed attempts; pending ones are counted separately.
func (s *server) GetUsage(ctx context.Context, req api.GetUsageRequestObject) (api.GetUsageResponseObject, error) {
	window, ok := parseWindow(req.Params.Window)
	if !ok {
		return api.GetUsage400JSONResponse{BadRequestJSONResponse: badRequestBody("window must be 5_hours, day, week or month")}, nil
	}
	groups := map[string]func(context.Context, store.AttemptFilter) ([]store.UsageTotals, error){
		"":        nil,
		"user":    s.deps.Store.SumUsageByUser,
		"session": s.deps.Store.SumUsageBySession,
		"model":   s.deps.Store.SumUsageByModel,
	}
	groupFn, ok := groups[deref(req.Params.Group)]
	if !ok {
		return api.GetUsage400JSONResponse{BadRequestJSONResponse: badRequestBody("group must be user, session or model")}, nil
	}
	user, err := scopeOf(ctx, req.Params.User)
	if err != nil {
		return api.GetUsage403JSONResponse{ForbiddenJSONResponse: forbiddenBody(err.Error())}, nil
	}
	start, end := window.Bounds(s.deps.now())
	f := store.AttemptFilter{UserEmail: user, From: start, To: end}
	total, err := s.deps.Store.SumUsage(ctx, f)
	if err != nil {
		return api.GetUsage503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	var grouped []store.UsageTotals
	if groupFn != nil {
		if grouped, err = groupFn(ctx, f); err != nil {
			return api.GetUsage503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
		}
	}
	return api.GetUsage200JSONResponse{Window: api.Window(window), Start: start, End: end, Total: usageTotalsView(total),
		Groups: views(grouped, usageTotalsView)}, nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
