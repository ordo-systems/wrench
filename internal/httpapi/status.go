package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ordo-systems/wrench/api"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/version"
)

// statusTimeout bounds the status probes, so a hung dependency becomes a reason instead of a hung dashboard.
const statusTimeout = 3 * time.Second

// statusInputs are the observations behind a caller's pause reasons. Windows hold the caller's own and the global
// budget windows; global amounts never leave this package.
type statusInputs struct {
	StorageDown, UpstreamDown, StatusFresh bool
	Windows                                []budget.Usage
}

// pauseReasons decides why the caller's new requests would be refused, in a fixed order. With storage down
// nothing that depends on it is known, so only the caller's own GitLab status is added.
func pauseReasons(in statusInputs) []api.PauseReason {
	out := []api.PauseReason{}
	add := func(code api.PauseReasonCode) { out = append(out, api.PauseReason{Code: code}) }
	if in.StorageDown {
		add(api.StorageUnavailable)
		if !in.StatusFresh {
			add(api.GitlabStatusUnconfirmed)
		}
		return out
	}
	if in.UpstreamDown {
		add(api.UpstreamUnavailable)
	}
	if exhausted(in.Windows, budget.ScopeGlobal) {
		add(api.GlobalBudgetExhausted)
	}
	if exhausted(in.Windows, budget.ScopeUser) {
		add(api.OwnBudgetExhausted)
	}
	if !in.StatusFresh {
		add(api.GitlabStatusUnconfirmed)
	}
	return out
}

func exhausted(windows []budget.Usage, scope budget.Scope) bool {
	for _, w := range windows {
		if w.Scope == scope && w.Percent >= 100 {
			return true
		}
	}
	return false
}

// statusProbe runs one status check under its own statusTimeout, so a slow check cannot fail the next one.
func statusProbe(ctx context.Context, check func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	return check(ctx)
}

// GetStatus tells any caller whether their inference can run and which models their tier offers. Once the caller
// is authenticated it answers 200 even when a check fails, since that is when the caller most needs the reason;
// a storage outage that already fails authentication is answered 503 by the auth middleware.
func (s *server) GetStatus(ctx context.Context, _ api.GetStatusRequestObject) (api.GetStatusResponseObject, error) {
	id, _ := auth.FromContext(ctx)
	in := statusInputs{StatusFresh: id.StatusFresh}
	if err := statusProbe(ctx, s.deps.Store.Ping); err != nil {
		in.StorageDown = true
	} else {
		in.UpstreamDown = statusProbe(ctx, s.deps.UpstreamReady) != nil
		if s.deps.Budget != nil {
			err := statusProbe(ctx, func(ctx context.Context) (err error) {
				in.Windows, err = s.deps.Budget.Status(ctx, id.Email)
				return err
			})
			in.StorageDown = err != nil
		}
	}
	reasons := pauseReasons(in)
	state := api.InferenceStatusStateOk
	if len(reasons) > 0 {
		state = api.InferenceStatusStatePaused
	}
	return api.GetStatus200JSONResponse{Inference: api.InferenceStatus{State: state, Reasons: reasons},
		Models: s.tierModels(ctx, config.For(ctx, s.deps.Config), id.Email), Version: version.String(),
		Alerts: s.statusAlerts(ctx, id, in.StorageDown)}, nil
}

// maxBannerIncidents bounds the incidents read for the banner; open ones come first.
const maxBannerIncidents = 100

// statusAlerts lists the banner alerts the caller may see. A failed read gives none: the banner is advisory
// and the pause reasons already explain a storage outage.
func (s *server) statusAlerts(ctx context.Context, id auth.Identity, storageDown bool) []api.StatusAlert {
	if storageDown {
		return []api.StatusAlert{}
	}
	open, err := s.deps.Store.ListIncidents(ctx, "", maxBannerIncidents)
	if err != nil {
		return []api.StatusAlert{}
	}
	return bannerAlerts(open, id.Email, id.Role == config.RoleAdmin)
}

// bannerAlerts picks the open critical incidents a viewer may see. Administrators get each with its message.
// Everyone else gets fixed texts for the incidents that explain why their requests fail, and nothing else: the
// incident messages carry amounts, other users' addresses, balances and configuration errors.
func bannerAlerts(open []store.Incident, viewer string, admin bool) []api.StatusAlert {
	out := []api.StatusAlert{}
	for _, in := range open {
		if in.ResolvedAt != nil || in.Severity != store.SeverityCritical {
			continue
		}
		message := in.Message
		if !admin {
			var ok bool
			if message, ok = userBannerText(in.Key, viewer); !ok {
				continue
			}
		}
		out = append(out, api.StatusAlert{Key: in.Key, Severity: api.StatusAlertSeverityCritical, Message: message})
	}
	return out
}

// userBannerText is the amount-free text for an incident key, or false when the key is not the viewer's business.
func userBannerText(key, viewer string) (string, bool) {
	switch key {
	case "provider:openrouter":
		return "OpenRouter is unreachable; requests may fail", true
	}
	if window, ok := exhaustedWindow(key, "budget:global::"); ok {
		return fmt.Sprintf("The shared %s budget is exhausted; requests are paused until it resets", windowWords(window)), true
	}
	if window, ok := exhaustedWindow(key, "budget:user:"+viewer+":"); ok && viewer != "" {
		return fmt.Sprintf("Your %s budget is exhausted; requests are paused until it resets", windowWords(window)), true
	}
	return "", false
}

// windowWords names a budget window token in banner text: "5_hours" reads "5-hour"; day, week and month stay.
func windowWords(window string) string {
	if window == "5_hours" {
		return "5-hour"
	}
	return window
}

// exhaustedWindow extracts the window of an exhausted-budget key such as budget:global::day:exhausted.
func exhaustedWindow(key, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(key, prefix)
	if !ok {
		return "", false
	}
	window, ok := strings.CutSuffix(rest, ":exhausted")
	return window, ok && window != "" && !strings.Contains(window, ":")
}
