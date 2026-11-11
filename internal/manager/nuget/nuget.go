// Package nuget extracts NuGet package references, MSBuild SDKs and .NET tools from MSBuild
// files (`*.csproj`, `*.fsproj`, `*.vbproj`, `*.props`, `*.targets`, including the central
// `Directory.Packages.props`), `global.json` (the .NET SDK and `msbuild-sdks`) and
// `dotnet-tools.json`, resolves `$(Property)` versions through the file and the
// Directory.Packages.props/Directory.Build.props above it, and looks packages up on the
// package sources of the nuget.config files above each file.
package nuget

import (
	"fmt"
	"path"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name.
const Name = "nuget"

// Dep types (Renovate's): NuGet packages and tools, MSBuild project SDKs, the .NET SDK of
// global.json.
const (
	DepTypeNuGet      = "nuget"
	DepTypeMSBuildSDK = "msbuild-sdk"
	DepTypeDotnetSDK  = "dotnet-sdk"
)

// Datasource and versioning names.
const (
	dsNuGet  = "nuget"
	dsDotnet = "dotnet-version"
	scheme   = "nuget"
)

// Skip reasons.
const (
	skipNoVersion  = "no version"
	skipInvalid    = "invalid version"
	skipLocate     = "cannot locate value"
	skipFloatAll   = "floating version"
	skipNonLiteral = "non-literal version"
	skipUndefined  = "undefined property $("
	skipCycle      = "property cycle $("
	skipID         = "non-literal package id"
	skipNoSources  = "no http(s) package source in nuget.config"
	skipUnmapped   = "no packageSourceMapping pattern matches the package"
)

// maxPropertyDepth bounds `$(A)` → `$(B)` → … chains.
const maxPropertyDepth = 8

type nugetManager struct {
	rangeEditor
	configs  configSet
	mu       sync.Mutex
	projects map[string]*project // MSBuild files of the run by path
	refs     map[string][]ref    // per MSBuild file: deps whose property it does not define
}

// ref is a dep of a file (index into its deps) whose version is a property defined elsewhere.
type ref struct {
	i  int
	it item
}

// New returns the nuget manager.
func New() manager.Manager { return &nugetManager{} }

func (*nugetManager) Name() string { return Name }

// Match reports whether the slash path is an MSBuild project or import (`.csproj`, `.fsproj`,
// `.vbproj`, `.props`, `.targets`, but not the `*.nuget.g.props` restore writes),
// `global.json`, `dotnet-tools.json`, a `nuget.config` or a `packages.config` (any case).
func (*nugetManager) Match(p string) bool {
	base := strings.ToLower(path.Base(p))
	switch path.Ext(base) {
	case ".csproj", ".fsproj", ".vbproj":
		return true
	case ".props", ".targets":
		return !strings.HasSuffix(base, ".nuget.g.props") && !strings.HasSuffix(base, ".nuget.g.targets")
	case ".json":
		return base == "global.json" || base == "dotnet-tools.json"
	case ".config":
		return base == "nuget.config" || base == packagesConfig
	}
	return false
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (*nugetManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".csproj", ".fsproj", ".vbproj", ".props", ".targets", ".json", ".config"}}
}

// Extract returns one dep per versioned package item (PackageReference, PackageVersion,
// GlobalPackageReference, PackageDownload, DotNetCliToolReference; `Include` or `Update`,
// `VersionOverride` over `Version`, attribute or child element), MSBuild SDK (`<Project
// Sdk="Name/Version">`, `<Sdk>`, `<Import Sdk>`), global.json SDK and msbuild-sdks entry,
// dotnet-tools.json tool and packages.config `<package>`. A `$(Name)` version is resolved
// through the file's own properties; Link resolves the rest. A nuget.config yields no deps:
// Link applies its sources.
func (m *nugetManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	var items []item
	var p *project
	var err error
	switch base := strings.ToLower(path.Base(file)); {
	case isConfig(file):
		return nil, m.configs.add(file, src)
	case base == "global.json":
		items, err = globalJSON(src)
	case base == "dotnet-tools.json":
		items, err = dotnetTools(src)
	case base == packagesConfig:
		items, err = parsePackagesConfig(src)
	default:
		p, err = parseProject(src)
		if p != nil {
			items = p.items
		}
	}
	if err != nil {
		return nil, err
	}
	var deps []dep.Dep
	var refs []ref
	own := func(name string) (value, string, bool) {
		if p == nil {
			return value{}, "", false
		}
		v, ok := p.props[strings.ToLower(name)]
		return v, file, ok
	}
	for _, it := range items {
		if _, ok := it.versionValue(); !ok {
			continue // a centrally managed PackageReference, or an Update without version
		}
		d := dep.Dep{Manager: Name, Datasource: dsNuGet, Versioning: scheme, DepType: it.depType, File: file, Line: it.name.line, Name: it.name.text}
		if it.depType == DepTypeDotnetSDK {
			d.Datasource = dsDotnet
		}
		resolve(&d, file, it, own)
		if strings.HasPrefix(d.Skip, skipUndefined) {
			refs = append(refs, ref{i: len(deps), it: it})
		}
		deps = append(deps, d)
	}
	if p != nil {
		m.mu.Lock()
		if m.projects == nil {
			m.projects, m.refs = map[string]*project{}, map[string][]ref{}
		}
		m.projects[file] = p
		if refs != nil {
			m.refs[file] = refs
		}
		m.mu.Unlock()
	}
	return deps, nil
}

