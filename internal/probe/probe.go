// Package probe checks a dependency in the background and caches the result, so readiness answers never wait
// for, or add load to, the dependency they report on.
package probe

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// ErrNotProbed is the state until the first check finishes.
var ErrNotProbed = errors.New("not probed yet")

// Probe runs check every interval, each under timeout, and keeps the last result.
type Probe struct {
	check    func(context.Context) error
	interval time.Duration
	timeout  time.Duration
	last     atomic.Pointer[result]
	onChange func()
}

// result boxes a nil error so the atomic pointer can tell "passed" from "never ran".
type result struct{ err error }

func New(check func(context.Context) error, interval, timeout time.Duration) *Probe {
	return &Probe{check: check, interval: interval, timeout: timeout}
}

// Run checks at once and then every interval until ctx ends.
func (p *Probe) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		p.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// OnChange sets f to be called after a check flips the result between passing and failing; the state before the
// first check counts as failing. Call it before Run.
func (p *Probe) OnChange(f func()) { p.onChange = f }

func (p *Probe) once(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	was := p.Err(ctx) == nil
	next := &result{err: p.check(ctx)}
	p.last.Store(next)
	if p.onChange != nil && was != (next.err == nil) {
		p.onChange()
	}
}

// Err returns the last result; its signature matches the readiness dependencies it backs.
func (p *Probe) Err(context.Context) error {
	if r := p.last.Load(); r != nil {
		return r.err
	}
	return ErrNotProbed
}
