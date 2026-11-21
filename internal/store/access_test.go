package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// userByEmail reads a user row directly; production code reaches users only through credentials.
func userByEmail(ctx context.Context, s *Store, email string) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users WHERE email = $1", email))
}

func activeUser(t *testing.T, s *Store, email, subject string) User {
	t.Helper()
	ctx := context.Background()
	u, err := s.BindIdentity(ctx, email, "https://gitlab.example.com", subject, 42, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStatus(ctx, u.ID, true, t0); err != nil {
		t.Fatal(err)
	}
	u, err = userByEmail(ctx, s, email)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestBindIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const iss = "https://gitlab.example.com"

	u, err := s.BindIdentity(ctx, "a@x", iss, "1", 1, t0)
	if err != nil || u.Status != UserPending || u.StatusCheckedAt != nil || u.GitLabID != 1 {
		t.Fatalf("first bind = %+v, %v", u, err)
	}
	again, err := s.BindIdentity(ctx, "a@x", iss, "1", 1, t0.Add(time.Hour))
	if err != nil || again.ID != u.ID {
		t.Fatalf("relogin = %+v, %v", again, err)
	}
	// Another GitLab account claiming the same email is not silently rebound.
	if _, err := s.BindIdentity(ctx, "a@x", iss, "2", 2, t0); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("other subject: %v", err)
	}
	// The same GitLab account after an email change keeps its history; it is not a new user.
	if _, err := s.BindIdentity(ctx, "renamed@x", iss, "1", 1, t0); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("email change: %v", err)
	}
	if _, err := userByEmail(ctx, s, "nobody@x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
}

