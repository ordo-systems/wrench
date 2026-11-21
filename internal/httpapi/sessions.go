package httpapi

import (
	"time"

	"github.com/ordo-systems/wrench/internal/store"
)

// SessionTiming sets when a silent session turns offline.
type SessionTiming struct {
	OfflineAfter time.Duration
}

// liveStatus is the dashboard view of a session. Offline means no request arrived within the timeout; it does
// not prove that the client stopped.
type liveStatus string

const (
	statusActive  liveStatus = "active"
	statusOffline liveStatus = "offline"
)

func statusOf(s store.Session, now time.Time, offlineAfter time.Duration) liveStatus {
	if now.Sub(s.LastSeenAt) > offlineAfter {
		return statusOffline
	}
	return statusActive
}
