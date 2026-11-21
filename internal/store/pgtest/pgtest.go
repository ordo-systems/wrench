// Package pgtest creates disposable PostgreSQL databases for tests. It does not import package
// store, so the store's own in-package tests can use it without an import cycle.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// dsnVar names the server used for database tests. `make test` provides it;
// plain `go test` skips database tests without it.
const dsnVar = "WRENCH_TEST_POSTGRES_DSN"

// Database creates an empty database, returns its DSN and drops it when the test ends.
func Database(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(dsnVar)
	if dsn == "" {
		t.Skip(dsnVar + " is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "wrench_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	// Registered first, so it runs after the callers' cleanups have closed their pools.
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}
