// Package storetest provides migrated, disposable stores for tests of packages that use the store.
package storetest

import (
	"context"
	"testing"

	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/pgtest"
)

// Open creates a fresh migrated store and drops its database when the test ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, pgtest.Database(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
