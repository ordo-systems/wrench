package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestCatalogModelsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if got, at, err := s.LoadModels(ctx); err != nil || len(got) != 0 || !at.IsZero() {
		t.Fatalf("empty catalog = %v, %v, %v", got, at, err)
	}
	first := []CatalogModel{{UpstreamID: "a/x", ContextLength: 1000, MaxCompletionTokens: 100, PricePrompt: "0.0000003", PriceCompletion: "0.0000012"}, {UpstreamID: "b/y", ContextLength: 2000, MaxCompletionTokens: 2000}}
	if err := s.ReplaceModels(ctx, first, t0); err != nil {
		t.Fatal(err)
	}
	second := []CatalogModel{{UpstreamID: "c/z", ContextLength: 3000, MaxCompletionTokens: 300, PricePrompt: "0.0000005", PriceCompletion: "0"}}
	later := t0.Add(time.Hour)
	if err := s.ReplaceModels(ctx, second, later); err != nil {
		t.Fatal(err)
	}
	got, at, err := s.LoadModels(ctx)
	if err != nil || !slices.Equal(got, second) || !at.Equal(later) {
		t.Fatalf("catalog = %v, %v, %v", got, at, err)
	}
}

func TestCatalogPricesSurviveReload(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ms := []CatalogModel{{UpstreamID: "a/x", ContextLength: 1, MaxCompletionTokens: 1, PricePrompt: "0.0000003", PriceCompletion: "0.0000012"},
		{UpstreamID: "b/y", ContextLength: 1, MaxCompletionTokens: 1}}
	if err := s.ReplaceModels(ctx, ms, t0); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.LoadModels(ctx)
	if err != nil || !slices.Equal(got, ms) || got[1].PricePrompt != "" {
		t.Fatalf("prices = %+v, %v", got, err)
	}
}
