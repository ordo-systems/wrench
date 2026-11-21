package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/login"
	"github.com/ordo-systems/wrench/internal/money"
)

// The OpenCode Console contract: a vanilla opencode signed in through the device login reads its provider,
// policies and budgets from these endpoints. Shapes follow opencode 2.3.

// consoleProvider is the provider id opencode shows and the policies allow.
const consoleProvider = "wrench"

func (s *server) GetConsoleUser(ctx context.Context, _ api.GetConsoleUserRequestObject) (api.GetConsoleUserResponseObject, error) {
	id, _ := auth.FromContext(ctx)
	return api.GetConsoleUser200JSONResponse{Id: strconv.FormatInt(id.UserID, 10), Email: id.Email}, nil
}

func (s *server) ListConsoleOrgs(context.Context, api.ListConsoleOrgsRequestObject) (api.ListConsoleOrgsResponseObject, error) {
	return api.ListConsoleOrgs200JSONResponse{{Id: login.OrgID, Name: s.deps.OrgName}}, nil
}

// GetConsoleConfig gives opencode one provider, Wrench, with the caller's catalog-listed tier models, and policies
// that forbid every other provider.
func (s *server) GetConsoleConfig(ctx context.Context, _ api.GetConsoleConfigRequestObject) (api.GetConsoleConfigResponseObject, error) {
	id, _ := auth.FromContext(ctx)
	models := map[string]api.ConsoleModel{}
	for _, m := range s.tierModels(ctx, config.For(ctx, s.deps.Config), id.Email) {
		models[m.Name] = api.ConsoleModel{Name: m.Name, Limit: api.ConsoleModelLimit{Context: m.InputTokenLimit, Output: m.MaxOutputTokens}}
	}
	return api.GetConsoleConfig200JSONResponse{
		Body: api.ConsoleConfig{
			Providers: map[string]api.ConsoleProvider{consoleProvider: {
				Name: "Wrench", Package: "@opencode/ai/providers/openai-compatible",
				Settings: api.ConsoleProviderSettings{BaseURL: s.deps.PublicURL + "/v1"}, Models: models,
			}},
			Policies: []api.ConsolePolicy{
				{Action: "provider.use", Resource: "*", Effect: api.ConsolePolicyEffectDeny},
				{Action: "provider.use", Resource: consoleProvider, Effect: api.ConsolePolicyEffectAllow},
			},
		},
		Headers: api.GetConsoleConfig200ResponseHeaders{XOpencodeInstructionsSkills: "1"},
	}, nil
}

// ListConsoleInstructions answers the fetch that the config header triggers; Wrench manages no instructions.
func (s *server) ListConsoleInstructions(context.Context, api.ListConsoleInstructionsRequestObject) (api.ListConsoleInstructionsResponseObject, error) {
	return api.ListConsoleInstructions200JSONResponse{Instructions: []api.ConsoleInstruction{}}, nil
}

func (s *server) ListConsoleSkills(context.Context, api.ListConsoleSkillsRequestObject) (api.ListConsoleSkillsResponseObject, error) {
	return api.ListConsoleSkills200JSONResponse{Skills: []api.ConsoleSkill{}}, nil
}

// ListConsoleBudgets turns the caller's budget windows into Console budget rules for opencode's /usage. Global
// windows reveal everyone's spend, so only administrators get them.
func (s *server) ListConsoleBudgets(ctx context.Context, _ api.ListConsoleBudgetsRequestObject) (api.ListConsoleBudgetsResponseObject, error) {
	if s.deps.Budget == nil {
		return api.ListConsoleBudgets200JSONResponse{}, nil
	}
	id, _ := auth.FromContext(ctx)
	usage, err := s.deps.Budget.Status(ctx, id.Email)
	if err != nil {
		return api.ListConsoleBudgets503JSONResponse{UnavailableJSONResponse: unavailableBody(ctx, err)}, nil
	}
	if id.Role != config.RoleAdmin {
		usage = slices.DeleteFunc(usage, func(u budget.Usage) bool { return u.Scope == budget.ScopeGlobal })
	}
	return api.ListConsoleBudgets200JSONResponse(views(usage, consoleBudgetView)), nil
}

func consoleBudgetView(u budget.Usage) api.ConsoleBudget {
	r := api.ConsoleBudget{RuleID: "user-" + string(u.Window), LimitDollars: dollars(u.Limit), SpentDollars: dollars(u.Spent),
		Scope: api.Each, Per: api.Set, Coverage: api.All, Models: []api.ConsoleBudgetModel{}, Vendors: []api.ConsoleVendor{},
		ResetsAt: u.ResetsAt}
	if u.Scope == budget.ScopeGlobal {
		r.RuleID, r.Scope, r.Shared = "global-"+string(u.Window), api.Combined, true
	}
	return r
}

// dollars converts an exact amount to the JSON number the contract carries, through its decimal form.
func dollars(a money.Amount) float64 {
	f, _ := strconv.ParseFloat(a.String(), 64)
	return f
}

// openCodeGoUsage answers opencode's OpenCode Go allowance check: Wrench has none, and 403 hides the section.
func openCodeGoUsage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusForbidden, errorBody("OpenCode Go is not available through Wrench"))
}