// resolve sets the version of d, declared by it in file, following `$(Name)` references
// through lookup. A version from a property of another file moves the dep (File, Line) to
// the definition, which its update edits.
func resolve(d *dep.Dep, file string, it item, lookup func(string) (value, string, bool)) {
	id := strings.TrimSpace(d.Name)
	if id == "" || strings.ContainsAny(id, "$;@%") {
		d.Skip = skipID
		return
	}
	d.Name = id
	v, _ := it.versionValue()
	owner, ref := file, ""
	for depth := 0; ; depth++ {
		name, ok := propertyRef(v.text)
		if !ok {
			break
		}
		if depth == maxPropertyDepth {
			d.Skip, d.Version = skipCycle+name+")", v.text
			return
		}
		def, f, found := lookup(name)
		if !found {
			d.Skip, d.Version = skipUndefined+name+")", v.text
			return
		}
		v, owner, ref = def, f, name
	}
	if strings.Contains(v.text, "$(") {
		d.Skip, d.Version = skipNonLiteral+" "+v.text, v.text
		return
	}
	setVersion(d, v.text, v.span)
	d.VersionRef = ref
	if owner != file {
		d.File, d.Line = owner, v.line
	}
}

// propertyRef returns Name when s is exactly a property reference `$(Name)`.
func propertyRef(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "$(") || !strings.HasSuffix(s, ")") {
		return "", false
	}
	name := s[2 : len(s)-1]
	if name == "" || strings.ContainsAny(name, "$()") {
		return "", false
	}
	return strings.TrimSpace(name), true
}

// importNames are the files MSBuild imports into every project below their directory, in the
// order a property lookup tries them: Directory.Packages.props is imported after
// Directory.Build.props, so its definitions win.
var importNames = []string{"Directory.Packages.props", "Directory.Build.props"}

// Link implements manager.Linker: versions whose `$(Name)` property a file does not define are
// looked up in the Directory.Packages.props and Directory.Build.props of its directory and
// every directory above it (nearest first), and each nuget-datasource dep gets the package
// sources of the nuget.config files above its file (sourcesFor), restricted by
// packageSourceMapping. Then it forgets the run's files.
func (m *nugetManager) Link(files [][]dep.Dep) {
	configs := m.configs.take()
	m.mu.Lock()
	projects, refs := m.projects, m.refs
	m.projects, m.refs = nil, nil
	m.mu.Unlock()
	for _, deps := range files {
		if len(deps) == 0 {
			continue
		}
		file := deps[0].File // the declaring file: only Link moves deps to another one
		for _, r := range refs[file] {
			d := &deps[r.i]
			*d = dep.Dep{Manager: d.Manager, Datasource: d.Datasource, Versioning: d.Versioning, DepType: d.DepType, File: file, Line: d.Line, Name: d.Name}
			resolve(d, file, r.it, func(name string) (value, string, bool) {
				for _, f := range importChain(projects, file) {
					if v, ok := projects[f].props[strings.ToLower(name)]; ok {
						return v, f, true
					}
				}
				return value{}, "", false
			})
		}
		applySources(configs, path.Dir(file), deps)
	}
}

// importChain returns file and the MSBuild files of the run it imports implicitly, in lookup
// order (importNames per directory, nearest directory first).
func importChain(projects map[string]*project, file string) []string {
	out := []string{file}
	for d := path.Dir(file); ; d = path.Dir(d) {
		for _, n := range importNames {
			if p := path.Join(d, n); p != file && projects[p] != nil {
				out = append(out, p)
			}
		}
		if d == "." || d == "/" {
			return out
		}
	}
}

// rangeEditor implements Edits and manager.RangeUpdater for the nuget-versioned deps of the
// nuget and cake managers.
type rangeEditor struct{}

// Edits replaces the version (RefSpan) with the new version, or rewrites a range for it
// (NewRange) under the update's rangeStrategy.
func (e rangeEditor) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	out, ok := e.NewRange(d, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil, fmt.Errorf("range %s cannot be rewritten for %s", d.Range, u.NewVersion)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: out}}, nil
}
