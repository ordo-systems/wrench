package gitlab

import (
	"slices"
	"testing"
)

func TestAccountID(t *testing.T) {
	tests := []struct {
		a, b string
		same bool
	}{
		{"user1@example.com", " User1@Example.com ", true},
		{"user1@example.com", "user2@example.com", false},
	}
	for _, tt := range tests {
		if (AccountID(tt.a) == AccountID(tt.b)) != tt.same {
			t.Errorf("AccountID(%q) == AccountID(%q) is %v, want %v", tt.a, tt.b, !tt.same, tt.same)
		}
		if AccountID(tt.a) <= 0 {
			t.Errorf("AccountID(%q) = %d, want positive", tt.a, AccountID(tt.a))
		}
	}
}

func TestConfigDirectory(t *testing.T) {
	d := ConfigDirectory([]string{"user1@example.com", "user2@example.com"})
	if got := d.Candidates(); !slices.Equal(got, []string{"user1@example.com", "user2@example.com"}) {
		t.Errorf("Candidates() = %v", got)
	}
	if a, ok := d.Lookup(AccountID("user1@example.com")); !ok || a.Email != "user1@example.com" || !a.EmailVerified || a.State != "active" {
		t.Errorf("configured account %+v, %v", a, ok)
	}
	id, err := d.SignIn(" Other@Example.com")
	if err != nil || id != AccountID("other@example.com") {
		t.Fatalf("SignIn = %d, %v", id, err)
	}
	if a, ok := d.Lookup(id); !ok || a.Email != "other@example.com" || !a.EmailVerified || a.State != "active" {
		t.Errorf("signed-in account %+v, %v", a, ok)
	}
	if a, ok := d.Lookup(12345); !ok || a.State != "active" || a.Email != "" {
		t.Errorf("unseen account %+v, %v", a, ok)
	}
	for _, bad := range []string{"", "no-at-sign", "@example.com", "a@", "a@b@c", "user1 @example.com", "a b@c", "Name <a@b.c>"} {
		if _, err := d.SignIn(bad); err == nil {
			t.Errorf("SignIn(%q) accepted", bad)
		}
	}
}
