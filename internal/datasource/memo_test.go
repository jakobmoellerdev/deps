package datasource

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemoDedupesConcurrentCallers(t *testing.T) {
	var (
		m     Memo[string, int]
		calls atomic.Int32
		start = make(chan struct{})
		wg    sync.WaitGroup
	)
	for range 16 {
		wg.Go(func() {
			<-start
			v, err := m.Do(t.Context(), "k", func() (int, error) {
				calls.Add(1)
				return 42, nil
			})
			if v != 42 || err != nil {
				t.Errorf("Do = %d, %v, want 42, nil", v, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("fn ran %d times, want 1", n)
	}
}

func TestMemoSharesErrors(t *testing.T) {
	var m Memo[string, int]
	boom := errors.New("boom")
	calls := 0
	for range 2 {
		v, err := m.Do(t.Context(), "k", func() (int, error) {
			calls++
			return 7, boom
		})
		if v != 7 || !errors.Is(err, boom) {
			t.Errorf("Do = %d, %v, want 7, boom", v, err)
		}
	}
	if calls != 1 {
		t.Errorf("fn ran %d times, want 1 (errors are cached too)", calls)
	}
}

// A lookup failing because its caller's context ended is not shared: the next caller of the
// key looks up again with its own context and its result is shared from then on.
func TestMemoRetriesAfterCanceledCaller(t *testing.T) {
	var m Memo[string, int]
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	lookup := func(ctx context.Context) func() (int, error) {
		return func() (int, error) {
			calls++
			if err := ctx.Err(); err != nil {
				return 0, fmt.Errorf("GET x: %w", err)
			}
			return 42, nil
		}
	}
	if _, err := m.Do(canceled, "k", lookup(canceled)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Do err = %v, want context.Canceled", err)
	}
	for range 2 {
		if v, err := m.Do(t.Context(), "k", lookup(t.Context())); v != 42 || err != nil {
			t.Errorf("live Do = %d, %v, want 42, nil", v, err)
		}
	}
	if calls != 2 {
		t.Errorf("fn ran %d times, want 2 (the canceled try and one live lookup)", calls)
	}
}

// A deadline of the request itself (httpx.RequestTimeout) while the caller's context lives
// is a failure of the source: it is shared like any other error.
func TestMemoSharesRequestTimeouts(t *testing.T) {
	var m Memo[string, int]
	calls := 0
	for range 2 {
		_, err := m.Do(t.Context(), "k", func() (int, error) {
			calls++
			return 0, fmt.Errorf("GET x: %w", context.DeadlineExceeded)
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Do err = %v, want context.DeadlineExceeded", err)
		}
	}
	if calls != 1 {
		t.Errorf("fn ran %d times, want 1", calls)
	}
}

// Callers waiting for a lookup whose caller is canceled meanwhile run it again with their
// own context instead of inheriting the cancellation.
func TestOnceWaiterRetriesCanceledLookup(t *testing.T) {
	var o Once[int]
	first, cancel := context.WithCancel(t.Context())
	running := make(chan struct{})
	done := make(chan error)
	go func() {
		_, err := o.Do(first, func() (int, error) {
			close(running)
			<-first.Done()
			return 0, first.Err()
		})
		done <- err
	}()
	<-running
	waiter := make(chan int)
	go func() {
		v, err := o.Do(t.Context(), func() (int, error) { return 42, nil })
		if err != nil {
			t.Errorf("waiter err = %v", err)
		}
		waiter <- v
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("first err = %v, want context.Canceled", err)
	}
	if v := <-waiter; v != 42 {
		t.Errorf("waiter = %d, want 42", v)
	}
	if v, _ := o.Do(t.Context(), func() (int, error) { return 0, errors.New("ran again") }); v != 42 {
		t.Errorf("after = %d, want the shared 42", v)
	}
}

func TestMemoPeek(t *testing.T) {
	var m Memo[string, int]
	if _, ok := m.Peek("k"); ok {
		t.Error("Peek before Do: ok")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = m.Do(canceled, "k", func() (int, error) { return 1, canceled.Err() })
	if _, ok := m.Peek("k"); ok {
		t.Error("Peek after a canceled lookup: ok")
	}
	_, _ = m.Do(t.Context(), "k", func() (int, error) { return 2, nil })
	if v, ok := m.Peek("k"); v != 2 || !ok {
		t.Errorf("Peek = %d, %v, want 2, true", v, ok)
	}
}

// A key's lookup does not block the lookup of another key, even one started from inside it.
func TestMemoKeysIndependent(t *testing.T) {
	var m Memo[string, string]
	ctx := t.Context()
	release := make(chan struct{})
	running := make(chan struct{})
	done := make(chan string)
	go func() {
		v, _ := m.Do(ctx, "a", func() (string, error) {
			close(running)
			<-release
			return "A", nil
		})
		done <- v
	}()
	<-running
	if v, _ := m.Do(ctx, "b", func() (string, error) { return "B", nil }); v != "B" {
		t.Errorf("Do(b) = %q, want B", v)
	}
	if v, _ := m.Do(ctx, "outer", func() (string, error) {
		return m.Do(ctx, "inner", func() (string, error) { return "I", nil })
	}); v != "I" {
		t.Errorf("nested Do = %q, want I", v)
	}
	close(release)
	if v := <-done; v != "A" {
		t.Errorf("Do(a) = %q, want A", v)
	}
	if v, _ := m.Do(ctx, "a", func() (string, error) { return "again", nil }); v != "A" {
		t.Errorf("second Do(a) = %q, want the cached A", v)
	}
}
