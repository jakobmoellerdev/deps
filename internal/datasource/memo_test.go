package datasource

import (
	"errors"
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
			v, err := m.Do("k", func() (int, error) {
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
		v, err := m.Do("k", func() (int, error) {
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

// A key's lookup does not block the lookup of another key, even one started from inside it.
func TestMemoKeysIndependent(t *testing.T) {
	var m Memo[string, string]
	release := make(chan struct{})
	running := make(chan struct{})
	done := make(chan string)
	go func() {
		v, _ := m.Do("a", func() (string, error) {
			close(running)
			<-release
			return "A", nil
		})
		done <- v
	}()
	<-running
	if v, _ := m.Do("b", func() (string, error) { return "B", nil }); v != "B" {
		t.Errorf("Do(b) = %q, want B", v)
	}
	if v, _ := m.Do("outer", func() (string, error) {
		return m.Do("inner", func() (string, error) { return "I", nil })
	}); v != "I" {
		t.Errorf("nested Do = %q, want I", v)
	}
	close(release)
	if v := <-done; v != "A" {
		t.Errorf("Do(a) = %q, want A", v)
	}
	if v, _ := m.Do("a", func() (string, error) { return "again", nil }); v != "A" {
		t.Errorf("second Do(a) = %q, want the cached A", v)
	}
}
