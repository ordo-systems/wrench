// Package catalog keeps the OpenRouter model catalog: the token limits of upstream models and whether each
// configured model exists upstream. It fetches from OpenRouter and caches the last successful catalog
// in PostgreSQL so a restart with OpenRouter down keeps working.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/ordo-systems/wrench/internal/config"
	"github.com/ordo-systems/wrench/internal/store"
)

// Entry is one upstream model's token limits.
type Entry = store.CatalogModel

// Status says whether a configured model can be used.
type Status string

const (
	// StatusOK means the upstream model is in the catalog.
	StatusOK Status = "ok"
	// StatusUnknownUpstream means a catalog exists but lacks the upstream model.
	StatusUnknownUpstream Status = "unknown_upstream"
	// StatusNoCatalog means neither a fetch nor the cache produced a catalog.
	StatusNoCatalog Status = "no_catalog"
)

// Store persists the last successful catalog.
type Store interface {
	LoadModels(ctx context.Context) ([]Entry, time.Time, error)
	ReplaceModels(ctx context.Context, es []Entry, at time.Time) error
}

// Source fetches the current catalog.
type Source interface {
	Fetch(ctx context.Context) ([]Entry, error)
}

// Catalog is the in-memory catalog. It is safe for concurrent use.
type Catalog struct {
	src Source
	st  Store
	now func() time.Time

	// wake asks Run for an immediate refresh; buffered so RefreshNow never blocks.
	wake chan struct{}

	mu        sync.RWMutex
	entries   map[string]Entry
	fetchedAt time.Time
}

// New builds an empty catalog; call Load or Refresh (or Run) to fill it.
func New(src Source, st Store, now func() time.Time) *Catalog {
	return &Catalog{src: src, st: st, now: now, wake: make(chan struct{}, 1)}
}

// Load fills the catalog from the cache.
func (c *Catalog) Load(ctx context.Context) error {
	es, at, err := c.st.LoadModels(ctx)
	if err != nil {
		return err
	}
	if len(es) > 0 {
		c.set(es, at)
	}
	return nil
}

// Refresh fetches the catalog and, on success, replaces the cache and the in-memory copy.
func (c *Catalog) Refresh(ctx context.Context) error {
	es, err := c.src.Fetch(ctx)
	if err != nil {
		return err
	}
	// A degraded upstream answering with no usable models must not wipe a good cache.
	if len(es) == 0 {
		return errors.New("catalog: fetched catalog has no usable models; keeping the previous one")
	}
	at := c.now()
	if err := c.st.ReplaceModels(ctx, es, at); err != nil {
		return err
	}
	c.set(es, at)
	return nil
}

func (c *Catalog) set(es []Entry, at time.Time) {
	m := make(map[string]Entry, len(es))
	for _, e := range es {
		m[e.UpstreamID] = e
	}
	c.mu.Lock()
	c.entries, c.fetchedAt = m, at
	c.mu.Unlock()
}

// Lookup returns an upstream model's limits and status.
func (c *Catalog) Lookup(upstreamID string) (Entry, Status) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.entries == nil {
		return Entry{}, StatusNoCatalog
	}
	e, ok := c.entries[upstreamID]
	if !ok {
		return Entry{}, StatusUnknownUpstream
	}
	return e, StatusOK
}

// ModelStatus is a configured model's availability.
type ModelStatus struct {
	Name       string `json:"name"`
	UpstreamID string `json:"upstream_id"`
	Status     Status `json:"status"`
}

// Statuses reports the availability of each configured model, in the given order.
func (c *Catalog) Statuses(models []config.Model) []ModelStatus {
	out := make([]ModelStatus, 0, len(models))
	for _, m := range models {
		_, s := c.Lookup(m.UpstreamID)
		out = append(out, ModelStatus{Name: m.Name, UpstreamID: m.UpstreamID, Status: s})
	}
	return out
}

// Unavailable keeps the statuses that are not ok.
func Unavailable(ss []ModelStatus) []ModelStatus {
	var out []ModelStatus
	for _, s := range ss {
		if s.Status != StatusOK {
			out = append(out, s)
		}
	}
	return out
}

// FetchedAt is when the current catalog was fetched; zero when there is none.
func (c *Catalog) FetchedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fetchedAt
}

// Run loads the cache, refreshes, and refreshes again every period until ctx ends. onCheck runs after each
// refresh attempt, so callers can report unavailable models.
func (c *Catalog) Run(ctx context.Context, every time.Duration, onCheck func()) {
	if err := c.Load(ctx); err != nil {
		slog.ErrorContext(ctx, "model catalog cache unavailable", "err", err)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "model catalog refresh failed", "err", err)
		}
		if onCheck != nil {
			onCheck()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.wake:
		}
	}
}

// RefreshNow makes Run refresh at once, for example after the configuration changed its models.
func (c *Catalog) RefreshNow() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// openRouterSource reads OpenRouter's model list from <base>/models.
type openRouterSource struct {
	endpoint *url.URL
	key      string
	client   *http.Client
}

// NewOpenRouterSource fetches the catalog from OpenRouter's API base with the OpenRouter key.
func NewOpenRouterSource(base *url.URL, key string) Source {
	return &openRouterSource{endpoint: base.JoinPath("models"), key: key, client: &http.Client{Timeout: 15 * time.Second}}
}

func (s *openRouterSource) Fetch(ctx context.Context) ([]Entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog: status %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			TopProvider   struct {
				MaxCompletionTokens *int `json:"max_completion_tokens"`
			} `json:"top_provider"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("catalog: body: %w", err)
	}
	if body.Data == nil {
		return nil, errors.New("catalog: response has no data")
	}
	out := make([]Entry, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID == "" || d.ContextLength <= 0 {
			continue
		}
		e := Entry{UpstreamID: d.ID, ContextLength: d.ContextLength, MaxCompletionTokens: d.ContextLength,
			PricePrompt: d.Pricing.Prompt, PriceCompletion: d.Pricing.Completion}
		if mc := d.TopProvider.MaxCompletionTokens; mc != nil && *mc > 0 {
			e.MaxCompletionTokens = *mc
		}
		out = append(out, e)
	}
	return out, nil
}
