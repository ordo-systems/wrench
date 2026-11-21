package proxy

import "sync"

// limiter caps concurrent upstream requests per user (across all devices) and in total.
// It is in-process state: a restart drops in-flight requests anyway.
type limiter struct {
	mu      sync.Mutex
	perUser int
	global  int
	total   int
	users   map[string]int
}

func newLimiter(perUser, global int) *limiter {
	return &limiter{perUser: perUser, global: global, users: map[string]int{}}
}

// acquire takes a slot for the user or reports that a limit is reached.
func (l *limiter) acquire(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total >= l.global || l.users[user] >= l.perUser {
		return false
	}
	l.total++
	l.users[user]++
	return true
}

// release returns a slot taken by acquire.
func (l *limiter) release(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.users[user]--; l.users[user] <= 0 {
		delete(l.users, user)
	}
}
