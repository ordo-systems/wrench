package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/config"
)

func TestScopeFor(t *testing.T) {
	user := auth.Identity{Email: "user1@example.com", Role: config.RoleUser}
	admin := auth.Identity{Email: "user3@example.com", Role: config.RoleAdmin}
	for _, tc := range []struct {
		name    string
		caller  auth.Identity
		user    string
		want    string
		wantErr error
	}{
		{"user default", user, "", "user1@example.com", nil},
		{"user own", user, "user1@example.com", "user1@example.com", nil},
		{"user own unnormalized", user, " User1@Example.com ", "user1@example.com", nil},
		{"user other", user, "user2@example.com", "", errForeignScope},
		{"user everyone", user, "*", "", errForeignScope},
		{"admin default", admin, "", "user3@example.com", nil},
		{"admin own", admin, "user3@example.com", "user3@example.com", nil},
		{"admin other", admin, "User2@example.com", "user2@example.com", nil},
		{"admin everyone", admin, "*", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scopeFor(tc.caller, tc.user)
			if got != tc.want || !errors.Is(err, tc.wantErr) {
				t.Fatalf("scopeFor(%q) = %q, %v; want %q, %v", tc.user, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestPricePerMillion(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     string // empty means null
		warns    bool
	}{
		{"typical input", "0.0000003", "0.3", false},
		{"typical output", "0.0000012", "1.2", false},
		{"opus output", "0.000075", "75", false},
		{"exponent notation", "3e-7", "0.3", false},
		{"sub-cent precision", "0.00000027", "0.27", false},
		{"zero", "0", "0", false},
		{"absent", "", "", false},
		{"garbage", "abc", "", true},
		{"negative", "-0.1", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			ctx := context.Background()
			prev := slog.Default()
			t.Cleanup(func() { slog.SetDefault(prev) })
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			got := pricePerMillion(ctx, "m", tc.in)
			if tc.want == "" && got != nil || tc.want != "" && (got == nil || *got != tc.want) {
				t.Fatalf("pricePerMillion(%q) = %v, want %q", tc.in, got, tc.want)
			}
			if warned := strings.Contains(logs.String(), "level=WARN"); warned != tc.warns {
				t.Fatalf("warned = %v, want %v: %s", warned, tc.warns, logs.String())
			}
		})
	}
}
