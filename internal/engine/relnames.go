package engine

import (
	"runtime"
	"sync"
	"weak"
)

// releaseNames shares the version names of release lists between the deps of one Resolve:
// datasources return one shared list per package, which every manifest of a monorepo that
// declares the package looks up (hundreds of versions for each of hundreds of deps). Lists
// are keyed by their first element and length; the key is weak and dropped once the list is
// collected, so a datasource returning fresh lists does not keep their names alive. The names
// are shared and must not be modified. A nil *releaseNames builds the names every time.
type releaseNames struct {
	mu sync.Mutex
	m  map[relsKey][]string // nil once Resolve returned (done)
}

type relsKey struct {
	first weak.Pointer[Release]
	n     int
}

func newReleaseNames() *releaseNames {
	return &releaseNames{m: map[relsKey][]string{}}
}

// of returns the Version of each release in rels, in order.
func (c *releaseNames) of(rels []Release) []string {
	if c == nil || len(rels) == 0 {
		return versionNames(rels)
	}
	k := relsKey{weak.Make(&rels[0]), len(rels)}
	c.mu.Lock()
	names, ok := c.m[k]
	c.mu.Unlock()
	if ok {
		return names
	}
	names = versionNames(rels)
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.m[k]; ok {
		return prev // a concurrent lookup of the same list came first
	}
	if c.m != nil {
		c.m[k] = names
		runtime.AddCleanup(&rels[0], c.drop, k)
	}
	return names
}

// drop forgets the names of a collected list.
func (c *releaseNames) drop(k relsKey) {
	c.mu.Lock()
	delete(c.m, k)
	c.mu.Unlock()
}

// done releases the cache; cleanups still pending find it empty.
func (c *releaseNames) done() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

func versionNames(rels []Release) []string {
	names := make([]string, len(rels))
	for i := range rels {
		names[i] = rels[i].Version
	}
	return names
}
