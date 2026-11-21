// Package alerts turns the journal, budgets and dependencies into alert incidents and notifications.
// Each check derives the conditions that currently hold; the store keeps one incident per continuing
// condition, so a problem is announced when it starts, repeated at most once per repeat period and
// announced again when it clears.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ordo-systems/wrench/internal/backupstatus"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
)

// Store is the journal and incident storage the checks need.
type Store interface {
	Ping(ctx context.Context) error
	Stats(ctx context.Context, f store.AttemptFilter, bucket time.Duration) (store.Stats, error)
	SyncIncidents(ctx context.Context, at time.Time, active []store.Condition) (bool, error)
	DueIncidents(ctx context.Context, at time.Time, repeatAfter time.Duration) ([]store.Incident, error)
	MarkNotified(ctx context.Context, id int64, at time.Time) error
}

// Budgets reports spend against limits for a user, including the global windows.
type Budgets interface {
	Status(ctx context.Context, email string) ([]budget.Usage, error)
}

// Balance reports the remaining OpenRouter credit.
type Balance interface {
	Balance(ctx context.Context) (money.Amount, error)
}

// Models reports configured model availability and when the model catalog was last fetched.
type Models interface {
	Statuses(models []config.Model) []catalog.ModelStatus
	FetchedAt() time.Time
}

// catalogStaleAfter is how old the last successful catalog fetch may get before it alerts.
const catalogStaleAfter = 24 * time.Hour

// Notifier delivers one alert text to the configured channel.
type Notifier interface {
	Notify(ctx context.Context, text string) error
}

// snapshot is everything one check looks at.
type snapshot struct {
	// usage holds each allowlisted user's windows; global windows appear once under the empty key.
	usage    map[string][]budget.Usage
	recent   store.StatsBucket
	balance  *money.Amount
	upstream error
	// backup is a problem with the last backup, empty when healthy or not deployed.
	backup string
	// unavailable are configured models that are not ok; catalogFetched is the last successful fetch (zero: none).
	unavailable    []catalog.ModelStatus
	catalogFetched time.Time
	// configError is the last rejected configuration change, empty when the file in force is valid.
	configError string
}

// evaluate returns the conditions that hold in s. It has no side effects.
func evaluate(cfg *config.Alerts, s snapshot, now time.Time) []store.Condition {
	var out []store.Condition
	for _, email := range slices.Sorted(maps.Keys(s.usage)) {
		for _, u := range s.usage[email] {
			if c, ok := budgetCondition(email, u); ok {
				out = append(out, c)
			}
		}
	}
	if s.upstream != nil {
		out = append(out, store.Condition{Key: "provider:openrouter", Severity: store.SeverityCritical,
			Message: "OpenRouter is unreachable with Wrench's key; inference will fail"})
	}
	if lb := cfg.LowBalance; lb != nil && s.balance != nil && *s.balance < lb.Threshold {
		out = append(out, store.Condition{Key: "openrouter:balance", Severity: store.SeverityCritical,
			Message: fmt.Sprintf("OpenRouter balance is $%s, below $%s", s.balance, lb.Threshold)})
	}
	out = append(out, providerConditions(cfg, s.recent)...)
	if s.backup != "" {
		out = append(out, store.Condition{Key: "backup", Severity: store.SeverityCritical, Message: "backup: " + s.backup})
	}
	if s.configError != "" {
		out = append(out, store.Condition{Key: "config:invalid", Severity: store.SeverityCritical,
			Message: "configuration change rejected; the previous configuration stays in force: " + s.configError})
	}
	if c, ok := modelCondition(s, now); ok {
		out = append(out, c)
	}
	return out
}

