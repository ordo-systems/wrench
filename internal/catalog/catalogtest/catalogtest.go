// Package catalogtest builds model catalogs for tests of packages that consult token limits and availability.
package catalogtest

import (
	"context"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/catalog"
	"github.com/ordo-systems/wrench/internal/config"
)

// Default limits every configured model gets unless the test overrides it.
const (
	ContextLength       = 64000
	MaxCompletionTokens = 8192
)

type source []catalog.Entry

func (s source) Fetch(context.Context) ([]catalog.Entry, error) { return s, nil }

type memory struct{}

func (memory) LoadModels(context.Context) ([]catalog.Entry, time.Time, error) {
	return nil, time.Time{}, nil
}
func (memory) ReplaceModels(context.Context, []catalog.Entry, time.Time) error { return nil }

// For returns a catalog that knows every model of cfg with the default limits. overrides replace entries by
// upstream ID; an override with ContextLength 0 removes the model, making it unknown upstream.
func For(t testing.TB, cfg *config.Config, overrides ...catalog.Entry) *catalog.Catalog {
	t.Helper()
	byID := map[string]catalog.Entry{}
	for _, m := range cfg.Models {
		byID[m.UpstreamID] = catalog.Entry{UpstreamID: m.UpstreamID, ContextLength: ContextLength, MaxCompletionTokens: MaxCompletionTokens}
	}
	for _, o := range overrides {
		if o.ContextLength == 0 {
			delete(byID, o.UpstreamID)
			continue
		}
		byID[o.UpstreamID] = o
	}
	var es source
	for _, e := range byID {
		es = append(es, e)
	}
	c := catalog.New(es, memory{}, func() time.Time { return time.Now().UTC() })
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}
