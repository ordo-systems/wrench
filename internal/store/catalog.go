package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CatalogModel is one OpenRouter catalog entry: the token limits and prices Wrench needs for an upstream model.
// The prices are OpenRouter's decimal strings in USD per token, kept verbatim; empty when the catalog gave none.
type CatalogModel struct {
	UpstreamID          string
	ContextLength       int
	MaxCompletionTokens int
	PricePrompt         string
	PriceCompletion     string
}

// LoadModels returns the cached catalog sorted by upstream ID and when it was fetched; zero time when empty.
func (s *Store) LoadModels(ctx context.Context) ([]CatalogModel, time.Time, error) {
	rows, err := s.pool.Query(ctx, `SELECT upstream_id, context_length, max_completion_tokens, price_prompt, price_completion, fetched_at
		FROM openrouter_models ORDER BY upstream_id`)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("store: load models: %w", err)
	}
	defer rows.Close()
	var out []CatalogModel
	var at time.Time
	for rows.Next() {
		var m CatalogModel
		if err := rows.Scan(&m.UpstreamID, &m.ContextLength, &m.MaxCompletionTokens, &m.PricePrompt, &m.PriceCompletion, &at); err != nil {
			return nil, time.Time{}, fmt.Errorf("store: load models: %w", err)
		}
		out = append(out, m)
	}
	return out, at.UTC(), rows.Err()
}

// ReplaceModels swaps the cached catalog for ms in one transaction.
func (s *Store) ReplaceModels(ctx context.Context, ms []CatalogModel, at time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM openrouter_models`); err != nil {
			return fmt.Errorf("store: replace models: %w", err)
		}
		for _, m := range ms {
			if _, err := tx.Exec(ctx, `INSERT INTO openrouter_models (upstream_id, context_length, max_completion_tokens, price_prompt, price_completion, fetched_at)
				VALUES ($1, $2, $3, $4, $5, $6)`, m.UpstreamID, m.ContextLength, m.MaxCompletionTokens, m.PricePrompt, m.PriceCompletion, at); err != nil {
				return fmt.Errorf("store: replace models: %w", err)
			}
		}
		return nil
	})
}
