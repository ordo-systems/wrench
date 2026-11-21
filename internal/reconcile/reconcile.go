// Package reconcile settles costs after the fact from provider generation metadata: it confirms
// unknown costs, verifies reported ones (auditing corrections), fills the actual inference provider,
// and closes attempts that a crash left open. Lookups never create new inference attempts.
package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
)

// errNotYet means the provider has no metadata for the generation yet.
var errNotYet = errors.New("reconcile: generation metadata not available yet")

// Generation is the provider's own record of a generation.
type Generation struct {
	Cost     money.Amount
	Provider string
}

// Generations looks up generation metadata by id.
type Generations interface {
	Lookup(ctx context.Context, generationID string) (Generation, error)
}

// Store is the persistence reconciliation needs.
type Store interface {
	RecoverInterrupted(ctx context.Context, staleBefore, now time.Time) (int64, error)
	DueReconciliations(ctx context.Context, now time.Time, limit int) ([]store.Reconciliation, error)
	RescheduleReconciliation(ctx context.Context, attemptID string, next time.Time, reason string) error
	SettleAttempt(ctx context.Context, s store.Settlement) (bool, error)
}

// passResult summarizes one pass.
type passResult struct {
	Recovered int64
	Looked    int
	// Settled counts attempts whose charge changed: unknown costs confirmed or corrected.
	Settled int
	// Verified counts reported costs that matched the provider.
	Verified int
	Retried  int
	Failed   bool
}

const batch = 100

// Reconciler runs passes over due attempts.
type Reconciler struct {
	store Store
	gens  Generations
	// staleAfter is how long an attempt may run before it is presumed lost.
	staleAfter time.Duration
	now        func() time.Time
	log        *slog.Logger
}

// New builds a reconciler; staleAfter should exceed the longest allowed request.
func New(s Store, gens Generations, staleAfter time.Duration) *Reconciler {
	return &Reconciler{store: s, gens: gens, staleAfter: staleAfter, now: func() time.Time { return time.Now().UTC() }, log: slog.Default()}
}

// Run passes every interval until ctx ends.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		r.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runOnce recovers lost attempts and processes every due lookup once.
func (r *Reconciler) runOnce(ctx context.Context) passResult {
	var res passResult
	now := r.now()
	n, err := r.store.RecoverInterrupted(ctx, now.Add(-r.staleAfter), now)
	if err != nil {
		r.log.Error("reconcile: interrupted attempts not recovered", "err", err)
		return passResult{Failed: true}
	}
	if res.Recovered = n; n > 0 {
		r.log.Warn("reconcile: closed attempts left open by a restart", "count", n)
	}
	due, err := r.store.DueReconciliations(ctx, now, batch)
	if err != nil {
		r.log.Error("reconcile: due attempts not listed", "err", err)
		res.Failed = true
		return res
	}
	for _, d := range due {
		res.Looked++
		r.one(ctx, d, now, &res)
	}
	return res
}

func (r *Reconciler) one(ctx context.Context, d store.Reconciliation, now time.Time, res *passResult) {
	g, lookupErr := r.gens.Lookup(ctx, d.GenerationID)
	if lookupErr != nil {
		res.Retried++
		if err := r.store.RescheduleReconciliation(ctx, d.ID, nextTry(d, now), lookupErr.Error()); err != nil {
			r.log.Error("reconcile: retry not scheduled", "attempt", d.ID, "err", err)
		}
		if !errors.Is(lookupErr, errNotYet) {
			r.log.Warn("reconcile: lookup failed", "attempt", d.ID, "tries", d.Tries+1, "err", lookupErr)
		}
		return
	}
	changed, err := r.store.SettleAttempt(ctx, store.Settlement{AttemptID: d.ID, Cost: g.Cost, Source: store.ChargeGeneration,
		Provider: g.Provider, Actor: "reconciler", At: now})
	switch {
	case err != nil:
		r.log.Error("reconcile: settlement not stored", "attempt", d.ID, "err", err)
	case changed:
		res.Settled++
	default:
		res.Verified++
	}
}

// nextTry waits one schedule step from now. A lookup runs once its offset from the finish is due,
// so this keeps the schedule for fresh attempts, and attempts that finished long ago (after an
// outage or a restart) back off the same way instead of being retried on every pass.
func nextTry(d store.Reconciliation, now time.Time) time.Time {
	return now.Add(store.ReconcileDelay(d.Tries+1) - store.ReconcileDelay(d.Tries))
}

// OpenRouterGenerations reads OpenRouter generation metadata from <base>/generation.
type OpenRouterGenerations struct {
	endpoint *url.URL
	key      string
	client   *http.Client
}

// NewOpenRouterGenerations targets OpenRouter's API base, authenticating with the OpenRouter key.
func NewOpenRouterGenerations(base *url.URL, key string) *OpenRouterGenerations {
	return &OpenRouterGenerations{endpoint: base.JoinPath("generation"), key: key, client: &http.Client{Timeout: 15 * time.Second}}
}

// Lookup returns the generation's total cost and provider. 404 is errNotYet.
func (g *OpenRouterGenerations) Lookup(ctx context.Context, generationID string) (Generation, error) {
	u := *g.endpoint
	u.RawQuery = url.Values{"id": {generationID}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Generation{}, err
	}
	req.Header.Set("Authorization", "Bearer "+g.key)
	resp, err := g.client.Do(req)
	if err != nil {
		return Generation{}, fmt.Errorf("reconcile: lookup: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Generation{}, errNotYet
	case resp.StatusCode != http.StatusOK:
		return Generation{}, fmt.Errorf("reconcile: lookup status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			TotalCost    *json.Number `json:"total_cost"`
			ProviderName string       `json:"provider_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Generation{}, fmt.Errorf("reconcile: lookup body: %w", err)
	}
	if body.Data.TotalCost == nil {
		return Generation{}, errors.New("reconcile: generation metadata has no total_cost")
	}
	cost, err := money.ParseCost(*body.Data.TotalCost)
	if err != nil {
		return Generation{}, err
	}
	return Generation{Cost: cost, Provider: body.Data.ProviderName}, nil
}
