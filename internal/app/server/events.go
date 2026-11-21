package server

import (
	"context"
	"sync"
	"time"

	"github.com/ordo-systems/wrench/internal/events"
	"github.com/ordo-systems/wrench/internal/proxy"
	"github.com/ordo-systems/wrench/internal/reconcile"
	"github.com/ordo-systems/wrench/internal/store"
)

// publishingJournal tells dashboards what each journal write changed. Events follow successful writes only.
type publishingJournal struct {
	proxy.Journal
	pub events.Publisher
	// users maps an open attempt to its user, which FinishAttempt no longer receives.
	users sync.Map
}

func newPublishingJournal(j proxy.Journal, pub events.Publisher) *publishingJournal {
	return &publishingJournal{Journal: j, pub: pub}
}

func (j *publishingJournal) StartAttempt(ctx context.Context, a store.Attempt) error {
	if err := j.Journal.StartAttempt(ctx, a); err != nil {
		return err
	}
	j.users.Store(a.ID, a.UserEmail)
	return nil
}

func (j *publishingJournal) TouchSession(ctx context.Context, id, userEmail string, keyID *int64, agent string, at time.Time) error {
	if err := j.Journal.TouchSession(ctx, id, userEmail, keyID, agent, at); err != nil {
		return err
	}
	j.pub.Publish(events.Sessions, userEmail)
	return nil
}

func (j *publishingJournal) FinishAttempt(ctx context.Context, id string, r store.AttemptResult) error {
	// Forget the user even when the write fails: nothing else would ever remove the entry.
	email, ok := j.users.LoadAndDelete(id)
	if err := j.Journal.FinishAttempt(ctx, id, r); err != nil {
		return err
	}
	if ok {
		j.pub.Publish(events.Usage, email.(string))
		j.pub.Publish(events.Budget, email.(string))
	}
	j.pub.Publish(events.Status, "")
	return nil
}

// publishingReconcileStore announces costs the reconciler changed. It learns each attempt's user from the due
// list, because a settlement does not carry it.
type publishingReconcileStore struct {
	reconcile.Store
	pub   events.Publisher
	users sync.Map
}

func newPublishingReconcileStore(s reconcile.Store, pub events.Publisher) *publishingReconcileStore {
	return &publishingReconcileStore{Store: s, pub: pub}
}

func (s *publishingReconcileStore) DueReconciliations(ctx context.Context, now time.Time, limit int) ([]store.Reconciliation, error) {
	due, err := s.Store.DueReconciliations(ctx, now, limit)
	for _, d := range due {
		s.users.Store(d.ID, d.UserEmail)
	}
	return due, err
}

func (s *publishingReconcileStore) SettleAttempt(ctx context.Context, st store.Settlement) (bool, error) {
	changed, err := s.Store.SettleAttempt(ctx, st)
	email, known := s.users.LoadAndDelete(st.AttemptID)
	if err != nil || !changed {
		return changed, err
	}
	if known {
		s.pub.Publish(events.Usage, email.(string))
		s.pub.Publish(events.Budget, email.(string))
	}
	s.pub.Publish(events.Status, "")
	return changed, nil
}
