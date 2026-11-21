package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
)

// This file converts domain values into the generated API types. Lists are built with make, so an empty result
// encodes as [] rather than null.

func badRequestBody(message string) api.BadRequestJSONResponse {
	return api.BadRequestJSONResponse(errorBody(message))
}

func forbiddenBody(message string) api.ForbiddenJSONResponse {
	return api.ForbiddenJSONResponse(errorBody(message))
}

func notFoundBody(message string) api.NotFoundJSONResponse {
	return api.NotFoundJSONResponse(errorBody(message))
}

// unavailableBody logs a storage failure and hides its detail from the client.
func unavailableBody(ctx context.Context, err error) api.UnavailableJSONResponse {
	slog.ErrorContext(ctx, "dashboard query failed", "err", err)
	return api.UnavailableJSONResponse(errorBody("storage is unavailable"))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// errForeignScope refuses a non-administrator who asks for someone else's data.
var errForeignScope = errors.New("only administrators may read other users' data")

// everyone is the ?user= value with which an administrator asks for every user's data.
const everyone = "*"

// scopeOf returns the store's user filter for the caller's ?user=; see scopeFor.
func scopeOf(ctx context.Context, user *string) (string, error) {
	id, _ := auth.FromContext(ctx)
	return scopeFor(id, deref(user))
}

// scopeFor decides whose data a list returns: the caller by default, another user or everyone ("") only for
// administrators. Defaulting to the caller keeps a forgotten parameter from showing other people's data.
func scopeFor(id auth.Identity, user string) (string, error) {
	if user == everyone {
		if id.Role != config.RoleAdmin {
			return "", errForeignScope
		}
		return "", nil
	}
	email := config.NormalizeEmail(user)
	if email == "" || email == id.Email {
		return id.Email, nil
	}
	if id.Role != config.RoleAdmin {
		return "", errForeignScope
	}
	return email, nil
}

// parseLimit reads ?limit=; empty means the default.
func parseLimit(v *string) (int, bool) {
	if deref(v) == "" {
		return defaultLimit, true
	}
	n, err := strconv.Atoi(*v)
	return n, err == nil && n > 0 && n <= maxLimit
}

// parseWindow validates client input; a failure becomes a documented 400 response.
func parseWindow(s string) (store.Window, bool) {
	w, err := store.ParseWindow(s)
	return w, err == nil
}

// pricePerMillion converts a stored per-token price to dollars per one million tokens. An empty price is
// unknown (nil); an unparseable one is unknown too and logged, because a bad catalog entry must not fail the API.
func pricePerMillion(ctx context.Context, model, price string) *api.Amount {
	if price == "" {
		return nil
	}
	a, err := money.ParsePerMillion(price)
	if err != nil {
		slog.WarnContext(ctx, "model price ignored", "model", model, "price", price, "err", err)
		return nil
	}
	return amountPtr(&a)
}

func amountPtr(a *money.Amount) *api.Amount {
	if a == nil {
		return nil
	}
	s := a.String()
	return &s
}

func tokensView(t store.Tokens) api.Tokens {
	return api.Tokens{Input: t.Input, Output: t.Output, Reasoning: t.Reasoning, CacheRead: t.CacheRead, CacheWrite: t.CacheWrite}
}

func attemptView(a store.Attempt) api.Attempt {
	return api.Attempt{Id: a.ID, RequestId: a.RequestID, UserEmail: a.UserEmail, SessionId: a.SessionID,
		RequestedModel: a.RequestedModel, UpstreamModel: a.UpstreamModel,
		Provider: a.Provider, InferenceProvider: a.InferenceProvider, GenerationId: a.GenerationID,
		StartedAt: a.StartedAt, FirstTokenAt: a.FirstTokenAt, FinishedAt: a.FinishedAt,
		Outcome: api.AttemptOutcome(a.Outcome), ErrorReason: a.ErrorReason, Tokens: tokensView(a.Tokens),
		Cost: amountPtr(a.Cost), CostState: api.AttemptCostState(a.CostState)}
}

func usageTotalsView(u store.UsageTotals) api.UsageTotals {
	v := api.UsageTotals{Tokens: tokensView(u.Tokens), Cost: u.Cost.String(), Attempts: u.Attempts, Pending: u.Pending}
	if u.Key != "" {
		v.Key = &u.Key
	}
	return v
}

func budgetWindowView(u budget.Usage) api.BudgetWindow {
	v := api.BudgetWindow{Scope: api.BudgetWindowScope(u.Scope), Window: api.Window(u.Window), Spent: u.Spent.String(),
		Limit: u.Limit.String(), Percent: u.Percent, ResetsAt: u.ResetsAt, Unsettled: u.Unsettled}
	if u.Threshold != 0 {
		v.Threshold = &u.Threshold
	}
	return v
}

// views converts a list, never returning nil.
func views[T, V any](in []T, view func(T) V) []V {
	out := make([]V, 0, len(in))
	for _, x := range in {
		out = append(out, view(x))
	}
	return out
}

func sessionView(x store.Session, status liveStatus, usage store.UsageTotals) api.Session {
	return api.Session{Id: x.ID, UserEmail: x.UserEmail, StartedAt: x.StartedAt, LastSeenAt: x.LastSeenAt,
		DeviceId: x.DeviceID, DeviceName: x.DeviceName, Agent: x.Agent, Status: api.SessionStatus(status), Usage: usageTotalsView(usage)}
}

func clientKeyView(k store.ClientKey) api.ClientKey {
	return api.ClientKey{Id: k.ID, UserEmail: k.UserEmail, DeviceId: k.DeviceID, DeviceName: k.DeviceName,
		CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt, RevokedAt: k.RevokedAt}
}

func incidentView(i store.Incident) api.Incident {
	return api.Incident{Id: i.ID, Key: i.Key, Severity: api.IncidentSeverity(i.Severity), UserEmail: i.UserEmail,
		Message: i.Message, OpenedAt: i.OpenedAt, LastSeenAt: i.LastSeenAt, LastNotifiedAt: i.LastNotifiedAt,
		ResolvedAt: i.ResolvedAt, ResolveNotified: i.ResolveNotified}
}

func observedView(o store.Observed) api.Observed {
	return api.Observed{Sum: o.Sum, Reported: o.Reported}
}

func latencyView(l store.Latency) api.Latency {
	return api.Latency{Observed: l.Observed, P50Ms: l.P50, P95Ms: l.P95}
}

func statsBucketView(b store.StatsBucket) api.StatsBucket {
	return api.StatsBucket{Start: b.Start, Attempts: b.Attempts, Requests: b.Requests, Success: b.Success, Errors: b.Errors,
		Cancelled: b.Cancelled, Running: b.Running, Pending: b.Pending, Confirmed: b.Confirmed, Cost: b.Cost.String(),
		InputTokens: observedView(b.Input), OutputTokens: observedView(b.Output),
		ReasoningTokens: observedView(b.Reasoning), CacheReadTokens: observedView(b.CacheRead),
		CacheWriteTokens: observedView(b.CacheWrite), CachedInput: b.CachedInput, CacheableInput: b.CacheableInput,
		FirstToken: latencyView(b.FirstToken), Duration: latencyView(b.Duration), ActiveSessions: b.ActiveSessions}
}

func breakdownView(b store.Breakdown) api.Breakdown {
	return api.Breakdown{Key: b.Key, Attempts: b.Attempts, Pending: b.Pending, Cost: b.Cost.String()}
}

func statsView(st store.Stats) api.Stats {
	return api.Stats{Buckets: views(st.Buckets, statsBucketView), Total: statsBucketView(st.Total),
		ByUser: views(st.ByUser, breakdownView), ByModel: views(st.ByModel, breakdownView),
		ByProvider: views(st.ByProvider, breakdownView),
		Sessions: api.SessionCosts{Sessions: st.Sessions.Sessions, Incomplete: st.Sessions.Incomplete,
			Average: amountPtr(st.Sessions.Average), P50: amountPtr(st.Sessions.P50), P95: amountPtr(st.Sessions.P95)},
		OldestPending: st.OldestPending}
}
