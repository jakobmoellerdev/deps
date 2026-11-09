// Package pep621 extracts the PEP 621 dependencies of pyproject.toml files (`[project]`
// dependencies and optional-dependencies), PEP 735 `[dependency-groups]`, uv's
// `[tool.uv] dev-dependencies` and pdm's `[tool.pdm.dev-dependencies]`, honoring
// `[tool.uv.sources]`, `[[tool.uv.index]]` and `[[tool.pdm.source]]`.
package pep621

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pytool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Name is the manager name (Renovate's).
const Name = "pep621"

// DepTypes.
const (
	DepTypeDependencies      = "project.dependencies"
	DepTypeOptional          = "project.optional-dependencies"
	DepTypeGroups            = "dependency-groups"
	DepTypeUVDev             = "tool.uv.dev-dependencies"
	DepTypePDMDev            = "tool.pdm.dev-dependencies"
	DepTypeBuildSystem       = "build-system.requires"
	DepTypeUVConstraint      = "tool.uv.constraint-dependencies"
	DepTypeUVOverride        = "tool.uv.override-dependencies"
	DepTypeUVBuildConstraint = "tool.uv.build-constraint-dependencies"
	SkipWorkspace            = "workspace member"
	SkipMultipleSources      = "multiple sources"
	SkipUnknownIndex         = "unknown index"
)

var matchRe = regexp.MustCompile(`(^|/)pyproject\.toml$`)

// pep621Manager rewrites specifiers with pyreq.Ranges and records the project names of its
// files for Link.
type pep621Manager struct {
	pyreq.Ranges
	projects pyreq.Projects
}

// New returns the pep621 manager.
func New() manager.Manager { return &pep621Manager{} }

func (*pep621Manager) Name() string { return Name }

// Match reports whether the slash path is a pyproject.toml file. The suffix check skips the
// regexp for the paths that cannot match.
func (*pep621Manager) Match(p string) bool {
	return strings.HasSuffix(p, "pyproject.toml") && matchRe.MatchString(p)
}

// ReadLocks records the versions uv.lock, poetry.lock or pdm.lock locks the deps to
// (pytool.ReadLocks).
func (*pep621Manager) ReadLocks(root string, files [][]dep.Dep) []error {
	return pytool.ReadLocks(root, files)
}

// LockRoots implements manager.LockMaintainer: the uv.lock or pdm.lock of each file's lock
// directory (pytool.LockRoots; a poetry.lock is the poetry manager's).
func (*pep621Manager) LockRoots(root string, files []string) []manager.LockRoot {
	return pytool.LockRoots(root, files, pytool.UVLock, pytool.PDMLock)
}

// Link implements manager.Linker: a dep named like the project of a scanned pyproject.toml
// gets its DefinedIn (pyreq.Projects).
func (m *pep621Manager) Link(files [][]dep.Dep) { m.projects.Link(files) }

// Extract returns one dep per PEP 508 requirement string of the dependency arrays (also
// `[build-system] requires` and uv's constraint, override and build constraint lists) and
// records the project's name for Link. `include-group` entries of dependency groups are
// references to another group, whose deps are extracted there; other non-string items are
// ignored.
func (m *pep621Manager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	doc, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	m.projects.Add(file, doc)
	x := extractor{file: file, uv: newUVSources(doc), pdm: newPDMSources(doc)}
	if rp := doc.Get("project", "requires-python"); rp != nil && rp.Kind == tomlspan.String {
		x.python = rp.Str
	}
	x.list(DepTypeDependencies, doc.Get("project", "dependencies"))
	x.tables(DepTypeOptional, doc.Get("project", "optional-dependencies"))
	x.tables(DepTypeGroups, doc.Get("dependency-groups"))
	x.list(DepTypeUVDev, doc.Get("tool", "uv", "dev-dependencies"))
	x.tables(DepTypePDMDev, doc.Get("tool", "pdm", "dev-dependencies"))
	x.list(DepTypeBuildSystem, doc.Get("build-system", "requires"))
	x.list(DepTypeUVConstraint, doc.Get("tool", "uv", "constraint-dependencies"))
	x.list(DepTypeUVOverride, doc.Get("tool", "uv", "override-dependencies"))
	x.list(DepTypeUVBuildConstraint, doc.Get("tool", "uv", "build-constraint-dependencies"))
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
// indexes) or skips it when the source is not an index. A git source with a version `tag`
// makes d (also an unconstrained one) a git-tags dep.
func (s uvSources) apply(d *dep.Dep) {
	if d.Datasource == pyreq.GitTags || d.Skip != "" && d.Skip != pyreq.SkipUnconstrained {
		return
	}
	src := s.sources[pyreq.Normalize(d.Name)]
	if src != nil && src.Kind == tomlspan.Table && pyreq.GitTable(d, src, "tag") {
		return
	}
	if d.Skip != "" {
		return
	}
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
		d.Skip = skip // path or url
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
