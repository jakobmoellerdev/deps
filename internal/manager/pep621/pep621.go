// Package pep621 extracts the PEP 621 dependencies of pyproject.toml files (`[project]`
// dependencies and optional-dependencies), PEP 735 `[dependency-groups]`, uv's
// `[tool.uv] dev-dependencies` and pdm's `[tool.pdm.dev-dependencies]`, honoring
// `[tool.uv.sources]`, `[[tool.uv.index]]` and `[[tool.pdm.source]]`.
package pep621

import (
	"regexp"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Name is the manager name (Renovate's).
const Name = "pep621"

// DepTypes.
const (
	DepTypeDependencies = "project.dependencies"
	DepTypeOptional     = "project.optional-dependencies"
	DepTypeGroups       = "dependency-groups"
	DepTypeUVDev        = "tool.uv.dev-dependencies"
	DepTypePDMDev       = "tool.pdm.dev-dependencies"
	SkipWorkspace       = "workspace member"
	SkipMultipleSources = "multiple sources"
	SkipUnknownIndex    = "unknown index"
)

var matchRe = regexp.MustCompile(`(^|/)pyproject\.toml$`)

type pep621Manager struct{}

// New returns the pep621 manager.
func New() manager.Manager { return pep621Manager{} }

func (pep621Manager) Name() string { return Name }

// Match reports whether the slash path is a pyproject.toml file.
func (pep621Manager) Match(p string) bool { return matchRe.MatchString(p) }

// Edits rewrites the range (pyreq.Edits).
func (pep621Manager) Edits(u dep.Update) ([]patch.Edit, error) { return pyreq.Edits(u) }

// InRange implements manager.RangeUpdater.
func (pep621Manager) InRange(d dep.Dep, v string) bool { return pyreq.InRange(d, v) }

// NewRange implements manager.RangeUpdater.
func (pep621Manager) NewRange(d dep.Dep, v string) (string, bool) { return pyreq.NewRange(d, v) }

// ReadLocks records the versions uv.lock, poetry.lock or pdm.lock locks the deps to
// (pytool.ReadLocks).
func (pep621Manager) ReadLocks(root string, files [][]dep.Dep) []error {
	return pytool.ReadLocks(root, files)
}

// Extract returns one dep per PEP 508 requirement string of the dependency arrays.
// `include-group` entries of dependency groups and non-string items are ignored.
func (pep621Manager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	doc, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	x := extractor{file: file, uv: newUVSources(doc), pdm: newPDMSources(doc)}
	if rp := doc.Get("project", "requires-python"); rp != nil && rp.Kind == tomlspan.String {
		x.python = rp.Str
	}
	x.list(DepTypeDependencies, doc.Get("project", "dependencies"))
	x.tables(DepTypeOptional, doc.Get("project", "optional-dependencies"))
	x.tables(DepTypeGroups, doc.Get("dependency-groups"))
	x.list(DepTypeUVDev, doc.Get("tool", "uv", "dev-dependencies"))
	x.tables(DepTypePDMDev, doc.Get("tool", "pdm", "dev-dependencies"))
	return x.deps, nil
}

type extractor struct {
	file   string
	uv     uvSources
	pdm    pdmSources
	python string // [project] requires-python
	deps   []dep.Dep
}

// tables extracts every array of the table t (optional-dependencies, dependency-groups,
// pdm dev-dependencies).
func (x *extractor) tables(depType string, t *tomlspan.Value) {
	if t == nil || t.Kind != tomlspan.Table {
		return
	}
	for _, k := range t.Keys {
		x.list(depType, t.Fields[k])
	}
}

func (x *extractor) list(depType string, a *tomlspan.Value) {
	for _, v := range a.Strings() {
		d, ok := pyreq.TOMLRequirement(Name, depType, x.file, v)
		if !ok {
			continue
		}
		d.PythonConstraint = x.python
		x.uv.apply(&d)
		x.pdm.apply(&d)
		x.deps = append(x.deps, d)
	}
}

// uvSources are the `[tool.uv.sources]` entries by normalized name and the index URLs of
// `[[tool.uv.index]]` by name; extra are those neither default nor `explicit`, which uv
// searches for every package not pinned to an index.
type uvSources struct {
	sources      map[string]*tomlspan.Value
	indexes      map[string]string
	defaultIndex string
	extra        []string
}

func newUVSources(doc *tomlspan.Value) uvSources {
	s := uvSources{sources: map[string]*tomlspan.Value{}, indexes: map[string]string{}}
	if t := doc.Get("tool", "uv", "sources"); t != nil && t.Kind == tomlspan.Table {
		for _, k := range t.Keys {
			s.sources[pyreq.Normalize(k)] = t.Fields[k]
		}
	}
	if a := doc.Get("tool", "uv", "index"); a != nil && a.Kind == tomlspan.Array {
		for _, idx := range a.Items {
			u := idx.Get("url")
			if u == nil || u.Kind != tomlspan.String {
				continue
			}
			if n := idx.Get("name"); n != nil && n.Kind == tomlspan.String {
				s.indexes[n.Str] = u.Str
			}
			def, explicit := idx.Get("default"), idx.Get("explicit")
			switch {
			case def != nil && def.Str == "true":
				if s.defaultIndex == "" {
					s.defaultIndex = u.Str
				}
			case explicit == nil || explicit.Str != "true":
				s.extra = append(s.extra, u.Str)
			}
		}
	}
	return s
}

// apply sets d's Source (and ExtraSources) from its uv source (or the default and extra
// indexes) or skips it when the source is not an index.
func (s uvSources) apply(d *dep.Dep) {
	if d.Skip != "" {
		return
	}
	src := s.sources[pyreq.Normalize(d.Name)]
	switch {
	case src == nil:
		d.Source, d.ExtraSources = s.defaultIndex, s.extra
		return
	case src.Kind == tomlspan.Array:
		d.Skip = SkipMultipleSources
		return
	case src.Kind != tomlspan.Table:
		return
	}
	if w := src.Get("workspace"); w != nil && w.Str == "true" {
		d.Skip = SkipWorkspace
		return
	}
	if _, skip := pyreq.TableSpec(src); skip != pyreq.SkipUnconstrained {
		d.Skip = skip // git, path or url
		return
	}
	idx := src.Get("index")
	if idx == nil || idx.Kind != tomlspan.String {
		d.Source, d.ExtraSources = s.defaultIndex, s.extra
		return
	}
	u, ok := s.indexes[idx.Str]
	if !ok {
		d.Skip = SkipUnknownIndex + " " + idx.Str
		return
	}
	d.Source = u
}