// modelCondition alerts while a configured model is unavailable or the catalog has not been fetched for a day.
func modelCondition(s snapshot, now time.Time) (store.Condition, bool) {
	var problems []string
	if len(s.unavailable) > 0 {
		names := make([]string, 0, len(s.unavailable))
		for _, m := range s.unavailable {
			names = append(names, fmt.Sprintf("%s (%s)", m.Name, m.Status))
		}
		problems = append(problems, "models unavailable: "+strings.Join(names, ", "))
	}
	if !s.catalogFetched.IsZero() && now.Sub(s.catalogFetched) > catalogStaleAfter {
		problems = append(problems, fmt.Sprintf("model catalog was last fetched %s ago", now.Sub(s.catalogFetched).Round(time.Minute)))
	}
	if len(problems) == 0 {
		return store.Condition{}, false
	}
	return store.Condition{Key: "models:catalog", Severity: store.SeverityCritical, Message: strings.Join(problems, "; ")}, true
}

// budgetCondition alerts on exhausted windows and on reached warn_at_percent thresholds. The threshold is
// part of the key, so crossing a higher one is a new incident.
func budgetCondition(email string, u budget.Usage) (store.Condition, bool) {
	who := "global"
	if u.Scope != budget.ScopeGlobal {
		who = email + "'s"
	}
	reset := u.ResetsAt.Format(time.RFC3339)
	key := fmt.Sprintf("budget:%s:%s:%s", u.Scope, email, u.Window)
	switch {
	case u.Spent >= u.Limit:
		return store.Condition{Key: key + ":exhausted", Severity: store.SeverityCritical, UserEmail: email,
			Message: fmt.Sprintf("%s %s budget is exhausted: $%s of $%s, resets %s", who, u.Window, u.Spent, u.Limit, reset)}, true
	case u.Threshold > 0:
		return store.Condition{Key: fmt.Sprintf("%s:%d", key, u.Threshold), Severity: store.SeverityWarning, UserEmail: email,
			Message: fmt.Sprintf("%s %s budget reached %d%%: $%s of $%s, resets %s", who, u.Window, u.Percent, u.Spent, u.Limit, reset)}, true
	}
	return store.Condition{}, false
}

// providerConditions checks the recent error rate and first-token latency, once enough attempts finished.
func providerConditions(cfg *config.Alerts, recent store.StatsBucket) []store.Condition {
	var out []store.Condition
	finished := recent.Success + recent.Errors
	if e := cfg.APIErrors; e != nil && finished >= int64(e.MinAttempts) &&
		float64(recent.Errors)*100 >= float64(finished)*e.Threshold {
		out = append(out, store.Condition{Key: "provider:errors", Severity: store.SeverityWarning,
			Message: fmt.Sprintf("%d of %d requests failed in the last %s", recent.Errors, finished, cfg.Window)})
	}
	ft := recent.FirstToken
	if f := cfg.FirstToken; f != nil && ft.P95 != nil && ft.Observed >= int64(f.MinAttempts) &&
		*ft.P95 > float64(f.ThresholdP95.Milliseconds()) {
		out = append(out, store.Condition{Key: "provider:latency", Severity: store.SeverityWarning,
			Message: fmt.Sprintf("p95 time to first token is %.1fs over the last %s (limit %s)", *ft.P95/1000, cfg.Window, f.ThresholdP95)})
	}
	return out
}

// Runner checks conditions periodically and delivers due notifications.
type Runner struct {
	cfg      *config.Holder
	store    Store
	budgets  Budgets
	balance  Balance
	models   Models
	upstream func(ctx context.Context) error
	notifier Notifier
	now      func() time.Time
	log      *slog.Logger
	// backupStatus is the backup container's status file; empty when backup is not deployed.
	backupStatus string
	events       events.Publisher
	// storageNotifiedAt throttles the storage-down alert, which cannot be stored while storage is down.
	storageNotifiedAt time.Time
}

// New builds a runner; balance and models may be nil when they cannot be checked, and backupStatus empty
// when no backup container is deployed. ev, which may be nil, hears when incidents open or resolve.
func New(cfg *config.Holder, s Store, b Budgets, balance Balance, models Models, upstream func(context.Context) error, n Notifier, backupStatus string, ev events.Publisher) *Runner {
	return &Runner{cfg: cfg, store: s, budgets: b, balance: balance, models: models, upstream: upstream, notifier: n, backupStatus: backupStatus,
		events: ev, now: func() time.Time { return time.Now().UTC() }, log: slog.Default()}
}

