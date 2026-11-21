package httpapi

import (
	"context"
	"slices"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
)

// GetBudget reports the caller's windows, or another user's for administrators. Global windows reveal everyone's spend, so only administrators get
// them; other users learn about a global refusal from its 402 message.
func (s *server) GetBudget(ctx context.Context, req api.GetBudgetRequestObject) (api.GetBudgetResponseObject, error) {
	if s.deps.Budget == nil {
		return api.GetBudget404JSONResponse{NotFoundJSONResponse: notFoundBody("this server has no budgets")}, nil
	}
	id, _ := auth.FromContext(ctx)
	email, err := scopeOf(ctx, req.Params.User)
	if err != nil {
		return api.GetBudget403JSONResponse{ForbiddenJSONResponse: forbiddenBody(err.Error())}, nil
	}
	if email == "" {
		return api.GetBudget400JSONResponse{BadRequestJSONResponse: badRequestBody("user must name one user, not *")}, nil
	}
	usage, err := s.deps.Budget.Status(ctx, email)
	if err != nil {
		return api.GetBudget503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	if id.Role != config.RoleAdmin {
		usage = slices.DeleteFunc(usage, func(u budget.Usage) bool { return u.Scope == budget.ScopeGlobal })
	}
	warn := slices.Clone(config.For(ctx, s.deps.Config).Budget.WarnAtPercent)
	if warn == nil {
		warn = []int{}
	}
	return api.GetBudget200JSONResponse{Windows: views(usage, budgetWindowView), WarnAtPercent: warn}, nil
}
