// Package ant extracts the Maven dependencies of Apache Ant build files (`build.xml`) that use
// the Maven Ant Tasks or the Maven Resolver Ant Tasks: `<dependency>` elements with
// groupId/artifactId/version attributes or `coords`, versions from Ant properties (`<property
// name value>`, `<property file>` properties files) also across `<import>`ed build files, and
// the repositories of `<remoteRepository>` elements and `settingsFile` Maven settings.
package ant

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name (Renovate's).
const Name = "ant"

// Dep types: the Maven scope of a dependency (`scope` attribute or the scope part of
// `coords`); compile when absent or not one of these.
const (
	DepTypeCompile  = "compile"
	DepTypeRuntime  = "runtime"
	DepTypeTest     = "test"
	DepTypeProvided = "provided"
	DepTypeSystem   = "system"
)

// Skip reasons.
const (
	skipUnresolved  = "unresolved property "
	skipCycle       = "property cycle "
	skipUnsupported = "unsupported version "
	skipName        = "unresolved property in "
	skipLocate      = "cannot locate value"
	skipRange       = "version range"
)

// scopes are the Maven scopes that are dep types.
var scopes = map[string]bool{DepTypeCompile: true, DepTypeRuntime: true, DepTypeTest: true, DepTypeProvided: true, DepTypeSystem: true}

var propRe = regexp.MustCompile(`\$\{([^}]*)\}`)

type antManager struct{}

// New returns the ant manager.
func New() manager.Manager { return antManager{} }

func (antManager) Name() string { return Name }

// Match reports whether the slash path is a build.xml.
func (antManager) Match(p string) bool { return p == "build.xml" || strings.HasSuffix(p, "/build.xml") }

// Extract returns one dep per versioned `<dependency>` of the build file and the files it
// imports (walk), with `${name}` versions resolved through the properties they define.
func (antManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := parse(src)
	if err != nil {
		return nil, err
	}
	w := newWalker(file, doc, env)
	w.walk(file, doc, nil)
	deps := make([]dep.Dep, 0, len(w.decls))
	for _, dc := range w.decls {
		deps = append(deps, w.resolve(dc))
	}
	return deps, nil
}

// Link implements manager.Linker: a build file imported by several extracted build files (or
// itself a build.xml) yields its deps once, for the first of them in discovery order; so does
// a properties file version that several build files share for the same artifact.
func (antManager) Link(files [][]dep.Dep) {
	type key struct {
		file, name, depType, version string
		line                         int
		span                         dep.Span
	}
	seen := map[key]bool{}
	for i, deps := range files {
		out := deps[:0]
		for _, d := range deps {
			k := key{d.File, d.Name, d.DepType, d.Version, d.Line, d.RefSpan}
			if !seen[k] {
				seen[k] = true
				out = append(out, d)
			}
		}
		files[i] = out
	}
}

// resolve returns the dep of dc: the group and artifact with `${name}` expanded, the version
// followed through `${a}` → `${b}` chains to its definition. A version defined in another
// file moves the dep (File, Line) there.
func (w *walker) resolve(dc decl) dep.Dep {
	d := dep.Dep{
		Manager: Name, Datasource: mavends.Name, Versioning: "maven", DepType: dc.depType,
		File: dc.file, Line: dc.version.line, Name: dc.group.text + ":" + dc.artifact.text,
		Version: dc.version.text, Source: source(dc.repos),
	}
	group, okG := w.expand(dc.group.text)
	artifact, okA := w.expand(dc.artifact.text)
	if !okG || !okA {
		d.Skip = skipName + d.Name
		return d
	}
	d.Name = group + ":" + artifact
	if group == dc.group.text && artifact == dc.artifact.text && dc.group.span != (dep.Span{}) && dc.artifact.span != (dep.Span{}) {
		d.NameParts = []dep.NamePart{
			{Span: dc.group.span, Text: group, Role: dep.NameGroup},
			{Span: dc.artifact.span, Text: artifact, Role: dep.NameArtifact},
		}
	}
	v, owner, ref := dc.version, dc.file, ""
	seen := map[string]bool{}
	for {
		m := propRe.FindStringSubmatch(v.text)
		if m == nil {
			break
		}
		if m[0] != v.text {
			d.Skip, d.Version = skipUnsupported+v.text, v.text
			return d
		}
		if seen[m[1]] {
			d.Skip = skipCycle + m[0]
			return d
		}
		seen[m[1]] = true
		def, ok := w.props[m[1]]
		if !ok {
			d.Skip = skipUnresolved + m[0]
			return d
		}
		v, owner, ref = def.value, def.file, m[1]
	}
	switch {
	case v.span == (dep.Span{}):
		d.Skip, d.Version = skipLocate, v.text
		return d
	case versioning.IsMavenRange(v.text):
		r, err := versioning.ParseMavenRange(v.text)
		if err != nil {
			d.Skip, d.Version = skipRange, v.text
			return d
		}
		setRange(&d, r, v)
	default:
		d.Version, d.RefSpan = v.text, v.span
	}
	d.VersionRef = ref
	if owner != dc.file {
		d.File, d.Line, d.NameParts = owner, v.line, nil
	}
	return d
}

// setRange sets the version of d from the Maven range r written as v: an exact `[1.0]` gets
// Version and a RefSpan on the version alone; any other range becomes the dep's Range, RefSpan
// the whole value, Version its anchor (the first lower bound, else the first upper one).
func setRange(d *dep.Dep, r versioning.MavenRange, v value) {
	if len(r) == 1 && r[0].Exact() {
		d.Version = r[0].Min
		d.RefSpan = dep.Span{Start: v.span.Start + r[0].MinSpan.Start, End: v.span.Start + r[0].MinSpan.End}
		return
	}
	d.Range, d.Version, d.RefSpan = v.text, cmp.Or(r[0].Min, r[0].Max), v.span
}

// source returns the dep Source of repos: `[<id>=]<url>/` each, without repeated ids or URLs,
// followed by Maven Central unless a repository is Central or has the id `central`; "" when
// there are none (Maven Central only).
func source(repos []repo) string {
	var b strings.Builder
	central := false
	seen := make([]repo, 0, len(repos))
	for _, r := range repos {
		if !strings.HasSuffix(r.url, "/") {
			r.url += "/"
		}
		if slices.ContainsFunc(seen, func(s repo) bool { return r.id != "" && s.id == r.id || s.url == r.url }) {
			continue
		}
		seen = append(seen, r)
		central = central || r.url == mavends.Central || r.id == "central"
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if r.id != "" {
			b.WriteString(r.id + "=")
		}
		b.WriteString(r.url)
	}
	if b.Len() > 0 && !central {
		b.WriteString(" central=" + mavends.Central)
	}
	return b.String()
}

// Edits replaces the version (the property value for a `${name}` version), or rewrites a
// range for it (NewRange), and, for a replacement, the group and artifact (manager.NameEdits).
func (antManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	edits := []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}
	if d.Range != "" {
		out, ok := antManager{}.NewRange(d, u.NewVersion, u.RangeStrategy)
		if !ok {
			return nil, fmt.Errorf("range %s cannot be rewritten for %s", d.Range, u.NewVersion)
		}
		edits[0].Old, edits[0].New = d.Range, out
	}
	if u.NewName != "" {
		ne, err := manager.NameEdits(d, u.NewName)
		if err != nil {
			return nil, err
		}
		edits = append(edits, ne...)
	}
	return edits, nil
}

// coordRe is a groupId:artifactId (Maven's id pattern for both).
var coordRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+:[A-Za-z0-9_.-]+$`)

// Rename implements manager.Renamer: name is groupId:artifactId, looked up in the same
// repositories (Source).
func (antManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if !coordRe.MatchString(name) {
		return d, fmt.Errorf("%q is not groupId:artifactId", name)
	}
	d.Name = name
	return d, nil
}

// InRange implements manager.RangeUpdater: whether d's Maven range admits v.
func (antManager) InRange(d dep.Dep, v string) bool {
	r, err := versioning.ParseMavenRange(d.Range)
	return err == nil && r.Admits(v)
}

// NewRange implements manager.RangeUpdater for Maven ranges as the maven manager does: pin
// writes v; a range admitting v is kept; one v exceeds is extended to admit it.
func (antManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	if strategy == dep.RangePin {
		return v, v != d.Range
	}
	r, err := versioning.ParseMavenRange(d.Range)
	if err != nil {
		return "", false
	}
	return r.Extend(d.Range, v)
}
