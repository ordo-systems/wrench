// Package server runs the Wrench monolith: client API, OpenRouter proxy, accounting and the embedded dashboard.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/ordo-systems/wrench/internal/alerts"
	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/budget"
	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/env"
	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/gitlab"
	"github.com/ordo-systems/wrench/internal/httpapi"
	"github.com/ordo-systems/wrench/internal/login"
	"github.com/ordo-systems/wrench/internal/probe"
	"github.com/ordo-systems/wrench/internal/proxy"
	"github.com/ordo-systems/wrench/internal/reconcile"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/version"
	"github.com/ordo-systems/wrench/internal/web"
)

// reloadEvery is how often the configuration file is checked when no file event arrives.
const reloadEvery = 10 * time.Second

// Run validates the whole configuration before touching the database or opening the listener,
// so an invalid candidate configuration never replaces a running instance. Later edits of the configuration
// apply without a restart when valid. ready, when non-nil, receives the bound address once the server accepts
// connections.
func Run(ctx context.Context, configPath string, lookupEnv func(string) (string, bool), ready chan<- string) error {
	return run(ctx, configPath, lookupEnv, ready, reloadEvery)
}

// run is Run with the reload polling period as a parameter, so tests need not wait ten seconds.
func run(ctx context.Context, configPath string, lookupEnv func(string) (string, bool), ready chan<- string, every time.Duration) error {
	holder, e, err := loadConfiguration(configPath, lookupEnv)
	if err != nil {
		return err
	}
	cfg := holder.Current()

	st, err := store.Open(ctx, e.PostgresDSN.Reveal())
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	bus := events.New()
	gl := gitlab.New(e.GitLab, e.PublicURL)
	go login.NewSyncer(st, gl, e.GitLab.SyncInterval).Run(ctx)
	// An attempt still open after the longest allowed request plus a margin was lost to a restart.
	go reconcile.New(newPublishingReconcileStore(st, bus), reconcile.NewOpenRouterGenerations(e.Upstream.URL, e.Upstream.Key.Reveal()), e.Upstream.TimeoutTotal+time.Minute).
		Run(ctx, 5*time.Second)

	models := catalog.New(catalog.NewOpenRouterSource(e.Upstream.URL, e.Upstream.Key.Reveal()), st, func() time.Time { return time.Now().UTC() })
	// The cache serves requests until the first fetch finishes; Run reloads it and then refreshes hourly.
	if err := models.Load(ctx); err != nil {
		slog.Error("model catalog cache unavailable", "err", err)
	}
	go models.Run(ctx, time.Hour, func() { logUnavailable(holder.Current(), models) })
	go config.Watch(ctx, holder, configPath, every, func() {
		slog.Info("configuration reloaded", "revision", holder.Status().Revision)
		models.RefreshNow()
		bus.Publish(events.Status, "")
	})
	if unused := cfg.UnreferencedModels(); len(unused) > 0 {
		slog.Warn("models not used by any tier", "models", unused)
	}

	cookies := auth.Cookies{Secure: !e.HTTPOnly}
	for _, w := range startupWarnings(e) {
		slog.Warn(w)
	}
	checker := budget.NewChecker(holder, st)
	p := proxy.New(holder, models, e.Upstream, newPublishingJournal(st, bus), checker)
	balance := alerts.NewOpenRouterBalance(e.Upstream.URL, e.Upstream.Key.Reveal())
	// Readiness, status and alerts read the cached probe; reading the key checks both the network path and the key.
	upstream := probe.New(alerts.NewOpenRouterKey(e.Upstream.URL, e.Upstream.Key.Reveal()).Check,
		upstreamProbeInterval, upstreamProbeTimeout)
	upstream.OnChange(func() { bus.Publish(events.Status, "") })
	go upstream.Run(ctx)
	startAlerts(ctx, holder, e, st, checker, models, balance, upstream.Err, bus)
	srv := &http.Server{
		Handler: httpapi.New(httpapi.Deps{
			Store: st, Auth: auth.NewResolver(holder, st, cookies, e.GitLab.SyncInterval), Proxy: p, Dashboard: web.Handler(),
			Config: holder, Catalog: models, UpstreamReady: upstream.Err, PublicURL: e.PublicURL.Scheme + "://" + e.PublicURL.Host,
			Login: login.New(holder, st, gl, cookies, e.WebSessionTTL, e.ClientKeysTTL, e.PublicURL.Scheme+"://"+e.PublicURL.Host, bus), Budget: checker, Events: bus,
			Sessions: httpapi.SessionTiming(e.Sessions), BackupStatusFile: e.BackupStatusFile, OrgName: e.OrgName,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: streams are bounded by the upstream timeouts instead.
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", e.ListenAddr)
	if err != nil {
		return err
	}
	slog.Info("wrench listening", "version", version.String(), "addr", ln.Addr().String(), "users", len(cfg.Users), "models", len(cfg.Models))
	if ready != nil {
		ready <- ln.Addr().String()
	}
	return serve(ctx, srv, ln, e.Upstream.TimeoutTotal)
}

// startAlerts runs alert checks every minute; the runner skips its checks while config.yaml has no alerts
// section, so the section can be added or removed without a restart. Without a webhook, alerts are logged and
// still recorded as incidents for the dashboard.
func startAlerts(ctx context.Context, cfg *config.Holder, e env.Env, st *store.Store, b *budget.Checker, models alerts.Models,
	balance alerts.Balance, ready func(context.Context) error, ev events.Publisher) {
	if cfg.Current().Alerts == nil {
		slog.Warn("alerts are disabled until config.yaml has an alerts section")
	}
	var n alerts.Notifier = alerts.LogNotifier{Log: slog.Default()}
	if e.AlertWebhookURL != "" {
		n = alerts.NewWebhook(e.AlertWebhookURL.Reveal())
	}
	go alerts.New(cfg, st, b, balance, models, ready, n, e.BackupStatusFile, ev).Run(ctx, time.Minute)
}

// The OpenRouter probe's cadence: frequent enough for readiness, light on OpenRouter's per-IP burst limit for /key.
const (
	upstreamProbeInterval = 30 * time.Second
	upstreamProbeTimeout  = 5 * time.Second
)

// httpOnlyWarning is logged at every start in HTTP-only mode.
const httpOnlyWarning = "HTTP-only mode: session cookies are not Secure; never use it in production"

// startupWarnings lists the configuration risks to log when the server starts.
func startupWarnings(e env.Env) []string {
	if e.HTTPOnly {
		return []string{httpOnlyWarning}
	}
	return nil
}

// logUnavailable reports configured models the catalog cannot serve, after every catalog check.
func logUnavailable(cfg *config.Config, models *catalog.Catalog) {
	bad := catalog.Unavailable(models.Statuses(cfg.Models))
	if len(bad) == 0 {
		return
	}
	names := make([]string, 0, len(bad))
	for _, m := range bad {
		names = append(names, fmt.Sprintf("%s (%s: %s)", m.Name, m.UpstreamID, m.Status))
	}
	slog.Error("configured models unavailable", "models", names)
}

// loadConfiguration validates the configuration and the environment together, before any side effect other than
// configuring the logger.
func loadConfiguration(path string, lookupEnv func(string) (string, bool)) (*config.Holder, env.Env, error) {
	holder, err := config.NewHolder(path, func() time.Time { return time.Now().UTC() })
	if err != nil {
		return nil, env.Env{}, err
	}
	e, err := env.LoadEnv(lookupEnv)
	if err != nil {
		return nil, env.Env{}, err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel(e.LogLevel)})))
	return holder, e, nil
}

// serve runs until ctx is cancelled, then drains in-flight streams for up to drain.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down", "drain", drain.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Probe succeeds only on HTTP 200 within five seconds; container health checks use it instead of curl.
func Probe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
	}
	return nil
}

func logLevel(s string) slog.Level {
	var l slog.Level
	_ = l.UnmarshalText([]byte(s))
	return l
}
