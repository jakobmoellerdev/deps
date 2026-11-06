package datasource

import (
	"context"
	"sync"
)

// Memo runs a lookup once per key and shares its result, error included, with every caller of
// that key, as Once does: a failure after the caller's context ended is not shared. The zero
// value is ready to use; a Memo must not be copied after first use.
type Memo[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]*Once[V]
}

// Do returns the result of fn, the lookup of key bounded by ctx, running fn only until a call
// of key returned a shared result. Concurrent callers of that key wait for it; fn runs without
// the Memo's lock, so other keys proceed.
func (m *Memo[K, V]) Do(ctx context.Context, key K, fn func() (V, error)) (V, error) {
	m.mu.Lock()
	e := m.m[key]
	if e == nil {
		if m.m == nil {
			m.m = make(map[K]*Once[V])
		}
		e = &Once[V]{}
		m.m[key] = e
	}
	m.mu.Unlock()
	return e.Do(ctx, fn)
}

// Peek returns the shared value of key without looking up; ok is false while there is none.
func (m *Memo[K, V]) Peek(key K) (v V, ok bool) {
	m.mu.Lock()
	e := m.m[key]
	m.mu.Unlock()
	if e == nil {
		return v, false
	}
	return e.Peek()
}