// Run checks every interval until ctx ends.
func (r *Runner) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := r.Tick(ctx); err != nil {
			r.log.Error("alert check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one check against the configuration in force; it does nothing while config.yaml has no alerts
// section. Storage failure is alerted directly, throttled in memory.
func (r *Runner) Tick(ctx context.Context) error {
	now := r.now()
	cfg := r.cfg.Current()
	policy := cfg.Alerts
	if policy == nil {
		return nil
	}
	// Budget lookups in this check read the same snapshot.
	ctx = config.WithSnapshot(ctx, cfg)
	if err := r.store.Ping(ctx); err != nil {
		if now.Sub(r.storageNotifiedAt) >= policy.RepeatAfter {
			if r.notifier.Notify(ctx, "[WRENCH CRITICAL] accounting storage is unavailable; inference is refused") == nil {
				r.storageNotifiedAt = now
			}
		}
		return fmt.Errorf("alerts: storage: %w", err)
	}
	r.storageNotifiedAt = time.Time{}
	snap, err := r.snapshot(ctx, cfg, now)
	if err != nil {
		return err
	}
	changed, err := r.store.SyncIncidents(ctx, now, evaluate(policy, snap, now))
	if err != nil {
		return err
	}
	if changed && r.events != nil {
		r.events.Publish(events.Alerts, "")
		r.events.Publish(events.Status, "")
	}
	due, err := r.store.DueIncidents(ctx, now, policy.RepeatAfter)
	if err != nil {
		return err
	}
	for _, i := range due {
		if err := r.notifier.Notify(ctx, text(i)); err != nil {
			r.log.Warn("alert not delivered; retrying next check", "key", i.Key, "err", err)
			continue
		}
		if err := r.store.MarkNotified(ctx, i.ID, now); err != nil {
			return err
		}
	}
	return nil
}

func text(i store.Incident) string {
	if i.ResolvedAt != nil {
		return "[WRENCH RESOLVED] " + i.Message
	}
	level := "WARNING"
	if i.Severity == store.SeverityCritical {
		level = "CRITICAL"
	}
	return fmt.Sprintf("[WRENCH %s] %s", level, i.Message)
}

func (r *Runner) snapshot(ctx context.Context, cfg *config.Config, now time.Time) (snapshot, error) {
	s := snapshot{usage: map[string][]budget.Usage{}}
	for i, u := range cfg.Users {
		usage, err := r.budgets.Status(ctx, u.Email)
		if err != nil {
			return s, err
		}
		for _, x := range usage {
			switch {
			case x.Scope != budget.ScopeGlobal:
				s.usage[u.Email] = append(s.usage[u.Email], x)
			case i == 0: // every user's status carries the same global windows
				s.usage[""] = append(s.usage[""], x)
			}
		}
	}
	window := cfg.Alerts.Window
	st, err := r.store.Stats(ctx, store.AttemptFilter{From: now.Add(-window), To: now}, window)
	if err != nil {
		return s, err
	}
	s.recent = st.Total
	s.upstream = r.upstream(ctx)
	s.configError = r.cfg.Status().Error
	if r.models != nil {
		s.unavailable = catalog.Unavailable(r.models.Statuses(cfg.Models))
		s.catalogFetched = r.models.FetchedAt()
	}
	if r.backupStatus != "" {
		if s.backup, err = backupstatus.Problem(r.backupStatus, now); err != nil {
			s.backup = err.Error()
		}
	}
	if r.balance != nil {
		b, err := r.balance.Balance(ctx)
		if err != nil {
			r.log.Warn("OpenRouter balance unavailable", "err", err)
		} else {
			s.balance = &b
		}
	}
	return s, nil
}
