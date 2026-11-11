// Package jsonnetbundler is the jsonnet-bundler manager: the git dependencies of
// jsonnetfile.json (jb), locked in jsonnetfile.lock.json. A version tag gets tag updates, a
// commit digest updates of the default branch, and a branch digest updates of the commit
// jsonnetfile.lock.json locks; the lock (and a committed vendor directory) is regenerated
// with `jb update` (Locker).
package jsonnetbundler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "jsonnet-bundler"

// DepTypeDependency is the dep type of a `dependencies[]` entry.
const DepTypeDependency = "dependency"

const (
	manifestName = "jsonnetfile.json"
	lockFile     = "jsonnetfile.lock.json"
	dsTags       = "git-tags"
	dsRefs       = "git-refs"
	// defaultBranch is the version of a commit-pinned dependency: git-refs' default branch.
	defaultBranch = "HEAD"
)

// Skip reasons.
const (
	skipLocal      = "local dependency"
	skipURL        = "unrecognized remote"
	skipNoVersion  = "no version"
	skipNoLock     = "no jsonnetfile.lock.json"
	skipUnreadable = "unreadable jsonnetfile.lock.json"
	skipNotLocked  = "not in jsonnetfile.lock.json"
	skipLocate     = "cannot locate value"
)

type jbManager struct{}

// New returns the jsonnet-bundler manager.
func New() manager.Manager { return jbManager{} }

func (jbManager) Name() string { return Name }

// Match reports whether the slash path is a jsonnetfile.json outside a vendor directory (jb
// vendors its dependencies with their own jsonnetfile.json).
func (jbManager) Match(p string) bool {
	return path.Base(p) == manifestName && !strings.HasPrefix(p, "vendor/") && !strings.Contains(p, "/vendor/")
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (jbManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{manifestName}}
}

// Extract returns one dep per `dependencies[]` entry. The file is JSON, read as YAML (a
// superset), so values are located with yamlspan.
func (jbManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	entries := yamlspan.Seq(yamlspan.Get(doc, "dependencies"))
	if len(entries) == 0 {
		return nil, nil
	}
	lines := yamlspan.LineStarts(src)
	deps := make([]dep.Dep, 0, len(entries))
	for _, e := range entries {
		if d, ok := entry(file, src, lines, e); ok {
			deps = append(deps, d)
		}
	}
	return deps, nil
}

// entry returns the dep of the dependency e; ok is false for an entry that is no dependency
// object.
func entry(file string, src []byte, lines []int, e *yaml.Node) (dep.Dep, bool) {
	if e.Kind != yaml.MappingNode {
		return dep.Dep{}, false
	}
	d := dep.Dep{Manager: Name, Datasource: dsTags, Versioning: "tag", DepType: DepTypeDependency, File: file, Line: e.Line}
	source := yamlspan.Get(e, "source")
	git := yamlspan.Get(source, "git")
	if git == nil {
		if local := yamlspan.Value(yamlspan.Get(yamlspan.Get(source, "local"), "directory")); local != "" {
			d.Name, d.Skip = local, skipLocal
			return d, true
		}
		return dep.Dep{}, false
	}
	remote := yamlspan.Value(yamlspan.Get(git, "remote"))
	name, gitSource := depName(remote, yamlspan.Value(yamlspan.Get(git, "subdir")))
	if gitSource == "" {
		d.Name, d.Skip = remote, skipURL
		return d, true
	}
	d.Name, d.Source = name, gitSource
	v := yamlspan.Get(e, "version")
	if v == nil || v.Kind != yaml.ScalarNode || v.Value == "" {
		d.Skip = skipNoVersion
		return d, true
	}
	d.Line, d.Version = v.Line, v.Value
	at := yamlspan.Locate(src, lines, v)
	var span dep.Span
	if at >= 0 {
		span = dep.Span{Start: at, End: at + len(v.Value)}
	}
	switch {
	case manager.IsHex(v.Value, 40):
		d.Datasource, d.Version = dsRefs, defaultBranch
		d.Digest, d.DigestSpan = v.Value, span
		if at < 0 {
			d.Skip = skipLocate
		}
	case isVersion(v.Value):
		d.RefSpan = span
		if at < 0 {
			d.Skip = skipLocate
		}
	default:
		// A branch: its commit is the one jsonnetfile.lock.json locks (ReadLocks).
		d.Datasource = dsRefs
	}
	return d, true
}

