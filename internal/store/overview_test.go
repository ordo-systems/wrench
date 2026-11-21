package store

import (
	"context"
	"testing"
	"time"
)

func TestUserStates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u, err := s.BindIdentity(ctx, "a@x", "https://gitlab.example.com", "1", 1, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, dev := range []string{"laptop", "desktop"} {
		if _, err := s.IssueClientKey(ctx, u.ID, dev, dev, []byte(dev), t0, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	states, err := s.UserStates(ctx, t0.Add(time.Minute))
	if err != nil || states["a@x"].ActiveKeys != 2 || states["a@x"].Status != UserPending {
		t.Fatalf("states = %+v, %v", states, err)
	}
	if expired, _ := s.UserStates(ctx, t0.Add(2*time.Hour)); expired["a@x"].ActiveKeys != 0 {
		t.Fatalf("expired keys counted: %+v", expired)
	}
}

func TestModelHealth(t *testing.T) {
	s := statsFixture(t)
	health, err := s.ModelHealth(context.Background(), t0)
	if err != nil || len(health) != 2 {
		t.Fatalf("health = %+v, %v", health, err)
	}
	glm, flash := health[0], health[1]
	if flash.Model != "glm-flash" || flash.Errors != 1 || flash.LastErrorReason != "upstream 500" || flash.LastSuccessAt != nil {
		t.Fatalf("glm-flash = %+v", flash)
	}
	if glm.Model != "glm" || glm.Attempts != 3 || glm.Errors != 0 || glm.LastSuccessAt == nil || *glm.FirstTokenP95 < 1000 {
		t.Fatalf("glm = %+v", glm)
	}
}
