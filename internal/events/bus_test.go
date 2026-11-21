package events

import (
	"testing"
	"time"
)

func received(ch <-chan Topic) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestDelivery(t *testing.T) {
	tests := []struct {
		name   string
		topic  Topic
		user   string
		viewer string
		admin  bool
		want   bool
	}{
		{"usage to owner", Usage, "a@x", "a@x", false, true},
		{"usage to admin", Usage, "a@x", "root@x", true, true},
		{"usage to other user", Usage, "a@x", "b@x", false, false},
		{"budget to owner", Budget, "a@x", "a@x", false, true},
		{"budget to admin", Budget, "a@x", "root@x", true, true},
		{"budget to other user", Budget, "a@x", "b@x", false, false},
		{"sessions to owner", Sessions, "a@x", "a@x", false, true},
		{"sessions to admin", Sessions, "a@x", "root@x", true, true},
		{"sessions to other user", Sessions, "a@x", "b@x", false, false},
		{"keys to owner", Keys, "a@x", "a@x", false, true},
		{"keys to admin", Keys, "a@x", "root@x", true, true},
		{"keys to other user", Keys, "a@x", "b@x", false, false},
		{"alerts to admin", Alerts, "", "root@x", true, true},
		{"alerts to user", Alerts, "", "a@x", false, false},
		{"users to admin", Users, "", "root@x", true, true},
		{"users to user", Users, "a@x", "a@x", false, false},
		{"status to user", Status, "", "a@x", false, true},
		{"status to admin", Status, "", "root@x", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New()
			ch, unsub := b.Subscribe(tt.viewer, tt.admin)
			defer unsub()
			b.Publish(tt.topic, tt.user)
			if got := received(ch); got != tt.want {
				t.Fatalf("received = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b := New()
	_, unsub := b.Subscribe("a@x", false)
	defer unsub()
	start := time.Now()
	for i := 0; i < 100; i++ {
		b.Publish(Usage, "a@x")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("100 publishes took %v, want under 100ms", d)
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe("a@x", false)
	unsub()
	unsub() // idempotent
	b.Publish(Usage, "a@x")
	if received(ch) {
		t.Fatal("received an event after unsubscribe")
	}
}
