// Package manager defines the interface dependency-file managers implement.
package manager

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Env carries run-wide settings to Extract.
type Env struct {
	DefaultHost string // actions default host
	// TerraformRegistry is the registry host of hostless provider and module addresses
	// (config terraform.defaultRegistry); "" = the default of the file type.
	TerraformRegistry string
	// Vendored reports whether the slash directory dir (relative to the scanned root)
	// vendors its modules. engine.Resolve supplies a filesystem check when nil.
	Vendored func(dir string) bool
	// Exists reports whether the slash path (relative to the scanned root) exists, for
	// file-based conditions such as Maven profile `<activation><file>`. engine.Resolve
	// supplies a filesystem check when nil.
	Exists func(path string) bool
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

// LockReader is implemented by managers whose deps a lockfile resolves to an exact version
// (npm's package-lock.json). After Link, the engine calls ReadLocks once with the absolute
// scanned root and the deps of each file the manager extracted without error; it records the
// locked versions in place (dep.Dep.Locked, LockUnread) and returns the lockfiles it could not
// read.
type LockReader interface {
	ReadLocks(root string, files [][]dep.Dep) []error
}

// LockRoot is a lockfile lockFileMaintenance refreshes: its slash path and the manifest in the
// same directory, both relative to the scanned root. The manifest is the file the maintenance
// update belongs to; the manager's post-processor (engine.LockMaintainer) refreshes the lock.
type LockRoot struct{ Manifest, Lockfile string }

// LockMaintainer is implemented by managers whose lockfiles lockFileMaintenance refreshes
// (cargo's Cargo.lock). LockRoots returns the lock roots of files, the manager's files that
// extracted without error (slash paths relative to the absolute root), each once and sorted
// by Lockfile.
type LockMaintainer interface {
	LockRoots(root string, files []string) []LockRoot
}

// RangeUpdater is implemented by managers whose deps carry a range (dep.Dep.Range) that Edits
// rewrites as a whole rather than by its version token (PEP 440 specifier sets). InRange
// reports whether d's range admits v; NewRange returns the range Edits writes for v, or false
// when that leaves the range unchanged or does not admit v. The engine proposes such a v only
// as a lockfile-only update, and only when the range admits it.
type RangeUpdater interface {
	InRange(d dep.Dep, v string) bool
	NewRange(d dep.Dep, v string) (string, bool)
}

// FileEditor is implemented by managers whose updates also edit files other than the dep's
// own (a gomod major module path update rewrites the import paths in the module's .go
// files). The engine applies the edits together with the update; if the dep's file or one
// of the other files fails, all of them are dropped.
type FileEditor interface {
	// FileEdits returns the byte edits by slash path relative to root (absolute), excluding
	// u.Dep.File; nil when the update touches no other file.
	FileEdits(root string, u dep.Update) (map[string][]patch.Edit, error)
}
