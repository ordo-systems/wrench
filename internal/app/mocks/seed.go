package mocks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/ordo-systems/wrench/internal/auth"
	"github.com/ordo-systems/wrench/internal/config"
	mockgitlab "github.com/ordo-systems/wrench/internal/mock/gitlab"
	"github.com/ordo-systems/wrench/internal/money"
	"github.com/ordo-systems/wrench/internal/store"
)

// SeedStore is the part of the store the demo seed writes through, so seeded rows obey the same rules as real ones.
type SeedStore interface {
	SumUsage(ctx context.Context, f store.AttemptFilter) (store.UsageTotals, error)
	TouchSession(ctx context.Context, sessionID, userEmail string, keyID *int64, agent string, at time.Time) error
	StartAttempt(ctx context.Context, a store.Attempt) error
	FinishAttempt(ctx context.Context, id string, r store.AttemptResult) error
	BindIdentity(ctx context.Context, email, issuer, subject string, gitlabID int64, at time.Time) (store.User, error)
	IssueClientKey(ctx context.Context, userID int64, deviceID, deviceName string, tokenHash []byte, now, expires time.Time) (store.ClientKey, error)
	RevokeClientKey(ctx context.Context, id int64, userEmail string, now time.Time) (string, error)
}

// seedDays is how far back the demo history reaches.
const seedDays = 30

// Seed writes demo history for the configuration's users and models when the journal is empty, and reports whether
// it did. The history is deterministic: sessions and attempts over the last 30 days with tokens, confirmed costs and
// some errors, and the first user's current 5-hour window at 85% so the alert runner raises a real budget warning.
// It writes no incidents (the runner owns them) and no unknown cost. Every user also gets three device keys (active,
// revoked, expired) and sessions that carry a device and a client agent. Users are bound to the demo GitLab mock's identity (issuer, and the account ID
// the mock derives from the email), so a later real sign-in binds to the seeded row instead of conflicting with it.
func Seed(ctx context.Context, st SeedStore, cfg *config.Config, issuer string, now time.Time) (bool, error) {
	total, err := st.SumUsage(ctx, store.AttemptFilter{})
	if err != nil {
		return false, err
	}
	if total.Attempts > 0 {
		return false, nil
	}
	rng := rand.New(rand.NewPCG(7, 11))
	keys := map[string][]int64{}
	for _, u := range cfg.Users {
		ids, err := seedKeys(ctx, st, u.Email, issuer, now)
		if err != nil {
			return false, err
		}
		keys[u.Email] = ids
	}
	n := 0
	for day := seedDays; day >= 0; day-- {
		for _, u := range cfg.Users {
			tier, ok := cfg.Tier(u.Tier)
			if !ok {
				continue
			}
			for s := range 1 + rng.IntN(3) {
				start := now.Add(-time.Duration(day)*24*time.Hour - time.Duration(rng.IntN(20*60))*time.Minute)
				if !start.Before(now) {
					continue
				}
				session := fmt.Sprintf("demo-%s-%d-%d", u.Email, day, s)
				// Sessions run on the active or the revoked key, never on the expired one.
				key := keys[u.Email][rng.IntN(2)]
				agent := fmt.Sprintf("opencode/2.3.%d", rng.IntN(5))
				if err := st.TouchSession(ctx, session, u.Email, &key, agent, start); err != nil {
					return false, err
				}
				written, last, err := seedSession(ctx, st, rng, &n, u.Email, session, tier, start)
				if err != nil {
					return false, err
				}
				n += written
				// Touch the session again at its last attempt, as the proxy does, so it lasts as long as its work.
				if last.After(now) {
					last = now
				}
				if err := st.TouchSession(ctx, session, u.Email, &key, agent, last); err != nil {
					return false, err
				}
			}
		}
	}
	if len(cfg.Users) > 0 {
		if err := topUpFiveHours(ctx, st, cfg, cfg.Users[0], keys[cfg.Users[0].Email][0], now, &n); err != nil {
			return false, err
		}
	}
	return n > 0, nil
}

// topUpFiveHours brings a user's current 5-hour window to 85% of its limit with one attempt shortly before now, so the
// alert runner raises a real budget warning. It never pushes a window to its limit, which would block the user.
func topUpFiveHours(ctx context.Context, st SeedStore, cfg *config.Config, u config.User, key int64, now time.Time, seq *int) error {
	limits := cfg.UserLimits(u)
	from, to := store.WindowFiveHours.Bounds(now)
	spent, err := st.SumUsage(ctx, store.AttemptFilter{UserEmail: u.Email, From: from, To: to})
	if err != nil {
		return err
	}
	add := limits.FiveHours*85/100 - spent.Cost
	if add <= 0 {
		return nil
	}
	for _, w := range []struct {
		window store.Window
		limit  money.Amount
	}{{store.WindowDay, limits.Day}, {store.WindowWeek, limits.Week}, {store.WindowMonth, limits.Month}} {
		wf, wt := w.window.Bounds(now)
		got, err := st.SumUsage(ctx, store.AttemptFilter{UserEmail: u.Email, From: wf, To: wt})
		if err != nil {
			return err
		}
		if got.Cost+add >= w.limit {
			return nil
		}
	}
	at := now.Add(-10 * time.Minute)
	if at.Before(from) {
		at = from
	}
	*seq++
	id := fmt.Sprintf("demo-%06d", *seq)
	session := "demo-" + u.Email + "-budget"
	if err := st.TouchSession(ctx, session, u.Email, &key, "opencode/2.3.0", at); err != nil {
		return err
	}
	if err := st.StartAttempt(ctx, store.Attempt{ID: id, RequestID: id, UserEmail: u.Email, SessionID: session,
		RequestedModel: tierDefault(cfg, u), StartedAt: at}); err != nil {
		return err
	}
	in, out := int64(400000), int64(20000)
	return st.FinishAttempt(ctx, id, store.AttemptResult{FinishedAt: at.Add(30 * time.Second), InferenceProvider: "DemoProvider",
		GenerationID: "gen-" + id, Outcome: store.OutcomeSuccess, Tokens: store.Tokens{Input: &in, Output: &out}, Cost: &add})
}

