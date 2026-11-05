package gradle

import (
	"regexp"
	"sort"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// configurations are the dependency configurations (and platform wrappers) followed by a
// coordinate string or map notation in build scripts.
var configurations = []string{
	"implementation", "api", "compileOnly", "compileOnlyApi", "runtimeOnly",
	"testImplementation", "testRuntimeOnly", "testCompileOnly", "androidTestImplementation",
	"debugImplementation", "releaseImplementation", "testFixturesImplementation", "testFixturesApi",
	"developmentOnly", "kapt", "ksp", "annotationProcessor", "classpath", "compile", "testCompile",
	"detektPlugins", "lintChecks", "checkstyle", "platform", "enforcedPlatform",
}

// coordRe, mapRe and pluginRe do not match a newline; each runs only on the lines that can
// hold a match (see inLines).
var (
	configs = `\b(` + strings.Join(configurations, "|") + `)[ \t]*\(?[ \t]*`
	// coordRe: `implementation("g:n:v")`, `api 'g:n:v'`, `implementation(platform("g:n:v"))`.
	coordRe = regexp.MustCompile(configs + `["']`)
	// mapRe: `implementation group: 'g', name: 'n', version: 'v'`, `implementation(group = "g", …)`.
	mapRe = regexp.MustCompile(configs + `(group|name|version)[ \t]*[:=]`)
	// pluginRe: `id("x") version "y"`, `id 'x' version 'y'`, `id("x").version("y")`,
	// `kotlin("jvm") version "y"`, `id("x") version kotlinVersion`; the version follows the match.
	pluginRe = regexp.MustCompile(`\b(id|kotlin)[ \t]*\(?[ \t]*(["']) *["'][ \t]*\)?[ \t]*\.?[ \t]*version\b[ \t]*\(?[ \t]*`)
	// gaRe is a group or artifact id of a coordinate found outside a dependency declaration.
	gaRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)
)

// configWords are the words coordRe and mapRe matches start with: a configuration, which
// mapRe's key can also follow directly (`implementationgroup:`).
var configWords = func() map[string]bool {
	m := make(map[string]bool, 4*len(configurations))
	for _, c := range configurations {
		m[c] = true
		for _, k := range []string{"group", "name", "version"} {
			m[c+k] = true
		}
	}
	return m
}()

// found is an extracted dep with the offset it is ordered by.
type found struct {
	off  int
	d    dep.Dep
	meta depMeta
}

type extractor struct {
	file  string
	s     *source
	out   []found
	taken map[int]bool // string literals already extracted, by content start
}

// extractScript returns the deps, their metadata, the variable definitions and the
// repositories of a build script, settings file or buildSrc source.
func extractScript(file string, src []byte, kind fileKind) ([]dep.Dep, []depMeta, defs, repoDecls) {
	groovy := strings.HasSuffix(file, ".gradle") || strings.HasSuffix(file, ".groovy")
	x := &extractor{file: file, s: scan(src, groovy), taken: map[int]bool{}}
	s := x.s
	if kind != kindSource {
		configLines := wordLines(s.code, configWords)
		for m := range inLines(coordRe, s.code, configLines) {
			if l, ok := s.lit(m[1]); ok {
				x.coordinate(l, x.class(m[0], string(s.code[m[2]:m[3]])), false)
			}
		}
		for m := range inLines(mapRe, s.code, configLines) {
			x.mapNotation(m[4], x.class(m[0], string(s.code[m[2]:m[3]])))
		}
		for m := range inLines(pluginRe, s.code, litLines(s.code, "version")) {
			x.plugin(m)
		}
		if kind == kindSettings {
			x.settingsCatalogs()
		}
	}
	as := s.assignments()
	sort.Slice(as, func(i, j int) bool { return as[i].lit.start < as[j].lit.start })
	vars := defs{}
	for _, a := range as {
		if x.coordinate(a.lit, x.class(a.lit.start, ""), true) {
			continue
		}
		if v := s.text(a.lit); literalDef(v) {
			vars.add(def{name: a.keys[0], value: v, span: dep.Span{Start: a.lit.start, End: a.lit.end}, line: s.lines.at(a.lit.start)}, a.keys...)
		}
	}
	if kind == kindSource {
		for _, l := range s.strs {
			x.coordinate(l, classLibrary, true)
		}
	}
	var repos repoDecls
	if kind != kindSource {
		repos = s.repositories(kind == kindSettings)
	}
	sort.SliceStable(x.out, func(i, j int) bool { return x.out[i].off < x.out[j].off })
	deps := make([]dep.Dep, len(x.out))
	meta := make([]depMeta, len(x.out))
	for i, f := range x.out {
		deps[i], meta[i] = f.d, f.meta
	}
	return deps, meta, vars, repos
}

// class is the repository class of a dependency declared at off with configuration conf.
func (x *extractor) class(off int, conf string) depClass {
	if conf == "classpath" || x.s.within(x.s.enclosing(off), "buildscript") {
		return classClasspath
	}
	return classLibrary
}

func (x *extractor) add(off int, d dep.Dep, meta depMeta) {
	x.out = append(x.out, found{off: off, d: d, meta: meta})
}