func TestStatusAndCredentials(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := activeUser(t, s, "a@x", "1")
	if u.Status != UserActive || !u.StatusCheckedAt.Equal(t0) {
		t.Fatalf("user = %+v", u)
	}

	if err := s.CreateWebSession(ctx, u.ID, hash("sess"), t0, t0.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, err := s.LookupWebSession(ctx, hash("sess"), t0.Add(time.Hour))
	if err != nil || c.UserEmail != "a@x" || c.UserStatus != UserActive || !c.StatusCheckedAt.Equal(t0) {
		t.Fatalf("session = %+v, %v", c, err)
	}
	if _, err := s.LookupWebSession(ctx, hash("sess"), t0.Add(12*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
	if _, err := s.LookupWebSession(ctx, hash("other"), t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}

	laptop, err := s.IssueClientKey(ctx, u.ID, "laptop", "Laptop", hash("k1"), t0, t0.Add(168*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueClientKey(ctx, u.ID, "desktop", "Desktop", hash("k2"), t0, t0.Add(168*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Relogin on the laptop rotates only the laptop key.
	if _, err := s.IssueClientKey(ctx, u.ID, "laptop", "Laptop", hash("k3"), t0.Add(time.Hour), t0.Add(169*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupClientKey(ctx, hash("k1"), t0.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotated key still valid: %v", err)
	}
	for _, k := range []string{"k2", "k3"} {
		if c, err := s.LookupClientKey(ctx, hash(k), t0.Add(2*time.Hour)); err != nil || c.UserEmail != "a@x" {
			t.Fatalf("%s = %+v, %v", k, c, err)
		}
	}
	if _, err := s.LookupClientKey(ctx, hash("k2"), t0.Add(168*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired key: %v", err)
	}

	keys, err := s.ListClientKeys(ctx, "a@x")
	if err != nil || len(keys) != 3 {
		t.Fatalf("keys = %+v, %v", keys, err)
	}
	var rotated, used int
	for _, k := range keys {
		if k.RevokedAt != nil {
			rotated++
			if k.ID != laptop.ID {
				t.Fatalf("wrong key revoked: %+v", k)
			}
		}
		if k.LastUsedAt != nil {
			used++
		}
	}
	if rotated != 1 || used != 2 {
		t.Fatalf("rotated %d used %d", rotated, used)
	}
}

func TestRevocation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := activeUser(t, s, "a@x", "1")
	b := activeUser(t, s, "b@x", "2")
	ka, _ := s.IssueClientKey(ctx, a.ID, "d1", "", hash("ka"), t0, t0.Add(time.Hour))
	if _, err := s.IssueClientKey(ctx, b.ID, "d1", "", hash("kb"), t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// A user cannot revoke someone else's key.
	if _, err := s.RevokeClientKey(ctx, ka.ID, "b@x", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign revoke: %v", err)
	}
	if _, err := s.RevokeClientKey(ctx, ka.ID, "a@x", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupClientKey(ctx, hash("ka"), t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked key: %v", err)
	}

	_ = s.CreateWebSession(ctx, b.ID, hash("sb"), t0, t0.Add(time.Hour))
	n, err := s.RevokeUserCredentials(ctx, "b@x", t0)
	if err != nil || n != 2 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	if _, err := s.LookupWebSession(ctx, hash("sb"), t0); !errors.Is(err, ErrNotFound) {
		t.Fatal("session survived revocation")
	}

	// A confirmed GitLab disable blocks the user and revokes everything at once.
	_, _ = s.IssueClientKey(ctx, a.ID, "d2", "", hash("ka2"), t0, t0.Add(time.Hour))
	_ = s.CreateWebSession(ctx, a.ID, hash("sa"), t0, t0.Add(time.Hour))
	if err := s.RecordStatus(ctx, a.ID, false, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupClientKey(ctx, hash("ka2"), t0); !errors.Is(err, ErrNotFound) {
		t.Fatal("key survived block")
	}
	if _, err := s.LookupWebSession(ctx, hash("sa"), t0); !errors.Is(err, ErrNotFound) {
		t.Fatal("session survived block")
	}
	u, _ := userByEmail(ctx, s, "a@x")
	if u.Status != UserBlocked {
		t.Fatalf("status %s", u.Status)
	}
	users, err := s.ListUsers(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("users = %+v, %v", users, err)
	}
}

func TestLoginFlowsAreSingleUse(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	f := LoginFlow{State: "st", Nonce: "n", Verifier: "v", BrowserBinding: hash("b"), ExpiresAt: t0.Add(10 * time.Minute)}
	if err := s.CreateLoginFlow(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConsumeLoginFlow(ctx, "st", t0)
	if err != nil || got.Nonce != "n" || string(got.BrowserBinding) != string(hash("b")) {
		t.Fatalf("flow = %+v, %v", got, err)
	}
	if _, err := s.ConsumeLoginFlow(ctx, "st", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("flow replay: %v", err)
	}
	_ = s.CreateLoginFlow(ctx, LoginFlow{State: "old", Nonce: "n", Verifier: "v", BrowserBinding: hash("b"), ExpiresAt: t0})
	if _, err := s.ConsumeLoginFlow(ctx, "old", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired flow: %v", err)
	}
}

func TestRevokeClientKeysRevokesOnlyActiveKeysOfThatUser(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := activeUser(t, s, "a@x", "1")
	b := activeUser(t, s, "b@x", "2")
	issue := func(userID int64, device, secret string, expires time.Time) ClientKey {
		t.Helper()
		k, err := s.IssueClientKey(ctx, userID, device, "", hash(secret), t0, expires)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	issue(a.ID, "d1", "a1", t0.Add(time.Hour))
	issue(a.ID, "d2", "a2", t0.Add(time.Hour))
	old := issue(a.ID, "d3", "a3", t0.Add(time.Hour))
	issue(a.ID, "d4", "a4", t0.Add(time.Minute))
	issue(b.ID, "d5", "b1", t0.Add(time.Hour))
	if _, err := s.RevokeClientKey(ctx, old.ID, "a@x", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, "s1", "a@x", nil, "", t0); err != nil {
		t.Fatal(err)
	}

	n, err := s.RevokeClientKeys(ctx, "a@x", t0.Add(30*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("revoked %d, %v; want 2", n, err)
	}
	again, err := s.RevokeClientKeys(ctx, "a@x", t0.Add(30*time.Minute))
	if err != nil || again != 0 {
		t.Fatalf("second pass revoked %d, %v; want 0", again, err)
	}
	for _, email := range []string{"a@x", "b@x"} {
		keys, err := s.ListClientKeys(ctx, email)
		if err != nil {
			t.Fatal(err)
		}
		revoked := 0
		for _, k := range keys {
			if k.RevokedAt != nil {
				revoked++
			}
		}
		if want := map[string]int{"a@x": 3, "b@x": 0}[email]; revoked != want {
			t.Fatalf("%s: %d revoked keys, want %d", email, revoked, want)
		}
	}
	sessions, err := s.ListSessions(ctx, "a@x", "", time.Time{}, 10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
}
