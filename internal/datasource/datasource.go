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
	// A *SkipError skips d instead of failing it.
	Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error)
	// Digest returns the digest of version; used when Release.Digest is "".
	Digest(ctx context.Context, d dep.Dep, version string) (string, error)
}

// SkipError is returned by Releases when d cannot be looked up for a reason that is not a
// failure, only known once the datasource reads d's configuration (a Cargo registry name no
// configuration defines). The engine reports d as skipped with Reason instead of as an error.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return e.Reason }

// Timer is implemented by datasources that know when a version was published. It is optional
// and called lazily, only for versions that end up in a pull request.
type Timer interface {
	// ReleaseTime returns when version of d was published; the zero time with a nil error
	// means unknown.
	ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error)
}

// ReleaseBound is implemented by Timers that know, without per-version lookups, a time no
// version of a dependency was published after (npm's abbreviated `modified`). It is optional;
// the engine skips a minimumReleaseAge lookup when the bound is already old enough, which
// cannot change the outcome since every release time is at or before it.
type ReleaseBound interface {
	// ReleasedBy returns the bound for d from data already fetched by Releases, or the zero
	// time when unknown.
	ReleasedBy(d dep.Dep) time.Time
}

// Planner is implemented by datasources that use the full set of deps they will be asked
// about before the first lookup, e.g. to keep only the wanted charts of a repository index.
// It is optional; the engine calls Plan once per run, before any Releases call, with the
// updatable deps of the datasource.
type Planner interface {
	Plan(deps []dep.Dep)
}

// SourceRepo is implemented by datasources that know the source repository of a package
// (npm `repository`, OCI `org.opencontainers.image.source`). It is optional.
type SourceRepo interface {
	// SourceRepo returns the repository reference of version of d as published (for example
	// `git+https://github.com/o/r.git` or `github:o/r`), or "" when unknown; callers normalize it.
	SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error)
}

// Revision is the newest commit of a dependency that tracks one (a Go pseudo-version).
type Revision struct {
	Version string    // as written into the file (the Go pseudo-version, or a release)
	Digest  string    // commit hash
	Time    time.Time // commit time; zero = unknown
}

// HeadSource is implemented by datasources whose deps can track a commit instead of a release
// (Go pseudo-versions). It is optional.
type HeadSource interface {
	// Head returns the newest revision of d (Go: the module's `@latest`, a pseudo-version of
	// the default branch's tip when the module has no newer release). dir is as for Releases.
	Head(ctx context.Context, d dep.Dep, dir string) (Revision, error)
}

// ModulePath is a successor module path of a dependency and its releases.
type ModulePath struct {
	Name     string // e.g. github.com/google/go-github/v93
	Releases []Release
}

// MajorPathSource is implemented by datasources whose major versions live under their own
// package name (Go module paths `<path>/v<N>`). It is optional.
type MajorPathSource interface {
	// MajorPaths returns the successive major module paths of d that exist (`/v<N+1>`,
	// `/v<N+2>`, …, stopping at the first missing one), lowest first, each with its releases.
	MajorPaths(ctx context.Context, d dep.Dep, dir string) ([]ModulePath, error)
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
	// Checksum returns the lowercase hex checksum for version as written; release is the
	// datasource's release name of version (they differ under extractVersion).
	Checksum(ctx context.Context, d dep.Dep, version, release string) (string, error)
}

// Overview documents what all datasources share (HTTP client, retries, rate limits, dedupe).
var Overview = refdoc.Section{
	Title: "Shared behavior",
	Items: []string{
		"All network calls share one HTTP/2 client. Concurrent requests are limited per upstream with `--concurrency` n (default 32): the npm registry and OSV get 2n slots each, container registries (Docker Hub, GHCR, Helm OCI charts) n/2, and everything else (Go proxy, GitHub, git tags, Helm repositories, custom datasources) shares n. A busy upstream doesn't hold up the others.",
		"Idempotent requests are retried up to 3 times on connection errors and 429/500/502/503/504, with jittered exponential backoff, honoring `Retry-After`.",
		"Rate limits that reset within 60s are waited out. Longer ones fail fast with `rate limited by <host> until <time>`.",
		"Lookups are deduped within a run. There is no persistent cache.",
	},
}
