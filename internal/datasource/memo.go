package datasource

import "sync"

// Memo runs a lookup once per key and shares its result, error included, with every caller of
// that key. The zero value is ready to use; a Memo must not be copied after first use.
type Memo[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]*memoEntry[V]
}

type memoEntry[V any] struct {
	once sync.Once
	v    V
	err  error
}

// Do returns the result of fn for key, running fn only for the first call of key. Concurrent
// callers of that key wait for it; fn runs without the Memo's lock, so other keys proceed.
func (m *Memo[K, V]) Do(key K, fn func() (V, error)) (V, error) {
	m.mu.Lock()
	e := m.m[key]
	if e == nil {
		if m.m == nil {
			m.m = make(map[K]*memoEntry[V])
		}
		e = &memoEntry[V]{}
		m.m[key] = e
	}
	m.mu.Unlock()
	e.once.Do(func() { e.v, e.err = fn() })
	return e.v, e.err
}
