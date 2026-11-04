// Package manager defines the interface dependency-file managers implement.
package manager

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Env carries run-wide settings to Extract.
type Env struct {
	DefaultHost string // actions default host
	// Vendored reports whether the slash directory dir (relative to the scanned root)
	// vendors its modules. engine.Resolve supplies a filesystem check when nil.
	Vendored func(dir string) bool
}

// Manager extracts dependencies from the files it matches and edits them.
type Manager interface {
	Name() string
	// Match reports whether the slash path (relative to the root) is handled by this manager.
	Match(path string) bool
	// Extract returns the deps in src with byte spans into src.
	Extract(file string, src []byte, env Env) ([]dep.Dep, error)
	// Edits returns the byte edits for one update; the engine merges them per file.
	Edits(u dep.Update) ([]patch.Edit, error)
}

// Linker is implemented by managers whose deps refer to definitions in other files (a Helm
// chart repository alias declared in a k0s ClusterConfig, a Flux HelmRepository). Extract
// records the definitions it sees; once every file of a run was extracted, the engine calls
// Link once with the deps of each file the manager extracted without error, in discovery
// order. Link resolves the references in place (Source, Skip), may drop deps that turned out
// not to be deps, and forgets the definitions, so the next run starts empty.
type Linker interface {
	Link(files [][]dep.Dep)
}