// coordinate extracts `group:name:version[:classifier][@ext]` from the literal l and reports
// whether it is one. Strict (outside dependency declarations) requires plain group and
// artifact ids. A version that is exactly one variable reference is left to Link.
func (x *extractor) coordinate(l strLit, class depClass, strict bool) bool {
	if x.taken[l.start] {
		return true
	}
	s := x.s.text(l)
	parts := strings.Split(s, ":")
	if len(parts) < 3 || len(parts) > 4 || parts[0] == "" || parts[1] == "" {
		return false
	}
	if strict && (!gaRe.MatchString(parts[0]) || !gaRe.MatchString(parts[1]) || strings.ContainsAny(s, " \t")) {
		return false
	}
	v, _, _ := strings.Cut(parts[2], "@")
	if v == "" {
		return false
	}
	d := newDep(x.file, DepTypeDependencies, x.s.lines.at(l.start))
	d.Name = parts[0] + ":" + parts[1]
	meta := depMeta{class: class}
	start := l.start + len(parts[0]) + len(parts[1]) + 2
	if strings.Contains(parts[0]+parts[1], "$") {
		d.Skip = "version variable"
	} else {
		if plainString(d.Name) {
			d.NameParts = []dep.NamePart{namePart(d.Name, l.start, dep.NameWhole)}
		}
		x.version(&d, &meta, v, dep.Span{Start: start, End: start + len(v)})
	}
	x.taken[l.start] = true
	x.add(l.start, d, meta)
	return true
}

// version sets the version of d from the literal content v at span: a literal, or a
// reference to a variable left to Link.
func (x *extractor) version(d *dep.Dep, meta *depMeta, v string, span dep.Span) {
	key, ref := templateRef(v, x.s.groovy)
	switch {
	case !ref:
		setVersion(d, v, span)
	case key == "":
		d.Skip = "version variable"
	default:
		x.ref(d, meta, key)
	}
}

func (*extractor) ref(d *dep.Dep, meta *depMeta, key string) {
	meta.ref = key
	d.Skip = "unresolved variable " + key
}

// value is a parsed argument value: a string literal or a bare reference expression.
type value struct {
	lit    strLit
	isLit  bool
	expr   string
	off    int
	exists bool
}

// argValue parses a value at i: a string literal or a bare reference; end is the offset
// after it.
func (x *extractor) argValue(i int) (v value, end int) {
	s := x.s
	if l, ok := s.litAfter(i); ok {
		return value{lit: l, isLit: true, off: i, exists: true}, l.end + 1
	}
	if m := exprRe.Find(s.code[i:]); m != nil {
		return value{expr: string(s.src[i : i+len(m)]), off: i, exists: true}, i + len(m)
	}
	return value{}, i
}

// mapNotation parses `group: 'g', name: 'n', version: 'v'` (or `key = "v"`) starting at i.
func (x *extractor) mapNotation(i int, class depClass) {
	s := x.s
	fields := map[string]value{}
	for i < len(s.code) {
		i = s.skipSpace(i)
		j := i
		for j < len(s.code) && isIdent(s.code[j]) {
			j++
		}
		key := string(s.code[i:j])
		j = s.skipSpace(j)
		if key == "" || j >= len(s.code) || s.code[j] != ':' && s.code[j] != '=' {
			break
		}
		v, end := x.argValue(s.skipSpace(j + 1))
		if !v.exists {
			break
		}
		fields[key] = v
		if i = s.skipSpace(end); i >= len(s.code) || s.code[i] != ',' {
			break
		}
		i = s.skipBlank(i + 1)
	}
	g, n, ver := fields["group"], fields["name"], fields["version"]
	if !g.isLit || !n.isLit || !ver.exists || x.taken[ver.lit.start] && ver.isLit {
		return
	}
	d := newDep(x.file, DepTypeDependencies, s.lines.at(g.off))
	group, name := s.text(g.lit), s.text(n.lit)
	if group == "" || name == "" {
		return
	}
	d.Name = group + ":" + name
	meta := depMeta{class: class}
	switch {
	case strings.Contains(d.Name, "$"):
		d.Skip = "version variable"
	case ver.isLit:
		v := s.text(ver.lit)
		if v == "" {
			return
		}
		x.version(&d, &meta, v, dep.Span{Start: ver.lit.start, End: ver.lit.end})
		x.taken[ver.lit.start] = true
	default:
		x.exprVersion(&d, &meta, ver.expr)
	}
	if plainString(d.Name) {
		d.NameParts = []dep.NamePart{namePart(group, g.lit.start, dep.NameGroup), namePart(name, n.lit.start, dep.NameArtifact)}
	}
	x.taken[g.lit.start], x.taken[n.lit.start] = true, true
	x.add(g.off, d, meta)
}

// exprVersion sets a version given as a bare expression (`kotlinVersion`, `Versions.kotlin`).
func (x *extractor) exprVersion(d *dep.Dep, meta *depMeta, expr string) {
	if key, ok := refKey(expr); ok {
		x.ref(d, meta, key)
	} else {
		d.Skip = "version variable"
	}
}

// plugin extracts a plugins { } entry matched by pluginRe.
func (x *extractor) plugin(m []int) {
	s := x.s
	idLit, ok := s.lit(m[4] + 1)
	if !ok {
		return
	}
	id := s.text(idLit)
	kotlin := string(s.src[m[2]:m[3]]) == "kotlin"
	if kotlin {
		id = "org.jetbrains.kotlin." + id
	}
	v, _ := x.argValue(m[1])
	if !v.exists || id == "" {
		return
	}
	d := newDep(x.file, DepTypePlugin, s.lines.at(m[0]))
	d.Name = id
	if !kotlin && plainString(id) {
		d.NameParts = []dep.NamePart{namePart(id, idLit.start, dep.NameWhole)}
	}
	meta := depMeta{class: classPlugin}
	switch {
	case strings.Contains(id, "$"):
		d.Skip = "version variable"
	case v.isLit:
		ver := s.text(v.lit)
		if ver == "" {
			return
		}
		x.version(&d, &meta, ver, dep.Span{Start: v.lit.start, End: v.lit.end})
		x.taken[v.lit.start] = true
	default:
		x.exprVersion(&d, &meta, v.expr)
	}
	x.add(m[0], d, meta)
}
