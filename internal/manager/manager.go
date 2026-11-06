// Package manager defines the interface dependency-file managers implement.
package manager

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/imageref"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Env carries run-wide settings to Extract, and YAML, which is per file.
type Env struct {
	DefaultHost string // actions default host
	GitLabURL   string // GitLab instance base URL without trailing slash; "" means https://gitlab.com
	// TerraformRegistry is the registry host of hostless provider and module addresses
	// (config terraform.defaultRegistry); "" = the default of the file type.
	TerraformRegistry string
	// Exists reports whether the slash path (relative to the scanned root) exists, for
	// file-based conditions such as Maven profile `<activation><file>`. engine.Resolve
	// supplies a filesystem check when nil.
	Exists func(path string) bool
	// Gitlinks returns the commits of the gitlinks (submodules) recorded in the git index at
	// paths, slash paths relative to the scanned root; paths without a gitlink are absent.
	// engine.Resolve supplies `git ls-files --stage` when nil.
	Gitlinks func(paths []string) (map[string]string, error)
	// Superproject returns the `remote.origin.url` and the checked-out branch of the git work
	// tree of the scanned root ("" when unset or HEAD is detached), which relative submodule
	// urls and `branch = .` resolve against. engine.Resolve supplies a git lookup when nil.
	Superproject func() (origin, branch string, err error)
	// YAML is the shared single-document parse of the file being extracted: the engine sets
	// a new one per file and passes the same src to every manager of that file, so managers
	// reading the same workflow parse it once. nil parses anew.
	YAML *yamlspan.Parsed
}

// GitRepo turns an https/http/ssh/git or scp-style (`git@host:owner/repo`) git URL into the
// owner/repo path and the source git-tags reads: the http(s) URL as written (scheme and port
// kept), https for the others, without `.git`; source is "" when the URL is not recognized.
func GitRepo(s string) (name, source string) {
	scheme := "https"
	var host, p string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", ""
		}
		switch u.Scheme {
		case "https", "http": // as written, with the port
			scheme, host = u.Scheme, u.Host
		case "ssh", "git": // the port is git's, not HTTP's
			host = u.Hostname()
		default:
			return "", ""
		}
		p = u.Path
	default: // git@host:owner/repo
		at := strings.IndexByte(s, '@')
		c := strings.IndexByte(s, ':')
		if c < 0 || at > c {
			return "", ""
		}
		host, p = s[at+1:c], s[c+1:]
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if host == "" || !strings.Contains(p, "/") {
		return "", ""
	}
	return p, scheme + "://" + host + "/" + p
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

// RangeRewriter is implemented by managers whose Edits rewrite a dep's range (dep.Dep.Range) as a
// whole per rangeStrategy (dep.Update.RangeStrategy): npm and cargo ranges, Python specifiers.
// NewRange returns the range Edits writes for v under strategy (a config.RangeStrategies value
// other than auto; "" is the manager's default rewrite), or false when that leaves the range
// unchanged or does not admit v. A version the range admits is bumped (its floor raised to v)
// under every strategy but pin; one outside it is written per strategy: widen widens the range,
// pin writes v exactly, any other replaces the range. The engine needs it for widen and pin; a
// v it cannot write is not proposed.
type RangeRewriter interface {
	NewRange(d dep.Dep, v, strategy string) (string, bool)
}

// RangeUpdater is a RangeRewriter whose range the engine matches through the manager
// (PEP 440 specifier sets) rather than as a Masterminds constraint: InRange reports whether d's
// range admits v. An admitted v NewRange cannot raise is proposed only as a lockfile-only update.
type RangeUpdater interface {
	RangeRewriter
	InRange(d dep.Dep, v string) bool
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

// Renamer is implemented by managers whose deps a replacement update (dep.Replacement) can
// move to another package: Edits rewrites the name with u.NewName (NameEdits). Rename returns
// d as the datasource looks it up under the new name (Name, Source), or an error when name is
// no valid name for the manager. Deps without NameParts are not renamed.
type Renamer interface {
	Rename(d dep.Dep, name string) (dep.Dep, error)
}

// NameEdits returns the edits writing name over d's NameParts: a NameWhole part gets the whole
// name, registry and repository parts get the name split by imageref.SplitRegistry, group and
// artifact parts the Maven coordinates split at the first ':'. Parts that already read their
// new text get no edit.
func NameEdits(d dep.Dep, name string) ([]patch.Edit, error) {
	if !d.Renamable() {
		return nil, errors.New("cannot locate the name")
	}
	edits := make([]patch.Edit, 0, len(d.NameParts))
	for _, p := range d.NameParts {
		v := name
		switch p.Role {
		case dep.NameRegistry, dep.NameRepository:
			reg, repo := imageref.SplitRegistry(name)
			if v = repo; p.Role == dep.NameRegistry {
				v = reg
			}
		case dep.NameGroup, dep.NameArtifact:
			group, artifact, ok := strings.Cut(name, ":")
			if !ok {
				return nil, fmt.Errorf("%q is not group:artifact", name)
			}
			if v = artifact; p.Role == dep.NameGroup {
				v = group
			}
		}
		if v != p.Text {
			edits = append(edits, patch.Edit{Start: p.Span.Start, End: p.Span.End, Old: p.Text, New: v})
		}
	}
	return edits, nil
}
