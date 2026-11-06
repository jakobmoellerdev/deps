// Package julia extracts the [compat] entries of Julia projects (Project.toml,
// JuliaProject.toml), reads the versions their Manifest.toml locks the packages to, and
// updates the manifest after an update with Pkg (see Locker).
package julia

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name (Dependabot's ecosystem name; Renovate has no Julia manager).
const Name = "julia"

// Datasource is the datasource of packages.
const Datasource = "julia-registry"

// Versioning is the versioning scheme of packages.
const Versioning = "julia"

// DepTypes: the project table naming the package.
const (
	DepTypeDeps     = "deps"     // [deps]
	DepTypeWeakdeps = "weakdeps" // [weakdeps], the triggers of package extensions
	DepTypeExtras   = "extras"   // [extras], test-only (`[targets]`) packages
	DepTypeJulia    = "julia"    // the `julia` compat entry
)

// Skip reasons.
const (
	skipJulia       = "julia compat is the project's supported Julia versions"
	skipStdlib      = "julia standard library"
	skipNotDep      = "not in [deps], [weakdeps] or [extras]"
	skipNonString   = "compat entry is not a string"
	skipEscaped     = "compat entry with escapes"
	skipUnsupported = "unsupported compat entry"
	skipPath        = "path source"
	skipURL         = "url source"
	skipNoLock      = "no Manifest.toml version for a package without a lower bound"
)

// anyVersion is the range of a [deps] package without a compat entry: Pkg allows any version.
const anyVersion = ">= 0"

// depTables are the project tables naming packages (name → UUID), in precedence order: a
// package in several (an extension trigger also in [extras]) takes the first one's DepType.
var depTables = []string{DepTypeDeps, DepTypeWeakdeps, DepTypeExtras}

// ProjectNames are the project file names Pkg reads, in its order of preference.
var ProjectNames = []string{"JuliaProject.toml", "Project.toml"}

type juliaManager struct{}

// New returns the julia manager.
func New() manager.Manager { return juliaManager{} }

func (juliaManager) Name() string { return Name }

// Match reports whether the slash path is a Project.toml or JuliaProject.toml.
func (juliaManager) Match(p string) bool {
	return strings.HasSuffix(p, "Project.toml") && slices.Contains(ProjectNames, p[strings.LastIndexByte(p, '/')+1:])
}

// pkgRef is a package a project names: its UUID, DepType and line.
type pkgRef struct {
	uuid, depType string
	line          int
}

// packages returns the packages of the project tables, by name.
func packages(root *tomlspan.Value) map[string]pkgRef {
	out := map[string]pkgRef{}
	for _, table := range depTables {
		t := root.Get(table)
		if t == nil || t.Kind != tomlspan.Table {
			continue
		}
		for _, name := range t.Keys {
			if _, seen := out[name]; seen {
				continue
			}
			v := t.Fields[name]
			if v.Kind == tomlspan.String {
				out[name] = pkgRef{uuid: strings.ToLower(v.Str), depType: table, line: v.Line}
			}
		}
	}
	return out
}

// sourceSkip is the skip reason of a package [sources] installs from a path or a git URL,
// which the registry does not serve.
func sourceSkip(root *tomlspan.Value, name string) string {
	s := root.Get("sources", name)
	switch {
	case s == nil || s.Kind != tomlspan.Table:
		return ""
	case s.Fields["path"] != nil:
		return skipPath
	case s.Fields["url"] != nil:
		return skipURL
	}
	return ""
}

// Extract returns one Dep per [compat] entry, and one per [deps] package without a compat
// entry (Range `>= 0`, never rewritten: only the manifest moves it), ordered by line. Range
// is the entry as written (`0.21, 1`; RefSpan its bytes), Version the highest lower bound of
// its terms as written (`1`, also for skipped entries), "" when no term has one (`< 2`; ReadLocks takes the locked
// version then). Source is the package UUID. The `julia` entry, standard libraries and
// packages of [sources] are skipped.
func (juliaManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	root, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	pkgs := packages(root)
	var deps []dep.Dep
	compat := root.Get("compat")
	if compat != nil && compat.Kind == tomlspan.Table {
		for _, name := range compat.Keys {
			deps = append(deps, compatDep(file, root, pkgs, name, compat.Fields[name]))
		}
	}
	if t := root.Get(DepTypeDeps); t != nil && t.Kind == tomlspan.Table {
		for _, name := range t.Keys {
			ref, ok := pkgs[name]
			if !ok || ref.depType != DepTypeDeps || stdlibs[ref.uuid] || compat != nil && compat.Fields[name] != nil {
				continue
			}
			d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepTypeDeps, File: file, Line: ref.line, Name: name, Source: ref.uuid,
				Range: anyVersion, RangeFixed: true, Skip: sourceSkip(root, name)}
			deps = append(deps, d)
		}
	}
	slices.SortStableFunc(deps, func(a, b dep.Dep) int { return cmp.Compare(a.Line, b.Line) })
	return deps, nil
}

// compatDep is the dep of the compat entry name = v.
func compatDep(file string, root *tomlspan.Value, pkgs map[string]pkgRef, name string, v *tomlspan.Value) dep.Dep {
	d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, File: file, Line: v.Line, Name: name}
	ref, known := pkgs[name]
	d.DepType, d.Source = ref.depType, ref.uuid
	switch {
	case name == "julia":
		d.DepType, d.Skip = DepTypeJulia, skipJulia
	case !known:
		d.Skip = skipNotDep
	case stdlibs[ref.uuid]:
		d.Skip = skipStdlib
	case v.Kind != tomlspan.String:
		d.Skip = skipNonString
	case !v.Exact:
		d.Skip = skipEscaped
	default:
		d.Skip = sourceSkip(root, name)
	}
	if v.Kind != tomlspan.String {
		return d
	}
	d.Range = v.Str
	terms, err := versioning.ParseJuliaCompat(v.Str)
	if err != nil {
		d.Version = v.Str
		d.Skip = cmp.Or(d.Skip, skipUnsupported)
		return d
	}
	if d.Skip == "" {
		d.RefSpan = dep.Span{Start: v.Start, End: v.End}
	}
	var floor versioning.JuliaBound
	for _, t := range terms {
		if t.Floor() && (floor.N == 0 || versioning.JuliaCompareParts(t.Lo.Parts, floor.Parts) > 0) {
			floor = t.Lo
		}
	}
	if floor.N > 0 {
		d.Version = floor.String()
	}
	return d
}

// Edits replaces the compat entry with the one NewRange writes for the new version.
func (m juliaManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.RangeFixed || d.RefSpan.End == 0 {
		return nil, fmt.Errorf("%s has no compat entry to rewrite", d.Name)
	}
	out, ok := m.NewRange(d, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("compat entry %q cannot be updated to admit %s", d.Range, u.NewVersion)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: out}}, nil
}

// NewRange implements manager.RangeRewriter (rewrite).
func (juliaManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	if d.RangeFixed {
		return "", false
	}
	return rewrite(d.Range, v, strategy)
}
