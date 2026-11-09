package engine

import (
	"runtime"
	"slices"
	"testing"
	"time"
)

// The names of one release list are built once per run and shared; a list of another length,
// another backing array or after the run gets its own.
func TestReleaseNamesShared(t *testing.T) {
	c := newReleaseNames()
	rels := []Release{{Version: "1.0.0"}, {Version: "1.1.0"}, {Version: "2.0.0"}}
	a, b := c.of(rels), c.of(rels)
	if want := []string{"1.0.0", "1.1.0", "2.0.0"}; !slices.Equal(a, want) {
		t.Fatalf("names = %q, want %q", a, want)
	}
	if &a[0] != &b[0] {
		t.Error("the same list got new names")
	}
	if p := c.of(rels[:2]); !slices.Equal(p, []string{"1.0.0", "1.1.0"}) || &p[0] == &a[0] {
		t.Errorf("prefix names = %q, shared = %v", p, &p[0] == &a[0])
	}
	if s := c.of(rels[1:]); !slices.Equal(s, []string{"1.1.0", "2.0.0"}) {
		t.Errorf("suffix names = %q", s)
	}
	if n := c.of(slices.Clone(rels)); &n[0] == &a[0] {
		t.Error("a copy of the list shares its names")
	}
	if e := c.of(nil); e == nil || len(e) != 0 {
		t.Errorf("names of no releases = %#v, want empty", e)
	}
	c.done()
	if d := c.of(rels); !slices.Equal(d, a) || &d[0] == &a[0] || len(c.m) != 0 {
		t.Errorf("after done: names = %q, shared = %v, cached = %d", d, &d[0] == &a[0], len(c.m))
	}
	var none *releaseNames
	if n := none.of(rels); !slices.Equal(n, a) {
		t.Errorf("nil cache: names = %q", n)
	}
}

// A collected release list leaves the cache, so lists a datasource builds per lookup do not
// keep their names alive for the whole run.
func TestReleaseNamesDropCollected(t *testing.T) {
	c := newReleaseNames()
	func() {
		rels := make([]Release, 64)
		for i := range rels {
			rels[i].Version = "1.0." + string(rune('0'+i%10))
		}
		c.of(rels)
	}()
	for range 200 {
		runtime.GC()
		c.mu.Lock()
		n := len(c.m)
		c.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("names of a collected list stay cached")
}
