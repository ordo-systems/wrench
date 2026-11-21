package probe

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond until it holds or a second passes.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestProbe(t *testing.T) {
	down := errors.New("down")
	tests := []struct {
		name  string
		check func(context.Context) error
		want  func(error) bool
	}{
		{"passing check", func(context.Context) error { return nil }, func(err error) bool { return err == nil }},
		{"failing check", func(context.Context) error { return down }, func(err error) bool { return errors.Is(err, down) }},
		{"check past its timeout", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			func(err error) bool { return errors.Is(err, context.DeadlineExceeded) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(tt.check, time.Hour, 50*time.Millisecond)
			if err := p.Err(context.Background()); !errors.Is(err, ErrNotProbed) {
				t.Fatalf("before Run: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go p.Run(ctx)
			waitFor(t, func() bool { return tt.want(p.Err(context.Background())) })
		})
	}
}

func TestProbeRecovers(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	p := New(func(context.Context) error {
		if failing.Load() {
			return errors.New("down")
		}
		return nil
	}, 10*time.Millisecond, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor(t, func() bool {
		return p.Err(context.Background()) != nil && !errors.Is(p.Err(context.Background()), ErrNotProbed)
	})
	failing.Store(false)
	waitFor(t, func() bool { return p.Err(context.Background()) == nil })
}

// Err answers from the cache: a check that never returns cannot hold up a reader.
func TestErrDoesNotWaitForTheCheck(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	p := New(func(context.Context) error { <-block; return nil }, time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	start := time.Now()
	if err := p.Err(context.Background()); !errors.Is(err, ErrNotProbed) || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("Err = %v after %s", err, time.Since(start))
	}
}

// OnChange fires once per flip between failing and passing; the unprobed state counts as failing.
func TestProbeOnChange(t *testing.T) {
	var calls int
	var failing bool
	p := New(func(context.Context) error {
		if failing {
			return errors.New("down")
		}
		return nil
	}, time.Hour, time.Second)
	p.OnChange(func() { calls++ })
	ctx := context.Background()
	steps := []struct {
		failing bool
		calls   int
	}{{true, 0}, {true, 0}, {false, 1}, {false, 1}, {true, 2}}
	for i, s := range steps {
		failing = s.failing
		p.once(ctx)
		if calls != s.calls {
			t.Fatalf("step %d: %d callbacks, want %d", i, calls, s.calls)
		}
	}
}
