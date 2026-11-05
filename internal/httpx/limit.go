package httpx

import (
	"context"
	"slices"
)

// Limits are the request semaphores of one run, one pool per upstream, sized from
// --concurrency n. A datasource holds a slot of its pool for each request (or `go list` run),
// so one busy upstream can't starve the others: OSV advisory fetches start as soon as their
// querybatch returns instead of queueing behind hundreds of npm packuments, and the npm
// registry can use more slots than Docker Hub, which answers bursts with 429s.
type Limits struct {
	// Default bounds every datasource without a pool of its own (Go proxy and toolchains,
	// GitHub, git tags, Helm repositories, Node.js releases, custom datasources, checksums,
	// release notes): n slots.
	Default chan struct{}
	// NPM bounds npm registry requests: 2n slots. The registry is a CDN; on backstage
	// (~750 packuments) 64 concurrent requests finish the lookups in half the time of 32.
	NPM chan struct{}
	// Docker bounds container registry operations (Docker Hub, GHCR, Helm OCI charts): n/2
	// slots, at least 1, so Docker Hub sees no more concurrent requests than it did when the
	// registries shared one pool with the busy npm and GitHub lookups.
	Docker chan struct{}
	// OSV bounds OSV querybatch and advisory requests: 2n slots, so a few hundred advisory
	// fetches finish alongside the release lookups rather than after them.
	OSV chan struct{}
	// Crates bounds crates.io sparse index requests: 2n slots. index.crates.io is a CDN
	// (Cargo itself fetches it with many parallel requests); the crates.io API calls for
	// source repositories share the pool.
	Crates chan struct{}
	// Maven bounds Maven repository requests (Central, Google Maven, the Gradle plugin portal,
	// pom.xml repositories): 2n slots. Central is a CDN, and a Gradle catalog asks two
	// repositories per artifact.
	Maven chan struct{}
}

// NewLimits returns the pools for --concurrency n (at least 1).
func NewLimits(n int) Limits {
	return Limits{
		Default: make(chan struct{}, n),
		NPM:     make(chan struct{}, 2*n),
		Docker:  make(chan struct{}, max(n/2, 1)),
		OSV:     make(chan struct{}, 2*n),
		Crates:  make(chan struct{}, 2*n),
		Maven:   make(chan struct{}, 2*n),
	}
}

// PoolUse is the number of held slots of one pool of Limits.
type PoolUse struct {
	Name string // "npm", "docker", "osv", "crates", "maven", "default"
	N    int
}

// InFlight returns the pools with held slots, busiest first (ties in field order): the
// requests running right now per upstream. It only reads the channel lengths.
func (l Limits) InFlight() []PoolUse {
	var out []PoolUse
	for _, p := range []PoolUse{{"npm", len(l.NPM)}, {"docker", len(l.Docker)}, {"osv", len(l.OSV)}, {"crates", len(l.Crates)}, {"maven", len(l.Maven)}, {"default", len(l.Default)}} {
		if p.N > 0 {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b PoolUse) int { return b.N - a.N })
	return out
}

// Acquire takes a slot of sem, waiting until one is free or ctx is done (then it returns
// ctx.Err()); release frees the slot. A nil sem is unbounded: Acquire returns a no-op release
// and no error, even for a done ctx, which the request itself then reports.
func Acquire(ctx context.Context, sem chan struct{}) (release func(), err error) {
	if sem == nil {
		return noRelease, nil
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func noRelease() {}
