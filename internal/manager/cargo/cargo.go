// Package cargo extracts crate dependencies from Cargo.toml manifests, reads the versions
// Cargo.lock locks them to, and updates Cargo.lock after an update (see Locker).
package cargo

import (
	"cmp"
	"fmt"
	"iter"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Name is the manager name.
const Name = "cargo"

// Datasource is the datasource of registry dependencies.
const Datasource = "crate"

// Versioning is the versioning scheme of registry dependencies.
const Versioning = "cargo"

// DepTypes.
const (
	DepTypeDependencies = "dependencies"
	DepTypeDev          = "dev-dependencies"
	DepTypeBuild        = "build-dependencies"
	DepTypeWorkspace    = "workspace.dependencies"
	DepTypePatch        = "patch"
	DepTypeReplace      = "replace"
)

// sections maps the dependency table names (with Cargo's legacy underscore spellings) to
// their DepType.
var sections = map[string]string{
	"dependencies":       DepTypeDependencies,
	"dev-dependencies":   DepTypeDev,
	"dev_dependencies":   DepTypeDev,
	"build-dependencies": DepTypeBuild,
	"build_dependencies": DepTypeBuild,
}

var matchRe = regexp.MustCompile(`(^|/)Cargo\.toml$`)

type cargoManager struct{}

// New returns the cargo manager.
func New() manager.Manager { return cargoManager{} }

func (cargoManager) Name() string { return Name }

// Match reports whether the slash path is a Cargo.toml file. The suffix check skips the regexp
// for the paths that cannot match.
func (cargoManager) Match(p string) bool {
	return strings.HasSuffix(p, "Cargo.toml") && matchRe.MatchString(p)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (cargoManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{"Cargo.toml"}}
}

// Extract returns one Dep per entry of the [dependencies], [dev-dependencies] and
// [build-dependencies] tables (also below [target.<spec>]), of [workspace.dependencies], of
// every [patch.<source>] table and of [replace], ordered by line. Entries may be strings,
// inline tables, dotted keys or [dependencies.<name>] tables. RefSpan covers the whole
// requirement (Range); Version is the version a bump raises.
func (cargoManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	root, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	var deps []dep.Dep
	for depType, t := range depTables(root) {
		for _, k := range t.Keys {
			deps = append(deps, entryDep(file, depType, k, t.Fields[k]))
		}
	}
	slices.SortStableFunc(deps, func(a, b dep.Dep) int { return cmp.Compare(a.Line, b.Line) })
	return deps, nil
}

// depTables yields, in document order, the dependency tables of the manifest root with the
// DepType of their entries: the sections tables (also below [target.<spec>]),
// [workspace.dependencies], every [patch.<source>] table and [replace]. Values that are not
// tables are skipped.
func depTables(root *tomlspan.Value) iter.Seq2[string, *tomlspan.Value] {
	return func(yield func(string, *tomlspan.Value) bool) {
		emit := func(depType string, t *tomlspan.Value) bool {
			return t == nil || t.Kind != tomlspan.Table || yield(depType, t)
		}
		for _, k := range root.Keys {
			v := root.Fields[k]
			ok := true
			switch {
			case sections[k] != "":
				ok = emit(sections[k], v)
			case k == "target" && v.Kind == tomlspan.Table:
				for _, spec := range v.Keys {
					t := v.Fields[spec]
					if t.Kind != tomlspan.Table {
						continue
					}
					for _, s := range t.Keys {
						if sections[s] != "" && !emit(sections[s], t.Fields[s]) {
							return
						}
					}
				}
			case k == "workspace":
				ok = emit(DepTypeWorkspace, root.Get("workspace", "dependencies"))
			case k == "patch" && v.Kind == tomlspan.Table:
				for _, source := range v.Keys {
					if !emit(DepTypePatch, v.Fields[source]) {
						return
					}
				}
			case k == "replace":
				ok = emit(DepTypeReplace, v)
			}
			if !ok {
				return
			}
		}
	}
}

// entryDep makes the Dep of the entry key = v in a table of depType.
func entryDep(file, depType, key string, v *tomlspan.Value) dep.Dep {
	d := dep.Dep{
		Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: depType,
		File: file, Line: v.Line, Name: key,
	}
	if depType == DepTypeReplace {
		// A [replace] key is a package ID: "name:version" or "name@version".
		d.Name, _, _ = strings.Cut(strings.ReplaceAll(key, "@", ":"), ":")
	}
	var ver *tomlspan.Value
	var gitURL string
	var tag, rev, branch *tomlspan.Value
	var workspace, path bool
	switch v.Kind {
	case tomlspan.String:
		ver = v
	case tomlspan.Table:
		str := func(k string) *tomlspan.Value {
			if f := v.Fields[k]; f != nil && f.Kind == tomlspan.String {
				return f
			}
			return nil
		}
		if p := str("package"); p != nil {
			d.Name = p.Str
		}
		if ver = str("version"); ver != nil {
			d.Line = ver.Line
		}
		if g := str("git"); g != nil {
			gitURL = g.Str
		}
		tag, rev, branch = str("tag"), str("rev"), str("branch")
		path = v.Fields["path"] != nil
		if w := v.Fields["workspace"]; w != nil && w.Kind == tomlspan.Scalar && w.Str == "true" {
			workspace = true
		}
		if r := str("registry"); r != nil && r.Str != "crates-io" {
			d.Source = r.Str
		}
		if r := str("registry-index"); r != nil {
			d.Source = r.Str
		}
	}
	switch {
	case workspace:
		d.Skip = "workspace dependency"
	case path:
		d.Skip = "path dependency"
	case gitURL != "":
		gitDep(&d, gitURL, tag, rev, branch)
	case ver == nil:
		d.Skip = "no version"
	case !ver.Exact:
		d.Skip = "cannot locate value"
	default:
		if !SetRequirement(&d, ver.Str) {
			d.Skip = "unsupported requirement"
			break
		}
		d.RefSpan = dep.Span{Start: ver.Start, End: ver.End}
	}
	return d
}

// SetRequirement sets d's Range (raw), Version (its bare version) and Constraint (its
// Masterminds form when that differs) from the Cargo requirement raw; false when raw is not
// one cargo deps support. Managers writing Cargo requirements elsewhere (Bazel crate.spec) use
// it with the cargo manager's Edits and NewRange.
func SetRequirement(d *dep.Dep, raw string) bool {
	req, ok := parseRequirement(raw)
	if !ok {
		return false
	}
	d.Range, d.Version, d.Constraint = raw, req.version(), ""
	if c := req.constraint(); c != raw {
		d.Constraint = c
	}
	return true
}

// gitDep makes a `git` dependency on an http(s) repository with a `tag` a git-tags dep, and
// one with a commit `rev` (7 to 40 hex digits) a git-refs dep whose digest follows the head of
// its `branch`, else of the default branch (Renovate: git-refs on `rev`). Any other git
// dependency (only a branch, the default branch, a rev that is no commit, an ssh URL) is
// skipped. An abbreviated rev is expanded from Cargo.lock (ReadLocks).
func gitDep(d *dep.Dep, raw string, tag, rev, branch *tomlspan.Value) {
	d.Source = ""
	u, err := url.Parse(raw)
	switch {
	case (tag == nil && rev == nil) || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		d.Skip = "git dependency"
		return
	case tag == nil && !commitRe.MatchString(rev.Str):
		d.Skip = "git rev is not a commit"
		return
	case tag != nil && !tag.Exact, tag == nil && (!rev.Exact || branch != nil && !branch.Exact):
		d.Skip = "cannot locate value"
		return
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	d.Source = strings.TrimSuffix(strings.TrimSuffix(u.String(), "/"), ".git")
	d.Versioning = "tag"
	if tag != nil {
		d.Datasource = "git-tags"
		d.Version, d.RefSpan, d.Line = tag.Str, dep.Span{Start: tag.Start, End: tag.End}, tag.Line
		return
	}
	d.Datasource, d.Version = DatasourceGitRefs, defaultBranch
	if branch != nil {
		d.Version = branch.Str
	}
	d.Digest, d.DigestSpan, d.Line = rev.Str, dep.Span{Start: rev.Start, End: rev.End}, rev.Line
}

// DatasourceGitRefs is the datasource of git dependencies pinned to a commit `rev`.
const DatasourceGitRefs = "git-refs"

// defaultBranch is the version of a rev dependency without `branch`: the repository's HEAD.
const defaultBranch = "HEAD"

// commitRe is a full or abbreviated commit hash.
var commitRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// fullCommit is the length of a full SHA-1 commit hash.
const fullCommit = 40

// Edits replaces the git tag or the commit rev (at the length it was written with), or
// rewrites the requirement for the new version under the update's rangeStrategy (rewrite):
// the operators, wildcards and precision are kept.
func (cargoManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == DatasourceGitRefs {
		n := d.DigestSpan.End - d.DigestSpan.Start
		switch {
		case u.NewVersion != d.Version:
			return nil, fmt.Errorf("cannot change the branch of git dependency %s", d.Name)
		case len(u.NewDigest) < n || len(d.Digest) < n:
			return nil, fmt.Errorf("git dependency %s: digest %q shorter than rev %q", d.Name, u.NewDigest, d.Digest)
		}
		return []patch.Edit{{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest[:n], New: strings.ToLower(u.NewDigest[:n])}}, nil
	}
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	req, ok := parseRequirement(d.Range)
	if !ok {
		return nil, fmt.Errorf("unsupported requirement %q", d.Range)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: req.rewrite(u.NewVersion, u.RangeStrategy)}}, nil
}

// NewRange implements manager.RangeRewriter (requirement.rewrite).
func (cargoManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	req, ok := parseRequirement(d.Range)
	if !ok {
		return "", false
	}
	out := req.rewrite(v, strategy)
	if out == d.Range {
		return "", false
	}
	if n, ok := parseRequirement(out); !ok || !n.admits(v) {
		return "", false
	}
	return out, true
}
