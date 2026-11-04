package httpx

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
}

// NewLimits returns the pools for --concurrency n (at least 1).
func NewLimits(n int) Limits {
	return Limits{
		Default: make(chan struct{}, n),
		NPM:     make(chan struct{}, 2*n),
		Docker:  make(chan struct{}, max(n/2, 1)),
		OSV:     make(chan struct{}, 2*n),
	}
}
