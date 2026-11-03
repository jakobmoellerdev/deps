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