// seedKeys binds the user to the demo GitLab identity and issues three device keys: an active laptop, a revoked
// desktop and an old laptop that expired. It returns the active and revoked key IDs, in that order.
func seedKeys(ctx context.Context, st SeedStore, email, issuer string, now time.Time) ([]int64, error) {
	gitlabID := mockgitlab.AccountID(email)
	user, err := st.BindIdentity(ctx, email, issuer, strconv.FormatInt(gitlabID, 10), gitlabID, now.AddDate(0, 0, -seedDays))
	if err != nil {
		return nil, fmt.Errorf("bind demo user %s: %w", email, err)
	}
	day := 24 * time.Hour
	specs := []struct {
		device, name     string
		created, expires time.Time
		revokedAt        time.Time
	}{
		{"laptop", "laptop", now.Add(-20 * day), now.Add(70 * day), time.Time{}},
		{"desktop", "desktop", now.Add(-25 * day), now.Add(65 * day), now.Add(-5 * day)},
		{"old-laptop", "old-laptop", now.Add(-100 * day), now.Add(-10 * day), time.Time{}},
	}
	var ids []int64
	for _, sp := range specs {
		_, hash := auth.NewToken("wrk_")
		k, err := st.IssueClientKey(ctx, user.ID, sp.device, sp.name, hash, sp.created, sp.expires)
		if err != nil {
			return nil, fmt.Errorf("issue demo key %s for %s: %w", sp.device, email, err)
		}
		if !sp.revokedAt.IsZero() {
			if _, err := st.RevokeClientKey(ctx, k.ID, email, sp.revokedAt); err != nil {
				return nil, fmt.Errorf("revoke demo key %s for %s: %w", sp.device, email, err)
			}
		}
		ids = append(ids, k.ID)
	}
	return ids, nil
}

// tierDefault is the default model of a user's tier.
func tierDefault(cfg *config.Config, u config.User) string {
	t, _ := cfg.Tier(u.Tier)
	return t.DefaultModel()
}

// seedSession writes one session's attempts on the tier's models. It returns how many attempts it wrote and when the
// last one started; seq numbers attempts across the whole seed.
func seedSession(ctx context.Context, st SeedStore, rng *rand.Rand, seq *int, email, session string, tier config.Tier,
	start time.Time) (int, time.Time, error) {
	written := 0
	at, last := start, start
	attempt := func(model string) error {
		last = at
		*seq++
		id := fmt.Sprintf("demo-%06d", *seq)
		if err := st.StartAttempt(ctx, store.Attempt{ID: id, RequestID: id, UserEmail: email, SessionID: session,
			RequestedModel: model, StartedAt: at}); err != nil {
			return err
		}
		written++
		finished := at.Add(time.Duration(2+rng.IntN(40)) * time.Second)
		first := at.Add(time.Duration(300+rng.IntN(4000)) * time.Millisecond)
		res := store.AttemptResult{FinishedAt: finished, FirstTokenAt: &first, InferenceProvider: "DemoProvider",
			GenerationID: "gen-" + id, Outcome: store.OutcomeSuccess}
		if rng.IntN(25) == 0 {
			res.Outcome, res.ErrorReason, res.FirstTokenAt = store.OutcomeError, "upstream 502 (demo)", nil
			zero := money.Amount(0)
			res.Cost = &zero
		} else {
			in, out := int64(2000+rng.IntN(30000)), int64(100+rng.IntN(3000))
			cost := money.Amount(in*300 + out*1500) // 0.3 and 1.5 USD per million tokens, in nano-dollars
			res.Tokens, res.Cost = store.Tokens{Input: &in, Output: &out}, &cost
		}
		at = finished.Add(time.Duration(5+rng.IntN(120)) * time.Second)
		return st.FinishAttempt(ctx, id, res)
	}
	for range 2 + rng.IntN(6) {
		model := tier.Models[rng.IntN(len(tier.Models))].Name
		if err := attempt(model); err != nil {
			return written, last, err
		}
	}
	return written, last, nil
}

// RunSeed seeds the database of WRENCH_POSTGRES_DSN from the configuration at configPath. It waits up to a minute
// for the schema, which the server creates on start.
func RunSeed(ctx context.Context, configPath string, lookupEnv func(string) (string, bool)) error {
	dsn, _ := lookupEnv("WRENCH_POSTGRES_DSN")
	if dsn == "" {
		return errors.New("mock seed: WRENCH_POSTGRES_DSN is required")
	}
	issuer, _ := lookupEnv("WRENCH_GITLAB_URL")
	if issuer == "" {
		return errors.New("mock seed: WRENCH_GITLAB_URL is required (the issuer a real sign-in binds to)")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	deadline := time.Now().Add(time.Minute)
	for {
		seeded, err := Seed(ctx, st, cfg, issuer, time.Now().UTC())
		switch {
		case err == nil && seeded:
			slog.InfoContext(ctx, "demo data seeded")
			return nil
		case err == nil:
			slog.InfoContext(ctx, "database already has data; nothing seeded")
			return nil
		case time.Now().After(deadline):
			return fmt.Errorf("mock seed: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
