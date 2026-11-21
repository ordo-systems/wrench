package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeviceCodeLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := activeUser(t, s, "a@x", "1")
	if err := s.CreateDeviceCode(ctx, hash("dev"), hash("USER"), t0.Add(10*time.Minute), 100); err != nil {
		t.Fatal(err)
	}
	if c, err := s.PollDeviceCode(ctx, hash("dev"), t0, 5*time.Second); err != nil || c.Status != DevicePending {
		t.Fatalf("first poll = %+v, %v", c, err)
	}
	if err := s.DecideDeviceCode(ctx, hash("USER"), u.Email, true, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	c, err := s.PollDeviceCode(ctx, hash("dev"), t0.Add(10*time.Second), 5*time.Second)
	if err != nil || c.Status != DeviceApproved || c.UserID != u.ID {
		t.Fatalf("approved poll = %+v, %v", c, err)
	}
	// An approved code yields one key: the next poll finds nothing.
	if _, err := s.PollDeviceCode(ctx, hash("dev"), t0.Add(20*time.Second), 5*time.Second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("poll after consumption = %v", err)
	}
}

func TestDeviceCodeSlowDown(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateDeviceCode(ctx, hash("dev"), hash("USER"), t0.Add(10*time.Minute), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PollDeviceCode(ctx, hash("dev"), t0, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PollDeviceCode(ctx, hash("dev"), t0.Add(time.Second), 5*time.Second); !errors.Is(err, ErrSlowDown) {
		t.Fatalf("fast poll = %v", err)
	}
	if c, err := s.PollDeviceCode(ctx, hash("dev"), t0.Add(7*time.Second), 5*time.Second); err != nil || c.Status != DevicePending {
		t.Fatalf("poll after the interval = %+v, %v", c, err)
	}
}

func TestDeviceCodeDecideOnce(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := activeUser(t, s, "a@x", "1")
	if err := s.CreateDeviceCode(ctx, hash("dev"), hash("USER"), t0.Add(10*time.Minute), 100); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideDeviceCode(ctx, hash("USER"), u.Email, false, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideDeviceCode(ctx, hash("USER"), u.Email, true, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second decision = %v", err)
	}
	if c, err := s.PollDeviceCode(ctx, hash("dev"), t0, 5*time.Second); err != nil || c.Status != DeviceDenied {
		t.Fatalf("denied poll = %+v, %v", c, err)
	}
	if err := s.CreateDeviceCode(ctx, hash("old"), hash("OLD"), t0, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideDeviceCode(ctx, hash("OLD"), u.Email, true, t0.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired decision = %v", err)
	}
	if _, err := s.PollDeviceCode(ctx, hash("old"), t0.Add(time.Second), 5*time.Second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired poll = %v", err)
	}
}

func TestDeviceCodesAreBounded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// An expired code is dropped and does not count against the limit.
	if err := s.CreateDeviceCode(ctx, hash("old"), hash("OLD"), time.Now().Add(-time.Minute), 2); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"a", "b"} {
		if err := s.CreateDeviceCode(ctx, hash("dev"+c), hash("USER"+c), time.Now().Add(time.Minute), 2); err != nil {
			t.Fatalf("code %s: %v", c, err)
		}
	}
	if err := s.CreateDeviceCode(ctx, hash("devc"), hash("USERc"), time.Now().Add(time.Minute), 2); !errors.Is(err, ErrDeviceCodesFull) {
		t.Fatalf("third live code = %v", err)
	}
	var indexed bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes
		WHERE tablename = 'device_codes' AND indexdef LIKE '%(expires_at)%')`).Scan(&indexed); err != nil || !indexed {
		t.Fatalf("expires_at index = %v, %v", indexed, err)
	}
}

func TestRefreshRotates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := activeUser(t, s, "a@x", "1")
	first, err := s.IssueDeviceKey(ctx, u.ID, "opencode-1", "opencode", hash("k1"), hash("r1"), t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, owner, err := s.RefreshClientKey(ctx, hash("r1"), hash("k2"), hash("r2"), t0.Add(time.Minute), t0.Add(2*time.Hour))
	if err != nil || second.ID == first.ID || second.DeviceID != "opencode-1" || owner.ID != u.ID {
		t.Fatalf("refresh = %+v %+v, %v", second, owner, err)
	}
	if _, err := s.LookupClientKey(ctx, hash("k1"), t0.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old key still works: %v", err)
	}
	if _, err := s.LookupClientKey(ctx, hash("k2"), t0.Add(2*time.Minute)); err != nil {
		t.Fatalf("new key: %v", err)
	}
	if _, _, err := s.RefreshClientKey(ctx, hash("r1"), hash("k3"), hash("r3"), t0.Add(3*time.Minute), t0.Add(3*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reused refresh = %v", err)
	}
	if _, err := s.RevokeClientKey(ctx, second.ID, "a@x", t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RefreshClientKey(ctx, hash("r2"), hash("k4"), hash("r4"), t0.Add(5*time.Minute), t0.Add(3*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refresh of a revoked key = %v", err)
	}
}

func TestUserByID(t *testing.T) {
	s := openTestStore(t)
	u := activeUser(t, s, "a@x", "1")
	got, err := s.UserByID(context.Background(), u.ID)
	if err != nil || got.Email != "a@x" || got.Status != UserActive {
		t.Fatalf("user = %+v, %v", got, err)
	}
	if _, err := s.UserByID(context.Background(), u.ID+1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user = %v", err)
	}
}

func TestTouchSessionRecordsKey(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := activeUser(t, s, "a@x", "1")
	k, err := s.IssueClientKey(ctx, u.ID, "laptop", "Laptop", hash("k"), t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, "s1", "a@x", nil, "", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, "s1", "a@x", &k.ID, "", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSessions(ctx, "a@x", "", time.Time{}, 10)
	if err != nil || len(list) != 1 || list[0].DeviceID != "laptop" || list[0].DeviceName != "Laptop" {
		t.Fatalf("sessions = %+v, %v", list, err)
	}
}

func TestLoginFlowKeepsReturnPath(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	f := LoginFlow{State: "st", Nonce: "n", Verifier: "v", BrowserBinding: hash("b"),
		ReturnTo: "/device?code=ABCD-EFGH", ExpiresAt: t0.Add(time.Hour)}
	if err := s.CreateLoginFlow(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConsumeLoginFlow(ctx, "st", t0)
	if err != nil || got.ReturnTo != "/device?code=ABCD-EFGH" {
		t.Fatalf("flow = %+v, %v", got, err)
	}
}
