// Package cargo extracts crate dependencies from Cargo.toml manifests, reads the versions
// Cargo.lock locks them to, and updates Cargo.lock after an update (see Locker).
package cargo

import (
	"cmp"
	"fmt"
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
	add := func(depType string, t *tomlspan.Value) {
		if t == nil || t.Kind != tomlspan.Table {
			return
		}
		for _, k := range t.Keys {
			deps = append(deps, entryDep(file, depType, k, t.Fields[k]))
		}
	}
	for _, k := range root.Keys {
		v := root.Fields[k]
		switch {
		case sections[k] != "":
			add(sections[k], v)
		case k == "target" && v.Kind == tomlspan.Table:
			for _, spec := range v.Keys {
				t := v.Fields[spec]
				if t.Kind != tomlspan.Table {
					continue
				}
				for _, s := range t.Keys {
					if sections[s] != "" {
						add(sections[s], t.Fields[s])
					}
				}
			}
		case k == "workspace":
			add(DepTypeWorkspace, root.Get("workspace", "dependencies"))
		case k == "patch" && v.Kind == tomlspan.Table:
			for _, source := range v.Keys {
				add(DepTypePatch, v.Fields[source])
			}
		case k == "replace":
			add(DepTypeReplace, v)
		}
	}
	slices.SortStableFunc(deps, func(a, b dep.Dep) int { return cmp.Compare(a.Line, b.Line) })
	return deps, nil
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
	var tag *tomlspan.Value
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
		tag = str("tag")
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
		gitDep(&d, gitURL, tag)
	case ver == nil:
		d.Skip = "no version"
	case !ver.Exact:
		d.Skip = "cannot locate value"
	default:
		req, ok := parseRequirement(ver.Str)
		if !ok {
			d.Skip = "unsupported requirement"
			break
		}
		d.Range, d.Version = ver.Str, req.version()
		if c := req.constraint(); c != ver.Str {
			d.Constraint = c
		}
		d.RefSpan = dep.Span{Start: ver.Start, End: ver.End}
	}
	return d
}

// gitDep makes a `git` dependency with a `tag` on an http(s) repository a git-tags dep; any
// other git dependency (branch, rev, default branch, ssh URL) is skipped.
func gitDep(d *dep.Dep, raw string, tag *tomlspan.Value) {
	d.Source = ""
	u, err := url.Parse(raw)
	switch {
	case tag == nil || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		d.Skip = "git dependency"
		return
	case !tag.Exact:
		d.Skip = "cannot locate value"
		return
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	d.Datasource, d.Versioning = "git-tags", "tag"
	d.Source = strings.TrimSuffix(strings.TrimSuffix(u.String(), "/"), ".git")
	d.Version, d.RefSpan, d.Line = tag.Str, dep.Span{Start: tag.Start, End: tag.End}, tag.Line
}

// Edits replaces the git tag, or rewrites the requirement for the new version under the
// update's rangeStrategy (rewrite): the operators, wildcards and precision are kept.
func (cargoManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
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
