package sbt

import (
	"cmp"
	"path"
	"slices"
	"strings"

	sbtds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/sbt"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// build is one sbt build: the files of a root directory (its *.sbt files and those of
// subdirectories without a build of their own, project/*.sbt, project/*.scala and
// project/build.properties).
type build struct {
	root   string
	files  []string // build definition files first (*.sbt outside project/), then project/*.scala, then project/*.sbt
	states map[string]*fileState

	sbtVersion string
	proxies    []string // the repositories file's entries; nil = none

	// The Scala version of the build definition: full (from scalaVersion or the first
	// crossScalaVersions, "" = unknown) and binary ("" = unresolved: a setting exists but
	// does not resolve to a literal).
	scalaFull, scalaBin string

	resolvers, metaResolvers []string
}

// rootOf returns the build root of file given the roots of project/ directories: the file's
// project root for project files, else the nearest root at or above its directory, else its
// directory.
func rootOf(file string, roots map[string]bool) string {
	dir := path.Dir(file)
	if path.Base(dir) == "project" {
		return path.Dir(dir)
	}
	if file == "repositories" {
		return "."
	}
	for d := dir; ; d = path.Dir(d) {
		if roots[d] {
			return d
		}
		if d == "." || d == "/" {
			return dir
		}
	}
}

// groupBuilds returns the build of each file.
func groupBuilds(states map[string]*fileState) map[string]*build {
	roots := map[string]bool{}
	var proxies []string
	for f, st := range states {
		if path.Base(path.Dir(f)) == "project" {
			roots[path.Dir(path.Dir(f))] = true
		}
		if st.kind == kindRepositories {
			proxies = st.proxies
		}
	}
	byRoot := map[string][]string{}
	for f := range states {
		r := rootOf(f, roots)
		byRoot[r] = append(byRoot[r], f)
	}
	out := make(map[string]*build, len(states))
	for r, fs := range byRoot {
		b := newBuild(r, states, fs)
		b.proxies = proxies
		for _, f := range fs {
			out[f] = b
		}
	}
	return out
}

// newBuild indexes the files of a build and resolves its sbt and Scala versions and
// resolvers.
func newBuild(root string, states map[string]*fileState, files []string) *build {
	b := &build{root: root, states: states}
	order := func(f string) int {
		switch states[f].kind {
		case kindBuild:
			if path.Dir(f) == root {
				return 0
			}
			return 1
		case kindProjectScala:
			return 2
		}
		return 3
	}
	b.files = slices.Clone(files)
	slices.SortStableFunc(b.files, func(x, y string) int {
		if ox, oy := order(x), order(y); ox != oy {
			return ox - oy
		}
		return strings.Compare(x, y)
	})
	var settings, crosses bool
	for _, f := range b.files {
		st := states[f]
		switch st.kind {
		case kindProperties:
			b.sbtVersion = st.sbtVersion
			continue
		case kindMeta:
			b.metaResolvers = append(b.metaResolvers, st.scan.resolvers...)
			continue
		case kindRepositories:
			continue
		}
		b.resolvers = append(b.resolvers, st.scan.resolvers...)
		for _, s := range st.scan.scala {
			settings = true
			if b.scalaFull == "" {
				if r, ok := b.resolve(f, s.value, 0); ok {
					b.scalaFull = r.tok.text
				}
			}
		}
		crosses = crosses || len(st.scan.cross) > 0
	}
	if b.scalaFull == "" {
	cross:
		for _, f := range b.files {
			if st := states[f]; st.kind == kindBuild || st.kind == kindProjectScala {
				for _, c := range st.scan.cross {
					if r, ok := b.resolve(f, c, 0); ok {
						b.scalaFull = r.tok.text
						break cross
					}
				}
			}
		}
	}
	switch {
	case b.scalaFull != "":
		b.scalaBin = scalaBinary(b.scalaFull)
	case !settings && !crosses:
		b.scalaBin = b.metaScala() // sbt's default scalaVersion is its own Scala version
	}
	return b
}

// metaScala returns the Scala binary version of the meta-build (sbt's own Scala).
func (b *build) metaScala() string {
	switch {
	case strings.HasPrefix(b.sbtVersion, "2."):
		return "3"
	case strings.HasPrefix(b.sbtVersion, "0.13"):
		return "2.10"
	}
	return "2.12"
}

// resolved is a string literal a term resolves to, with the file holding it and the
// definition it was reached through ("" = the term itself is the literal).
type resolved struct {
	tok  token
	file string
	def  string
}

// maxDepth bounds reference chains (`val a = b`).
const maxDepth = 8

// resolve returns the string literal t refers to from file: the literal itself, or the
// definition of a reference (following `val a = b` chains, `<module>.revision` and
// `<seq>.head`).
func (b *build) resolve(file string, t term, depth int) (resolved, bool) {
	switch {
	case t.kind == termStr:
		return resolved{tok: t.tok, file: file}, true
	case t.kind != termRef || depth > maxDepth:
		return resolved{}, false
	}
	ref := strings.TrimPrefix(t.ref, "_root_.")
	if p, ok := strings.CutSuffix(ref, ".revision"); ok {
		f, d := b.lookup(file, p)
		if d == nil || d.kind != defModule {
			return resolved{}, false
		}
		r, ok := b.resolve(f, d.mod.version, depth+1)
		r.def = cmp.Or(r.def, d.path)
		return r, ok
	}
	if p, ok := strings.CutSuffix(ref, ".head"); ok {
		f, d := b.lookup(file, p)
		if d == nil || d.kind != defSeq || len(d.items) == 0 {
			return resolved{}, false
		}
		return b.resolve(f, d.items[0], depth+1)
	}
	f, d := b.lookup(file, ref)
	if d == nil {
		return resolved{}, false
	}
	switch d.kind {
	case defString:
		return resolved{tok: d.tok, file: f, def: d.path}, true
	case defRef:
		r, ok := b.resolve(f, term{kind: termRef, ref: d.ref}, depth+1)
		r.def = cmp.Or(r.def, d.path)
		return r, ok
	}
	return resolved{}, false
}

// lookup returns the definition ref names, seen from file: one whose qualified path is ref
// or ends with `.ref`, in file first, then in the other files of the build.
func (b *build) lookup(file, ref string) (string, *def) {
	find := func(f string) *def {
		st := b.states[f]
		if st == nil {
			return nil
		}
		for i := range st.scan.defs {
			d := &st.scan.defs[i]
			if d.path == ref || strings.HasSuffix(d.path, "."+ref) {
				return d
			}
		}
		return nil
	}
	if d := find(file); d != nil {
		return file, d
	}
	for _, f := range b.files {
		if f == file {
			continue
		}
		if d := find(f); d != nil {
			return f, d
		}
	}
	return "", nil
}

// str returns the plain string a group or artifact term resolves to.
func (b *build) str(file string, t term) (string, bool) {
	r, ok := b.resolve(file, t, 0)
	if !ok || !r.tok.plain || r.tok.text == "" {
		return "", false
	}
	return r.tok.text, true
}

// dep returns the dep extracted as mt from file, resolved against the build; keep is false
// for a ModuleID whose group or artifact is not a string.
func (b *build) dep(file string, st *fileState, mt depMeta) (d dep.Dep, keep bool) {
	switch mt.kind {
	case metaModule:
		return b.moduleDep(file, st, mt.found)
	case metaScala:
		// scala-library is looked up by the maven datasource, which reads ivy=<url> entries
		// as Maven repositories: drop the Ivy repositories from its Source.
		src := strings.Fields(b.librarySource(false))
		src = slices.DeleteFunc(src, func(s string) bool { return strings.HasPrefix(s, sbtds.IvyID+"=") })
		d = dep.Dep{Manager: Name, Datasource: "maven", Versioning: "maven", DepType: DepTypeScala,
			File: file, Line: mt.scala.line, Name: "org.scala-lang:scala-library", Source: strings.Join(src, " ")}
		b.version(&d, file, mt.scala.value)
		if d.Skip == "" && strings.HasPrefix(d.Version, "3.") {
			d.Name = "org.scala-lang:scala3-library_3"
		}
		return d, true
	}
	return d, false // metaFixed deps are complete at extraction
}

// librarySource returns the dep Source of libraries of the build (meta: of the meta-build):
// the proxies, else Maven Central, then the resolvers; "" when that is Maven Central alone.
func (b *build) librarySource(meta bool) string {
	base := []string{central}
	if b.proxies != nil {
		base = b.proxies
	}
	res := b.resolvers
	if meta {
		res = b.metaResolvers
	}
	if s := sources(base, res); s != central {
		return s
	}
	return ""
}

// pluginSource returns the dep Source of sbt plugins: the proxies, else the sbt plugin
// releases (Ivy) and Maven Central, then the meta-build's resolvers.
func (b *build) pluginSource() string {
	if b.proxies != nil {
		return sources(b.proxies, b.metaResolvers)
	}
	return sources([]string{sbtds.IvyID + "=" + sbtds.PluginReleases, central}, b.metaResolvers)
}

// moduleDep returns the dep of a ModuleID.
func (b *build) moduleDep(file string, st *fileState, f found) (dep.Dep, bool) {
	m := f.mod
	g, okG := b.str(file, m.group)
	a, okA := b.str(file, m.artifact)
	if !okG || !okA {
		return dep.Dep{}, false
	}
	meta := st.kind == kindMeta
	d := dep.Dep{Manager: Name, Datasource: sbtds.PackageName, Versioning: "maven", DepType: libraryDepType(m.config),
		File: file, Line: m.line, Name: g + ":" + a}
	switch {
	case f.plugin:
		d.Datasource, d.DepType, d.SbtVersion = sbtds.PluginName, DepTypePlugin, b.sbtVersion
		d.Source = b.pluginSource()
	default:
		if f.compilerPlugin {
			d.DepType = DepTypePlugin
		}
		d.Source = b.librarySource(meta)
		suffix, ok := b.crossSuffix(m, meta)
		if !ok {
			d.Skip = skipScalaVersion
			return d, true
		}
		d.Name += suffix
	}
	b.version(&d, file, m.version)
	return d, true
}

// crossSuffix returns the artifact suffix of a ModuleID: `_<binary>` for `%%`/`%%%` and
// CrossVersion.binary, `_<full>` for CrossVersion.full (and patch), none for `%` and
// CrossVersion.disabled. ok is false when the Scala version it needs is unknown.
func (b *build) crossSuffix(m *module, meta bool) (string, bool) {
	bin, full := b.scalaBin, b.scalaFull
	if meta {
		bin, full = b.metaScala(), ""
	}
	mode := m.crossVersion
	if mode == "" {
		if m.cross == "%" {
			return "", true
		}
		mode = "binary"
	}
	switch mode {
	case "disabled":
		return "", true
	case "full", "patch":
		if full == "" {
			return "", false
		}
		return "_" + full, true
	}
	if bin == "" {
		return "", false
	}
	switch {
	case mode == "for3Use2_13" && bin == "3":
		bin = "2.13"
	case mode == "for2_13Use3" && bin == "2.13":
		bin = "3"
	}
	return "_" + bin, true
}

// version sets the version of d from the term t of file: a plain literal, or the literal a
// reference resolves to (d then points at the definition).
func (b *build) version(d *dep.Dep, file string, t term) {
	switch t.kind {
	case termStr:
	case termRef:
	default:
		d.Skip = skipNonLiteral
		return
	}
	r, ok := b.resolve(file, t, 0)
	if !ok {
		d.Skip = skipUnresolved(t.ref)
		return
	}
	if !r.tok.plain || r.tok.text == "" {
		d.Skip = skipNonLiteral
		return
	}
	d.File, d.Version, d.VersionRef = r.file, r.tok.text, r.def
	if r.def != "" {
		d.Line = r.tok.line
	}
	d.RefSpan = dep.Span{Start: r.tok.start, End: r.tok.end}
	if dynamic(d.Version) {
		d.Skip = skipDynamic
	}
}
