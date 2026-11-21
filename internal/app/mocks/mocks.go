// Package mocks runs the development mocks behind `wrench mock llm` and `wrench mock gitlab`.
package mocks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
	mockgitlab "github.com/ordo-systems/wrench/internal/mock/gitlab"
	"github.com/ordo-systems/wrench/internal/mock/llm"
)

// drain bounds graceful shutdown; mock requests are short except deliberately stalled streams.
const drain = 5 * time.Second

// GitLabOptions mirror the `mock gitlab` flags.
type GitLabOptions struct {
	Listen     string
	Issuer     string
	PublicURL  string
	ConfigPath string
}

// RunLLM serves the OpenRouter mock until ctx is cancelled.
func RunLLM(ctx context.Context, listen string) error {
	return serve(ctx, "llm", listen, llm.Handler())
}

// RunGitLab serves the GitLab mock until ctx is cancelled. The client credentials and sync token are the server's
// WRENCH_GITLAB_* variables; the picker lists the users of the Wrench configuration.
func RunGitLab(ctx context.Context, o GitLabOptions, lookupEnv func(string) (string, bool)) error {
	creds := map[string]string{}
	var missing []error
	for _, k := range []string{"WRENCH_GITLAB_CLIENT_ID", "WRENCH_GITLAB_CLIENT_SECRET", "WRENCH_GITLAB_SYNC_TOKEN"} {
		v, _ := lookupEnv(k)
		if v == "" {
			missing = append(missing, fmt.Errorf("%s is required", k))
		}
		creds[k] = v
	}
	if err := errors.Join(missing...); err != nil {
		return fmt.Errorf("mock gitlab: %w", err)
	}
	cfg, err := config.Load(o.ConfigPath)
	if err != nil {
		return err
	}
	emails := make([]string, 0, len(cfg.Users))
	for _, u := range cfg.Users {
		emails = append(emails, u.Email)
	}
	m, err := mockgitlab.New(mockgitlab.Options{
		Issuer: o.Issuer, PublicURL: o.PublicURL, ClientID: creds["WRENCH_GITLAB_CLIENT_ID"],
		ClientSecret: creds["WRENCH_GITLAB_CLIENT_SECRET"], SyncToken: creds["WRENCH_GITLAB_SYNC_TOKEN"],
		Directory: mockgitlab.ConfigDirectory(emails),
	})
	if err != nil {
		return err
	}
	return serve(ctx, "gitlab", o.Listen, m.Handler())
}

// serve runs h on addr until ctx is cancelled, then shuts down within drain.
func serve(ctx context.Context, name, addr string, h http.Handler) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mock %s: %w", name, err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("mock listening", "mock", name, "addr", ln.Addr().String())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
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
