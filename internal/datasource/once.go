package datasource

import (
	"context"
	"sync"
	"sync/atomic"
)

// Once runs a lookup once and shares its result, error included, with every later caller,
// except a failure that ended after the caller's context did: that one says nothing about the
// source (an errgroup sibling failed, a per-PR deadline passed), so it goes to its caller only
// and the next caller looks up again with its own context. A request timeout is no such
// failure while the caller's context lives, so a slow server is still asked once. Concurrent
// callers wait for the running lookup. The zero value is ready to use; a Once must not be
// copied after first use.
type Once[V any] struct {
	mu   sync.Mutex
	done atomic.Bool // v and err are final
	v    V
	err  error
}

// Do returns the result of fn, the lookup bounded by ctx, running it unless a call before
// returned a shared result.
func (o *Once[V]) Do(ctx context.Context, fn func() (V, error)) (V, error) {
	if o.done.Load() {
		return o.v, o.err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done.Load() {
		return o.v, o.err
	}
	v, err := fn()
	if err != nil && ctx.Err() != nil {
		return v, err
	}
	o.v, o.err = v, err
	o.done.Store(true)
	return v, err
}

// Peek returns the shared value without looking up; ok is false while there is none.
func (o *Once[V]) Peek() (v V, ok bool) {
	if !o.done.Load() {
		return v, false
	}
	return o.v, true
}
