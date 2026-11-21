package mocks

import (
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/ordo-systems/wrench/internal/config/configtest"
	mockgitlab "github.com/ordo-systems/wrench/internal/mock/gitlab"
	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
	"github.com/ordo-systems/wrench/internal/store/storetest"
)

func TestSeedFillsAnEmptyDatabaseOnce(t *testing.T) {
	st := storetest.Open(t) // skips without a test database
	cfg := configtest.Load(t, configtest.YAML)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	seeded, err := Seed(ctx, st, cfg, testIssuer, now)
	if err != nil || !seeded {
		t.Fatalf("first seed = %v, %v", seeded, err)
	}
	month := store.AttemptFilter{From: now.AddDate(0, 0, -31), To: now.Add(time.Minute)}
	total, err := st.SumUsage(ctx, month)
	if err != nil || total.Attempts < 100 || total.Pending != 0 {
		t.Fatalf("totals = %+v, %v", total, err)
	}
	byUser, _ := st.SumUsageByUser(ctx, month)
	if len(byUser) != len(cfg.Users) {
		t.Fatalf("users with attempts = %d, want %d", len(byUser), len(cfg.Users))
	}
	stats, err := st.Stats(ctx, month, 24*time.Hour)
	if err != nil || stats.Total.Errors == 0 || stats.Total.Success == 0 {
		t.Fatalf("stats = %+v, %v", stats.Total, err)
	}

	// The first user's current 5-hour window is past 80% (and every window below 100%), so the alert runner raises a
	// real budget warning without blocking the user; the seed itself writes no incident.
	first := cfg.Users[0]
	limits := cfg.UserLimits(first)
	for _, w := range []struct {
		window store.Window
		limit  money.Amount
		warn   bool
	}{{store.WindowFiveHours, limits.FiveHours, true}, {store.WindowDay, limits.Day, false},
		{store.WindowWeek, limits.Week, false}, {store.WindowMonth, limits.Month, false}} {
		from, to := w.window.Bounds(now)
		spent, err := st.SumUsage(ctx, store.AttemptFilter{UserEmail: first.Email, From: from, To: to})
		if err != nil || spent.Cost >= w.limit || (w.warn && spent.Cost*100 < w.limit*80) {
			t.Fatalf("%s %s spend %s of %s: %v", first.Email, w.window, spent.Cost, w.limit, err)
		}
	}
	if incidents, err := st.ListIncidents(ctx, "", 10); err != nil || len(incidents) != 0 {
		t.Fatalf("seed wrote incidents: %+v, %v", incidents, err)
	}

	again, err := Seed(ctx, st, cfg, testIssuer, now)
	if err != nil || again {
		t.Fatalf("second seed = %v, %v", again, err)
	}
	if after, _ := st.SumUsage(ctx, month); after.Attempts != total.Attempts {
		t.Fatalf("second seed wrote attempts: %d -> %d", total.Attempts, after.Attempts)
	}
}

const testIssuer = "http://gitlab:9000"

var agentPattern = regexp.MustCompile(`^opencode/2\.3\.\d+$`)

func TestSeedWritesKeysAndAgents(t *testing.T) {
	st := storetest.Open(t) // skips without a test database
	cfg := configtest.Load(t, configtest.YAML)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	if _, err := Seed(ctx, st, cfg, testIssuer, now); err != nil {
		t.Fatal(err)
	}

	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != len(cfg.Users) {
		t.Fatalf("users = %d, %v; want %d", len(users), err, len(cfg.Users))
	}
	for _, u := range users {
		// A later real sign-in through the demo GitLab mock must bind to the seeded row.
		id := mockgitlab.AccountID(u.Email)
		if u.Issuer != testIssuer || u.Subject != strconv.FormatInt(id, 10) || u.GitLabID != id {
			t.Fatalf("user %s identity = %q %q %d", u.Email, u.Issuer, u.Subject, u.GitLabID)
		}
		keys, err := st.ListClientKeys(ctx, u.Email)
		if err != nil || len(keys) < 2 {
			t.Fatalf("%s keys = %d, %v", u.Email, len(keys), err)
		}
		revoked, expired := 0, 0
		for _, k := range keys {
			if k.RevokedAt != nil {
				revoked++
			}
			if k.RevokedAt == nil && !k.ExpiresAt.After(now) {
				expired++
			}
		}
		if revoked == 0 || expired == 0 {
			t.Fatalf("%s revoked = %d, expired = %d", u.Email, revoked, expired)
		}
		if _, err := st.BindIdentity(ctx, u.Email, testIssuer, u.Subject, u.GitLabID, now); err != nil {
			t.Fatalf("real sign-in of %s: %v", u.Email, err)
		}
	}

	sessions, err := st.ListSessions(ctx, "", "", time.Time{}, 100000)
	if err != nil || len(sessions) == 0 {
		t.Fatalf("sessions = %d, %v", len(sessions), err)
	}
	lasting := 0
	for _, s := range sessions {
		if s.DeviceName == "" || !agentPattern.MatchString(s.Agent) {
			t.Fatalf("session %s device %q agent %q", s.ID, s.DeviceName, s.Agent)
		}
		if s.LastSeenAt.After(s.StartedAt) {
			lasting++
		}
	}
	// Sessions span their attempts, so the dashboard shows realistic durations rather than "0 min" everywhere.
	if lasting == 0 {
		t.Fatalf("no seeded session has last_seen_at after started_at (of %d)", len(sessions))
	}
}
