// Package poetry extracts the Poetry dependencies of pyproject.toml files:
// `[tool.poetry.dependencies]`, `[tool.poetry.dev-dependencies]` and
// `[tool.poetry.group.<name>.dependencies]`, resolving `source` against
// `[[tool.poetry.source]]`.
package poetry

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Name is the manager name (Renovate's).
const Name = "poetry"

// DepTypes and skip reasons.
const (
	DepTypeDependencies    = "dependencies"
	DepTypeDevDependencies = "dev-dependencies"
	DepTypeGroup           = "group"
	SkipMultiple           = "multiple constraints"
	SkipUnknownSource      = "unknown source"
)

var matchRe = regexp.MustCompile(`(^|/)pyproject\.toml$`)

type poetryManager struct{}

// New returns the poetry manager.
func New() manager.Manager { return poetryManager{} }

func (poetryManager) Name() string { return Name }

// Match reports whether the slash path is a pyproject.toml file. The suffix check skips the
// regexp for the paths that cannot match.
func (poetryManager) Match(p string) bool {
	return strings.HasSuffix(p, "pyproject.toml") && matchRe.MatchString(p)
}

// Edits rewrites the constraint (pyreq.Edits).
func (poetryManager) Edits(u dep.Update) ([]patch.Edit, error) { return pyreq.Edits(u) }

// InRange implements manager.RangeUpdater.
func (poetryManager) InRange(d dep.Dep, v string) bool { return pyreq.InRange(d, v) }

// NewRange implements manager.RangeUpdater.
func (poetryManager) NewRange(d dep.Dep, v string) (string, bool) { return pyreq.NewRange(d, v) }

// ReadLocks records the versions poetry.lock locks the deps to (pytool.ReadLocks).
func (poetryManager) ReadLocks(root string, files [][]dep.Dep) []error {
	return pytool.ReadLocks(root, files)
}

// LockRoots implements manager.LockMaintainer: the poetry.lock of each file's lock directory
// (pytool.LockRoots), also of Poetry 2 projects with only `[project]` dependencies.
func (poetryManager) LockRoots(root string, files []string) []manager.LockRoot {
	return pytool.LockRoots(root, files, pytool.PoetryLock)
}

// Extract returns one dep per entry of the dependency tables; the `python` constraint is not
// a dependency and is not extracted.
func (poetryManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	doc, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	p := doc.Get("tool", "poetry")
	if p == nil {
		return nil, nil
	}
	x := extractor{file: file, sources: map[string]string{}}
	for _, py := range []*tomlspan.Value{p.Get("dependencies", "python"), doc.Get("project", "requires-python")} {
		if py != nil && py.Kind == tomlspan.String && x.python == "" {
			x.python = py.Str
		}
	}
	x.readSources(p.Get("source"))
	x.table(DepTypeDependencies, p.Get("dependencies"))
	x.table(DepTypeDevDependencies, p.Get("dev-dependencies"))
	if g := p.Get("group"); g != nil && g.Kind == tomlspan.Table {
		for _, name := range g.Keys {
			x.table(DepTypeGroup, g.Fields[name].Get("dependencies"))
		}
	}
	return x.deps, nil
}

type extractor struct {
	file string
	// sources maps [[tool.poetry.source]] names to URLs; primary is the URL deps without
	// `source` resolve against ("" = PyPI), supplemental the sources searched after it.
	sources      map[string]string
	primary      string
	supplemental []string
	python       string // `python` of [tool.poetry.dependencies], else [project] requires-python
	deps         []dep.Dep
}

// readSources reads [[tool.poetry.source]]. A source with `priority = "primary"` or
// `"default"` (or the legacy `default = true`) replaces PyPI for deps without `source`;
// `priority = "supplemental"` sources are searched too.
func (x *extractor) readSources(a *tomlspan.Value) {
	if a == nil || a.Kind != tomlspan.Array {
		return
	}
	for _, s := range a.Items {
		name, u := s.Get("name"), s.Get("url")
		if name == nil || u == nil || name.Kind != tomlspan.String || u.Kind != tomlspan.String {
			continue
		}
		x.sources[name.Str] = u.Str
		prio, def := s.Get("priority"), s.Get("default")
		primary := prio != nil && (prio.Str == "primary" || prio.Str == "default") || def != nil && def.Str == "true"
		if primary && x.primary == "" {
			x.primary = u.Str
		} else if prio != nil && prio.Str == "supplemental" {
			x.supplemental = append(x.supplemental, u.Str)
		}
	}
}

func (x *extractor) table(depType string, t *tomlspan.Value) {
	if t == nil || t.Kind != tomlspan.Table {
		return
	}
	for _, name := range t.Keys {
		if name == "python" {
			continue
		}
		v := t.Fields[name]
		d := pyreq.NewDep(Name, depType, x.file, name, v.Line)
		d.Source, d.ExtraSources, d.PythonConstraint = x.primary, x.supplemental, x.python
		switch v.Kind {
		case tomlspan.String:
			pyreq.SetVersion(&d, v.Str, v.Start, v.Exact, pyreq.Poetry)
		case tomlspan.Table:
			spec, skip := pyreq.TableSpec(v)
			if skip != "" {
				d.Skip = skip
				break
			}
			d.Line = spec.Line
			pyreq.SetVersion(&d, spec.Str, spec.Start, spec.Exact, pyreq.Poetry)
			x.source(&d, v.Get("source"))
		case tomlspan.Array:
			d.Skip = SkipMultiple
		default:
			d.Skip = pyreq.SkipRange
		}
		x.deps = append(x.deps, d)
	}
}

// source points d at its explicit `source`.
func (x *extractor) source(d *dep.Dep, s *tomlspan.Value) {
	if s == nil || s.Kind != tomlspan.String || d.Skip != "" {
		return
	}
	u, ok := x.sources[s.Str]
	if !ok {
		d.Skip = SkipUnknownSource + " " + s.Str
		return
	}
	d.Source, d.ExtraSources = u, nil
}