// isVersion reports whether ref is a version tag (`v1`, `1.2`, `v1.2.3`).
func isVersion(ref string) bool {
	_, ok := versioning.ParseTag(ref)
	return ok
}

// lockEntry is a dependency of jsonnetfile.lock.json.
type lockEntry struct {
	Source struct {
		Git *struct {
			Remote string `json:"remote"`
			Subdir string `json:"subdir"`
		} `json:"git"`
	} `json:"source"`
	Version string `json:"version"`
}

// depName returns the name of the git dependency remote, subdir: Renovate's depName
// host/path[/subdir], the same for jsonnetfile.json and its lock, which jb writes with `.git`
// appended to the remote, and its https source; both are "" for an unrecognized remote.
func depName(remote, subdir string) (name, source string) {
	if _, source = manager.GitRepo(remote); source == "" {
		return "", ""
	}
	return path.Join(strings.TrimPrefix(source, "https://"), strings.Trim(subdir, "/")), source
}

// ReadLocks completes the branch deps of each jsonnetfile.json with the commit
// jsonnetfile.lock.json next to it locks (Digest). An unreadable lock is reported once and
// its branch deps are skipped.
func (jbManager) ReadLocks(root string, files [][]dep.Dep) []error {
	var errs []error
	for _, deps := range files {
		if !slices.ContainsFunc(deps, branchDep) {
			continue
		}
		file := path.Join(path.Dir(deps[0].File), lockFile)
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if errors.Is(err, fs.ErrNotExist) {
			skipBranches(deps, skipNoLock)
			continue
		}
		var l struct {
			Dependencies []lockEntry `json:"dependencies"`
		}
		if err == nil {
			err = json.Unmarshal(b, &l)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file, err))
			skipBranches(deps, skipUnreadable)
			continue
		}
		locked := map[string]string{}
		for _, e := range l.Dependencies {
			if e.Source.Git != nil {
				name, _ := depName(e.Source.Git.Remote, e.Source.Git.Subdir)
				locked[name] = e.Version
			}
		}
		for i := range deps {
			if !branchDep(deps[i]) {
				continue
			}
			if c := locked[deps[i].Name]; manager.IsHex(c, 40) {
				deps[i].Digest = c
			} else {
				deps[i].Skip = skipNotLocked
			}
		}
	}
	return errs
}

// branchDep reports whether d tracks a branch: a git-refs dep without a commit written in
// jsonnetfile.json.
func branchDep(d dep.Dep) bool {
	return d.Skip == "" && d.Datasource == dsRefs && d.DigestSpan == (dep.Span{})
}

func skipBranches(deps []dep.Dep, reason string) {
	for i := range deps {
		if branchDep(deps[i]) {
			deps[i].Skip = reason
		}
	}
}

// Edits rewrites the version of jsonnetfile.json: a tag, or a commit written there. A branch
// keeps its name; its new commit only moves jsonnetfile.lock.json (Locker), so its update has
// no edits.
func (jbManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewVersion != "" && u.NewVersion != d.Version {
		if d.Datasource != dsTags || d.RefSpan == (dep.Span{}) {
			return nil, errors.New("cannot change the branch of a dependency")
		}
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	if d.DigestSpan != (dep.Span{}) && u.NewDigest != "" && u.NewDigest != d.Digest {
		edits = append(edits, patch.Edit{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: u.NewDigest})
	}
	return edits, nil
}

// LockRoots implements manager.LockMaintainer: the jsonnetfile.lock.json of each
// jsonnetfile.json with one.
func (jbManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if _, ok := (Locker{}).Scope(context.Background(), root, f); ok {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: path.Join(path.Dir(f), lockFile)})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}
