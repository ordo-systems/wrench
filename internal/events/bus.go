// Package events is an in-process bus that tells dashboard streams which data changed. Events carry only a
// topic, never data, so a subscriber must re-read through its own authorized API.
package events

import "sync"

// Topic names a kind of change.
type Topic string

const (
	Usage    Topic = "usage"
	Sessions Topic = "sessions"
	Keys     Topic = "keys"
	Users    Topic = "users"
	Alerts   Topic = "alerts"
	Status   Topic = "status"
	Budget   Topic = "budget"
)

// subscriberBuffer is how many undelivered events a subscriber may hold before new ones are dropped.
const subscriberBuffer = 16

// Publisher is the publishing side of the bus, so callers can take a no-op in tests.
type Publisher interface {
	Publish(Topic, string)
}

type subscriber struct {
	viewer string
	admin  bool
	ch     chan Topic
}

// Bus fans events out to subscribers by topic and viewer. Publishing never blocks: a subscriber whose buffer
// is full misses the event, which is safe because events only signal "refetch".
type Bus struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

func New() *Bus {
	return &Bus{subs: map[*subscriber]struct{}{}}
}

// Publish delivers t to the subscribers allowed to see it. user is the email the event concerns, "" for
// system events. Per-user topics reach that user and admins; Alerts and Users reach admins; Status reaches all.
func (b *Bus) Publish(t Topic, user string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		if !s.sees(t, user) {
			continue
		}
		select {
		case s.ch <- t:
		default:
		}
	}
}

func (s *subscriber) sees(t Topic, user string) bool {
	switch t {
	case Status:
		return true
	case Alerts, Users:
		return s.admin
	default:
		return s.admin || (user != "" && s.viewer == user)
	}
}

// Subscribe registers a viewer and returns its event channel and an idempotent unsubscribe func. The channel
// is never closed; callers stop reading when they unsubscribe.
func (b *Bus) Subscribe(viewer string, admin bool) (<-chan Topic, func()) {
	s := &subscriber{viewer: viewer, admin: admin, ch: make(chan Topic, subscriberBuffer)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s.ch, func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}
}
