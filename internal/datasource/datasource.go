// Package datasource defines the interfaces the engine resolves dependencies through.
// It is a leaf package so datasource implementations need not import the engine;
// the engine re-exports these types as aliases.
package datasource

import (
	"context"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
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
