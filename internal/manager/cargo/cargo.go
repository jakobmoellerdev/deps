// Package cargo extracts crate dependencies from Cargo.toml manifests and regenerates
// Cargo.lock after an update (see Locker).
package cargo

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name.
const Name = "cargo"

// Datasource is the datasource of crates.io dependencies.
const Datasource = "crate"

// DepTypes.
const (
	DepTypeDependencies = "dependencies"
	DepTypeDev          = "dev-dependencies"
	DepTypeBuild        = "build-dependencies"
	DepTypeWorkspace    = "workspace.dependencies"
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

var (
	matchRe = regexp.MustCompile(`(^|/)Cargo\.toml$`)
	// reqRe is the supported requirement grammar: one comparator with an optional ^, ~, = or
	// >= operator and a MAJOR[.MINOR[.PATCH[-prerelease]]] version (group 2).
	reqRe = regexp.MustCompile(`^\s*(\^|~|=|>=)?\s*(\d+(?:\.\d+(?:\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)?)?)\s*$`)
)

type cargoManager struct{}

// New returns the cargo manager.
func New() manager.Manager { return cargoManager{} }

func (cargoManager) Name() string { return Name }

// Match reports whether the slash path is a Cargo.toml file.
func (cargoManager) Match(p string) bool { return matchRe.MatchString(p) }

// Extract returns one Dep per entry of the [dependencies], [dev-dependencies] and
// [build-dependencies] tables (also below [target.<spec>]) and of [workspace.dependencies],
// in the order they first appear. Entries may be strings, inline tables, dotted keys or
// [dependencies.<name>] tables. RefSpan covers only the version, so the operator is kept.
func (cargoManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	x := extractor{src: src, line: 1, index: map[string]int{}}
	if err := scanTOML(src, x.add); err != nil {
		return nil, err
	}
	deps := make([]dep.Dep, len(x.recs))
	for i := range x.recs {
		deps[i] = x.recs[i].dep(file)
	}
	return deps, nil
}

// Edits replaces the version token.
func (cargoManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// record collects the keys of one dependency entry.
type record struct {
	depType, key  string
	line, tagLine int
	version, tag  *item
	pkg, git      string
	path          bool
	workspace     bool
	registry      bool
}

type extractor struct {
	src   []byte
	recs  []record
	index map[string]int // depType + target + key -> index into recs

	// pos and line track the line of the items, whose offsets only grow.
	pos, line int
}

func (x *extractor) lineAt(off int) int {
	x.line += bytes.Count(x.src[x.pos:off], []byte{'\n'})
	x.pos = off
	return x.line
}

// depKey splits a key path into its DepType, target and the rest below the dependency table;
// depType is "" outside dependency tables.
func depKey(key []string) (depType, target string, rest []string) {
	switch {
	case len(key) >= 1 && sections[key[0]] != "":
		return sections[key[0]], "", key[1:]
	case len(key) >= 3 && key[0] == "target" && sections[key[2]] != "":
		return sections[key[2]], key[1], key[3:]
	case len(key) >= 2 && key[0] == "workspace" && key[1] == "dependencies":
		return DepTypeWorkspace, "", key[2:]
	}
	return "", "", nil
}

func (x *extractor) add(it item) {
	depType, target, rest := depKey(it.key)
	if depType == "" || len(rest) == 0 || it.kind == 'H' {
		return
	}
	id := depType + "\x00" + target + "\x00" + rest[0]
	i, ok := x.index[id]
	if !ok {
		i = len(x.recs)
		x.index[id] = i
		x.recs = append(x.recs, record{depType: depType, key: rest[0], line: x.lineAt(it.off)})
	}
	r := &x.recs[i]
	switch {
	case len(rest) == 1 && it.kind == 's':
		r.version = &it
		r.line = x.lineAt(it.off)
	case len(rest) == 2:
		r.attr(rest[1], &it, x)
	}
}

func (r *record) attr(name string, it *item, x *extractor) {
	switch name {
	case "version":
		if it.kind == 's' {
			r.version = it
			r.line = x.lineAt(it.off)
		}
	case "package":
		r.pkg = it.str
	case "path":
		r.path = true
	case "git":
		r.git = it.str
	case "tag":
		if it.kind == 's' {
			r.tag, r.tagLine = it, x.lineAt(it.off)
		}
	case "workspace":
		r.workspace = it.kind == 'v' && it.str == "true"
	case "registry", "registry-index":
		r.registry = true
	}
}

func (r *record) dep(file string) dep.Dep {
	name := r.key
	if r.pkg != "" {
		name = r.pkg
	}
	d := dep.Dep{
		Manager: Name, Datasource: Datasource, Versioning: "semver", DepType: r.depType,
		File: file, Line: r.line, Name: name,
	}
	switch {
	case r.workspace:
		d.Skip = "workspace dependency"
	case r.path:
		d.Skip = "path dependency"
	case r.git != "":
		r.gitDep(&d)
	case r.registry:
		d.Skip = "alternative registry"
	case r.version == nil:
		d.Skip = "no version"
	default:
		d.Version = r.version.str
		m := reqRe.FindStringSubmatchIndex(r.version.str)
		switch {
		case m == nil:
			d.Skip = "unsupported requirement"
		case r.version.span == (dep.Span{}):
			d.Skip = "cannot locate value"
		default:
			d.Version = r.version.str[m[4]:m[5]]
			start := r.version.span.Start + m[4]
			d.RefSpan = dep.Span{Start: start, End: start + len(d.Version)}
			if strings.Count(d.Version, ".") < 2 {
				d.Versioning = "tag" // keeps the precision: "1" -> "2", "0.4" -> "0.5"
			}
		}
	}
	return d
}

// gitDep makes a `git` dependency with a `tag` on an http(s) repository a git-tags dep; any
// other git dependency (branch, rev, default branch, ssh URL) is skipped.
func (r *record) gitDep(d *dep.Dep) {
	u, err := url.Parse(r.git)
	switch {
	case r.tag == nil || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		d.Skip = "git dependency"
		return
	case r.tag.span == (dep.Span{}):
		d.Skip = "cannot locate value"
		return
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	d.Datasource, d.Versioning = "git-tags", "tag"
	d.Source = strings.TrimSuffix(strings.TrimSuffix(u.String(), "/"), ".git")
	d.Version, d.RefSpan = r.tag.str, r.tag.span
	d.Line = r.tagLine
}
