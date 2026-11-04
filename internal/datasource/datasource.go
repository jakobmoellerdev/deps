// Package datasource defines the interfaces the engine resolves dependencies through.
// It is a leaf package so datasource implementations need not import the engine;
// the engine re-exports these types as aliases.
package datasource

import (
	"context"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Release is one published version. Digest is "" when the datasource only knows it on
// request (see Datasource.Digest).
type Release struct{ Version, Digest string }

// Datasource lists the releases of a dependency.
type Datasource interface {
	// Releases lists every release of d. dir is the absolute directory of d.File
	// (go list needs it). The result may be shared between callers and must not be modified.
	Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error)
	// Digest returns the digest of version; used when Release.Digest is "".
	Digest(ctx context.Context, d dep.Dep, version string) (string, error)
}

// Timer is implemented by datasources that know when a version was published. It is optional
// and called lazily, only for versions that end up in a pull request.
type Timer interface {
	// ReleaseTime returns when version of d was published; the zero time with a nil error
	// means unknown.
	ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error)
}

// SourceRepo is implemented by datasources that know the source repository of a package
// (npm `repository`, OCI `org.opencontainers.image.source`). It is optional.
type SourceRepo interface {
	// SourceRepo returns the repository reference of version of d as published (for example
	// `git+https://github.com/o/r.git` or `github:o/r`), or "" when unknown; callers normalize it.
	SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error)
}

// Vuln is an advisory affecting a dependency and the versions that fix it.
type Vuln struct {
	Advisory dep.Advisory
	Fixed    []string // versions (dep version syntax) that fix it
}

// SecuritySource looks up advisories.
type SecuritySource interface {
	// Advisories returns the vulnerabilities per dep (same indexing as deps); it is called
	// once per run with all updatable deps.
	Advisories(ctx context.Context, deps []dep.Dep) ([][]Vuln, error)
}

// ChecksumSource looks up the checksum of a release artifact of d (see dep.ChecksumSource).
type ChecksumSource interface {
	// Checksum returns the lowercase hex checksum for version.
	Checksum(ctx context.Context, d dep.Dep, version string) (string, error)
}

// Overview documents what all datasources share (HTTP client, retries, rate limits, dedupe).
var Overview = refdoc.Section{
	Title: "Shared behavior",
	Items: []string{
		"All network calls share one HTTP/2 client and one semaphore of `--concurrency` slots (default 32).",
		"Idempotent requests are retried up to 3 times on connection errors and 429/500/502/503/504, with jittered exponential backoff, honoring `Retry-After`.",
		"Rate limits that reset within 60s are waited out. Longer ones fail fast with `rate limited by <host> until <time>`.",
		"Lookups are deduped within a run. There is no persistent cache.",
	},
}
