// Package maven extracts dependencies, plugins, extensions and the parent from pom.xml files
// and build extensions from .mvn/extensions.xml.
package maven

import (
	"bytes"
	"cmp"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name.
const Name = "maven"

// DepTypes besides the dependency scopes (compile, provided, runtime, test, system, import).
const (
	DepTypeParent = "parent" // <parent>
	DepTypeBuild  = "build"  // plugins, their dependencies, extensions, annotation processor paths
)

// Central is Maven Central (id `central`), appended to the repositories a pom declares.
const Central = "https://repo.maven.apache.org/maven2/"

// defaultPluginGroup is the groupId of a <plugin> without one.
const defaultPluginGroup = "org.apache.maven.plugins"

// maxPropertyDepth bounds ${a} -> ${b} -> … chains and parent chains (and cycles).
const maxPropertyDepth = 10

var (
	matchRe = regexp.MustCompile(`(^|/|\.)pom\.xml$|(^|/)\.mvn/(extensions\.xml|maven\.config)$`)
	propRe  = regexp.MustCompile(`\$\{([^}]*)\}`)
	// builtinRe matches the model and environment properties Maven defines itself.
	builtinRe = regexp.MustCompile(`^(project|pom|env|settings)\.`)
)

// scopes are the dependency scopes, which are the DepTypes of <dependency> elements.
var scopes = map[string]bool{"compile": true, "provided": true, "runtime": true, "test": true, "system": true, "import": true}

type mavenManager struct {
	mu sync.Mutex
	// poms holds every pom extracted in this run by file, so Link can resolve inherited
	// properties and repositories and skip deps on modules of the same repository.
	poms map[string]*pom
	// mvnDirs maps each directory holding an extracted .mvn/ file (the multi-module project
	// directory of the poms below it) to its .mvn/maven.config, or nil.
	mvnDirs map[string]*config
}

// config is a .mvn/maven.config: its -D properties (user properties, which override the
// pom properties) and -P profile selection.
type config struct {
	file     string
	props    map[string]propDef
	profiles map[string]bool // -P ids: true = activated, false = deactivated (!id, -id)
}

// pom is what Link needs of an extracted pom.xml.
type pom struct {
	file           string
	ga, gav        string // own groupId:artifactId[:version]; "" when unknown
	parentGA       string // <parent> groupId:artifactId; "" = no parent
	parentGAV      string
	parentFile     string // <relativePath> resolved against the pom's directory; "" = no local parent
	props          map[string]propDef
	profiles       []profile
	builtin        map[string]string // project.groupId and friends, for names only
	repos, plugins []repo            // <repositories>, <pluginRepositories>
	version        string            // <version> as written (or the parent's), for gav
	decls          []decl            // per dep, re-resolved by Link
	plugin         []bool            // per dep: looked up in the plugin repositories
	cfg            *config           // the maven.config of the pom's project directory (Link)
}

// propDef is a property definition (or a <version> element): its value, span and line.
type propDef struct {
	value string
	span  dep.Span
	line  int
}

type profile struct {
	id     string
	props  map[string]propDef
	act    activation
	active bool // by the evaluated activation, else activeByDefault (see pom.activate)
}

// activation is a profile's <activation> as far as the repository can evaluate it.
type activation struct {
	byDefault bool       // <activeByDefault>true
	conds     bool       // has a condition besides activeByDefault
	known     bool       // every condition is evaluable: no <jdk>, <os>, unexpandable <file>, …
	files     bool       // every <file> condition holds (checked by Extract via manager.Env.Exists)
	props     []propCond // <property> conditions, checked against maven.config
}

// propCond is a <property> activation: name and value as written ("!" negates).
type propCond struct{ name, value string }

type repo struct{ id, url string }

// decl is the declaration of a dep: its index in the pom's deps, the group:artifact as
// written, its <version> element and its profile (-1 = none).
type decl struct {
	idx     int
	name    string
	version propDef
	profile int
}

// New returns the maven manager.
func New() manager.Manager {
	return &mavenManager{poms: map[string]*pom{}, mvnDirs: map[string]*config{}}
}

func (*mavenManager) Name() string { return Name }

// Match reports whether the slash path is a pom.xml (also `<name>.pom.xml`),
// .mvn/extensions.xml or .mvn/maven.config. The suffix check skips the regexp for the
// paths that cannot match.
func (*mavenManager) Match(p string) bool {
	if !strings.HasSuffix(p, "pom.xml") && !strings.HasSuffix(p, ".mvn/extensions.xml") &&
		!strings.HasSuffix(p, ".mvn/maven.config") {
		return false
	}
	return matchRe.MatchString(p)
}

// Extract parses src into an element tree with the byte span of each element's text and
// returns one Dep per versioned dependency, plugin, extension, annotation processor path and
// parent. A version `${name}` is resolved through the pom's properties (those of the dep's
// profile first); the dep's RefSpan is then the property value, so the update edits the
// property definition. A .mvn/maven.config yields no deps; Link resolves properties through
// its -D definitions.
func (m *mavenManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	dir := path.Dir(path.Dir(file))
	if path.Base(file) == "maven.config" {
		c := parseConfig(file, src)
		m.mu.Lock()
		m.mvnDirs[dir] = c
		m.mu.Unlock()
		return nil, nil
	}
	doc, err := parse(src)
	if err != nil {
		return nil, err
	}
	root := doc.child("project")
	if root == nil {
		root = doc.child("extensions")
	}
	if root == nil {
		return nil, errors.New("no <project> or <extensions> root element")
	}
	x := extractor{pom: &pom{file: file}, profileOf: map[*node]int{}, exists: env.Exists}
	if root.name == "project" {
		x.project(root)
	} else {
		m.mu.Lock()
		if _, ok := m.mvnDirs[dir]; !ok {
			m.mvnDirs[dir] = nil
		}
		m.mu.Unlock()
	}
	x.repos, x.pluginRepos = render([]*pom{x.pom}, false), render([]*pom{x.pom}, true)
	x.walk(root, false, -1)
	if root.name == "project" {
		m.mu.Lock()
		m.poms[file] = x.pom
		m.mu.Unlock()
	}
	return x.deps, nil
}

// Link resolves the properties of each pom again with its maven.config and its parents
// extracted in the same run (moving a dep to the definition), prepends the parents'
// repositories to the Source, skips the deps naming a pom of the run (a module of the
// repository, usually a -SNAPSHOT that no repository serves) and forgets the poms.
func (m *mavenManager) Link(files [][]dep.Dep) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byGA, byGAV := make(map[string]*pom, len(m.poms)), make(map[string]*pom, len(m.poms))
	for _, p := range m.poms {
		p.cfg = m.config(p.file)
		if p.cfg != nil {
			p.activate()
			s := scope{poms: []*pom{p}, profile: -1, cfg: p.cfg}
			if v, ok := s.interpolate(p.version); ok && p.ga != "" && v != "" {
				p.gav = p.ga + ":" + v
			}
			if v, ok := s.interpolate(p.parentGAV); ok {
				p.parentGAV = v
			}
		}
		index(byGA, p.ga, p)
		index(byGAV, p.gav, p)
	}
	for _, deps := range files {
		if len(deps) == 0 {
			continue
		}
		p := m.poms[deps[0].File]
		if p == nil {
			continue
		}
		s := scope{poms: m.chain(p, byGA, byGAV), cfg: p.cfg, linked: true}
		for i, u := range p.decls {
			s.profile = u.profile
			s.resolve(&deps[i], u)
		}
		if len(s.poms) > 1 {
			repos, plugins := render(s.poms, false), render(s.poms, true)
			for i := range deps {
				deps[i].Source = repos
				if p.plugin[i] {
					deps[i].Source = plugins
				}
			}
		}
	}
	for _, deps := range files {
		for i := range deps {
			switch _, internal := byGA[deps[i].Name]; {
			case internal:
				deps[i].Skip = "internal module"
			case deps[i].Name == m2eLifecycleMapping:
				deps[i].Skip = "m2e lifecycle mapping (not a published plugin)"
			}
		}
	}
	clear(m.poms)
	clear(m.mvnDirs)
}

// m2eLifecycleMapping is the Eclipse m2e configuration pseudo-plugin: poms declare it in
// pluginManagement to carry IDE settings, but no repository publishes it.
const m2eLifecycleMapping = "org.eclipse.m2e:lifecycle-mapping"

// config returns the maven.config of the multi-module project directory of the pom file:
// the nearest ancestor directory with an extracted .mvn/ file.
func (m *mavenManager) config(file string) *config {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if c, ok := m.mvnDirs[dir]; ok {
			return c
		}
		if dir == "." || dir == "/" {
			return nil
		}
	}
}

// parseConfig reads the -D/--define properties and -P/--activate-profiles selections of a
// maven.config, whose arguments are separated by whitespace or lines (`#` starts a comment
// line). A -D without value defines "true" (not editable).
func parseConfig(file string, src []byte) *config {
	c := &config{file: file, props: map[string]propDef{}, profiles: map[string]bool{}}
	var opt string // option awaiting its argument on the next token
	line := 0
	for off := 0; off < len(src); {
		end := bytes.IndexByte(src[off:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += off
		}
		line++
		l := src[off:end]
		if t := bytes.TrimSpace(l); len(t) > 0 && t[0] != '#' {
			for i := 0; i < len(l); {
				for i < len(l) && isSpace(l[i]) {
					i++
				}
				j := i
				for j < len(l) && !isSpace(l[j]) {
					j++
				}
				if i < j {
					opt = c.arg(opt, string(l[i:j]), off+i, line)
				}
				i = j
			}
		}
		off = end + 1
	}
	return c
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' }

// arg records the argument tok at byte offset off (after option opt, "" = none) and returns
// the option awaiting the next token.
func (c *config) arg(opt, tok string, off, line int) string {
	switch opt {
	case "-D", "--define":
		c.define(tok, off, line)
		return ""
	case "-P", "--activate-profiles":
		c.selectProfiles(tok)
		return ""
	}
	for _, o := range []string{"--define", "--activate-profiles", "-D", "-P"} {
		rest, ok := strings.CutPrefix(tok, o)
		if !ok {
			continue
		}
		if rest == "" {
			return o
		}
		if strings.HasPrefix(o, "--") {
			if rest, ok = strings.CutPrefix(rest, "="); !ok {
				return "" // another long option, e.g. --define-foo
			}
		}
		if o == "-D" || o == "--define" {
			c.define(rest, off+len(tok)-len(rest), line)
		} else {
			c.selectProfiles(rest)
		}
		return ""
	}
	return ""
}

// define records name=value at byte offset off; surrounding quotes are not part of the value.
func (c *config) define(kv string, off, line int) {
	name, value, ok := strings.Cut(kv, "=")
	if name == "" {
		return
	}
	if !ok {
		c.props[name] = propDef{value: "true", line: line}
		return
	}
	start := off + len(name) + 1
	if n := len(value); n >= 2 && (value[0] == '"' || value[0] == '\'') && value[n-1] == value[0] {
		value, start = value[1:n-1], start+1
	}
	c.props[name] = propDef{value: value, span: dep.Span{Start: start, End: start + len(value)}, line: line}
}

// selectProfiles records the comma-separated profile ids of -P ("!id" or "-id" deactivates, a "?"
// prefix marks an optional id).
func (c *config) selectProfiles(list string) {
	for id := range strings.SplitSeq(list, ",") {
		id = strings.TrimPrefix(strings.TrimSpace(id), "?")
		on := true
		if r, ok := strings.CutPrefix(id, "!"); ok {
			id, on = r, false
		} else if r, ok := strings.CutPrefix(id, "-"); ok {
			id, on = r, false
		}
		if id = strings.TrimPrefix(id, "?"); id != "" {
			c.profiles[id] = on
		}
	}
}

// activate sets the active profiles of p as Maven does: those selected by -P or whose
// evaluated conditions all hold (property against maven.config, file via manager.Env.Exists;
// <jdk>, <os> and the like count as not holding), unless deselected; the activeByDefault ones
// when no other is active.
func (p *pom) activate() {
	var props map[string]propDef
	var sel map[string]bool
	if p.cfg != nil {
		props, sel = p.cfg.props, p.cfg.profiles
	}
	anyActive := false
	for i := range p.profiles {
		pr := &p.profiles[i]
		on, chosen := sel[pr.id]
		if !chosen {
			on = pr.act.holds(props)
		}
		pr.active = on
		anyActive = anyActive || on
	}
	if anyActive {
		return
	}
	for i := range p.profiles {
		pr := &p.profiles[i]
		if _, deselected := sel[pr.id]; !deselected {
			pr.active = pr.act.byDefault
		}
	}
}

// holds reports whether the evaluable conditions of a hold against the user properties.
func (a activation) holds(props map[string]propDef) bool {
	if !a.conds || !a.known || !a.files {
		return false
	}
	for _, c := range a.props {
		if !c.holds(props) {
			return false
		}
	}
	return true
}

// holds evaluates c as Maven's PropertyProfileActivator: with a value, the property equals it
// (`!v`: differs, also when unset); without, the property is set and not empty (`!name`: is
// not).
func (c propCond) holds(props map[string]propDef) bool {
	name, notName := strings.CutPrefix(c.name, "!")
	v := props[name].value
	if c.value != "" {
		want, notValue := strings.CutPrefix(c.value, "!")
		return (v == want) != notValue
	}
	return (v != "") != notName
}

// index maps key to p, or to nil when several poms share the key.
func index(byKey map[string]*pom, key string, p *pom) {
	if key == "" {
		return
	}
	if _, dup := byKey[key]; dup {
		p = nil
	}
	byKey[key] = p
}

// chain returns p followed by its parents extracted in this run, nearest first.
func (m *mavenManager) chain(p *pom, byGA, byGAV map[string]*pom) []*pom {
	chain := []*pom{p}
	for range maxPropertyDepth {
		q := m.parent(chain[len(chain)-1], byGA, byGAV)
		if q == nil || slices.Contains(chain, q) {
			break
		}
		chain = append(chain, q)
	}
	return chain
}

// parent returns the in-run parent of p: the pom at its relativePath if its coordinates
// match, else the pom with the parent's groupId:artifactId:version or groupId:artifactId.
// An empty <relativePath/> means no local parent.
func (m *mavenManager) parent(p *pom, byGA, byGAV map[string]*pom) *pom {
	if p.parentGA == "" || p.parentFile == "" {
		return nil
	}
	if q := m.poms[p.parentFile]; q != nil && q.ga == p.parentGA {
		return q
	}
	if q := byGAV[p.parentGAV]; q != nil {
		return q
	}
	return byGA[p.parentGA]
}

// Edits replaces the version token (the property value for a `${name}` version).
func (*mavenManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

type extractor struct {
	pom         *pom
	exists      func(path string) bool // manager.Env.Exists; nil = file activation not evaluable
	profileOf   map[*node]int          // <profile> element -> index in pom.profiles
	repos       string                 // dep Source: <repositories> + Central, "" = Central only
	pluginRepos string                 // plugin Source: <pluginRepositories> + Central
	deps        []dep.Dep
}

// project reads the properties (also those of profiles), the repositories, the parent and
// the coordinates.
func (x *extractor) project(n *node) {
	p := x.pom
	p.props = properties(n.child("properties"))
	if list := n.child("profiles"); list != nil {
		for _, k := range list.kids {
			if k.name != "profile" {
				continue
			}
			x.profileOf[k] = len(p.profiles)
			p.profiles = append(p.profiles, profile{
				id:    k.text("id"),
				props: properties(k.child("properties")),
				act:   x.activation(k.child("activation")),
			})
		}
		p.activate()
	}
	p.repos = repositories(n.child("repositories"), "repository")
	p.plugins = repositories(n.child("pluginRepositories"), "pluginRepository")
	parent := n.child("parent")
	p.builtin = map[string]string{
		"project.groupId":        cmp.Or(n.text("groupId"), parent.text("groupId")),
		"project.artifactId":     n.text("artifactId"),
		"project.parent.groupId": parent.text("groupId"),
	}
	s := scope{poms: []*pom{p}, profile: -1}
	if g, a := p.builtin["project.groupId"], p.builtin["project.artifactId"]; g != "" && a != "" {
		if ga, ok := s.interpolate(g + ":" + a); ok {
			p.ga = ga
			p.version = cmp.Or(n.text("version"), parent.text("version"))
			if v, ok := s.interpolate(p.version); ok && v != "" {
				p.gav = ga + ":" + v
			}
		}
	}
	if g, a := parent.text("groupId"), parent.text("artifactId"); g != "" && a != "" {
		p.parentGA = g + ":" + a
		p.parentGAV = p.parentGA + ":" + parent.text("version")
		rel := "../pom.xml"
		if r := parent.child("relativePath"); r != nil {
			rel = r.value
		}
		if rel != "" {
			if !strings.HasSuffix(rel, ".xml") {
				rel = path.Join(rel, "pom.xml")
			}
			p.parentFile = path.Join(path.Dir(p.file), rel)
		}
	}
}

// activation reads an <activation> (nil = none); <file> paths are relative to the pom's
// directory, `${basedir}` and `${project.basedir}` expanded.
func (x *extractor) activation(n *node) activation {
	a := activation{known: true, files: true}
	if n == nil {
		return a
	}
	for _, k := range n.kids {
		switch k.name {
		case "activeByDefault":
			a.byDefault = k.value == "true"
			continue
		case "property":
			a.props = append(a.props, propCond{name: k.text("name"), value: k.text("value")})
			a.known = a.known && k.text("name") != ""
		case "file":
			for _, f := range k.kids {
				if f.name != "exists" && f.name != "missing" {
					continue
				}
				ok, known := x.fileExists(f.value)
				a.known = a.known && known
				a.files = a.files && ok == (f.name == "exists")
			}
		default: // jdk, os, packaging, condition: depend on the machine or the build
			a.known = false
		}
		a.conds = true
	}
	return a
}

// fileExists checks the activation file p; known is false when it cannot be decided from the
// repository (no checker, absolute or outside the root, other properties).
func (x *extractor) fileExists(p string) (ok, known bool) {
	for _, b := range []string{"${basedir}/", "${project.basedir}/"} {
		p = strings.TrimPrefix(p, b)
	}
	if x.exists == nil || p == "" || strings.Contains(p, "${") || path.IsAbs(p) || strings.Contains(p, `\`) {
		return false, false
	}
	p = path.Join(path.Dir(x.pom.file), p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return false, false
	}
	return x.exists(p), true
}

// properties returns the definitions below a <properties> element (nil when n is nil).
func properties(n *node) map[string]propDef {
	if n == nil {
		return nil
	}
	props := make(map[string]propDef, len(n.kids))
	for _, k := range n.kids {
		props[k.name] = propDef{value: k.value, span: k.span, line: k.line}
	}
	return props
}

// repositories returns the http(s) <repository> elements below list; URLs with properties
// are ignored.
func repositories(list *node, elem string) []repo {
	if list == nil {
		return nil
	}
	var repos []repo
	for _, r := range list.kids {
		u := r.text("url")
		if r.name != elem || strings.Contains(u, "${") || !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			continue
		}
		if !strings.HasSuffix(u, "/") {
			u += "/"
		}
		repos = append(repos, repo{id: r.text("id"), url: u})
	}
	return repos
}

// render returns the dep Source of the (plugin) repositories of chain, child first and
// without repeated ids or URLs, followed by Maven Central unless a repository is Central or
// has the id `central`; "" when there are none.
func render(chain []*pom, plugin bool) string {
	var out []repo
	central := false
	for _, p := range chain {
		repos := p.repos
		if plugin {
			repos = p.plugins
		}
		for _, r := range repos {
			if slices.ContainsFunc(out, func(o repo) bool { return r.id != "" && o.id == r.id || o.url == r.url }) {
				continue
			}
			central = central || r.url == Central || r.id == "central"
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range out {
		if i > 0 {
			b.WriteByte(' ')
		}
		if r.id != "" {
			b.WriteString(r.id)
			b.WriteByte('=')
		}
		b.WriteString(r.url)
	}
	if !central {
		b.WriteString(" central=" + Central)
	}
	return b.String()
}

// walk extracts the dep elements below n. inPlugin is set below a <plugin>, whose
// dependencies are build deps; profile is the index of the enclosing <profile> or -1.
func (x *extractor) walk(n *node, inPlugin bool, profile int) {
	for _, k := range n.kids {
		switch {
		case k.name == "dependency" && n.name == "dependencies":
			depType := DepTypeBuild
			if !inPlugin {
				depType = strings.ToLower(k.text("scope"))
				if !scopes[depType] {
					depType = "compile"
				}
			}
			x.add(k, depType, "", false, profile)
		case k.name == "plugin" && n.name == "plugins":
			x.add(k, DepTypeBuild, defaultPluginGroup, true, profile)
		case k.name == "extension" && n.name == "extensions":
			x.add(k, DepTypeBuild, "", true, profile)
		case k.name == "path" && n.name == "annotationProcessorPaths":
			x.add(k, DepTypeBuild, "", false, profile)
		case k.name == "parent" && n.name == "project" && n.parent != nil && n.parent.parent == nil:
			x.add(k, DepTypeParent, "", false, profile)
		}
		kp := profile
		if i, ok := x.profileOf[k]; ok {
			kp = i
		}
		x.walk(k, inPlugin || k.name == "plugin", kp)
	}
}

// add appends the dep of element e; elements without artifactId or version (managed by a
// parent or BOM) are not deps. The declaration is kept for Link.
func (x *extractor) add(e *node, depType, defaultGroup string, plugin bool, profile int) {
	a, v := e.child("artifactId"), e.child("version")
	if a == nil || v == nil || a.value == "" {
		return
	}
	group := cmp.Or(e.text("groupId"), defaultGroup)
	if group == "" {
		return
	}
	u := decl{idx: len(x.deps), name: group + ":" + a.value, version: propDef{value: v.value, span: v.span, line: v.line}, profile: profile}
	d := dep.Dep{Manager: Name, Datasource: "maven", Versioning: "maven", DepType: depType, Source: x.repos}
	if plugin {
		d.Source = x.pluginRepos
	}
	(scope{poms: []*pom{x.pom}, profile: profile}).resolve(&d, u)
	x.pom.decls = append(x.pom.decls, u)
	x.deps = append(x.deps, d)
	x.pom.plugin = append(x.pom.plugin, plugin)
}

// scope is where the properties of a dep are looked up.
type scope struct {
	poms    []*pom  // the dep's pom, then its in-run parents (Link only)
	profile int     // index of the dep's profile in poms[0].profiles; -1 = none
	cfg     *config // maven.config user properties, which override the poms' (Link only)
	linked  bool    // poms holds every in-run parent: a missing property is remote
}

// resolve sets the name, version, RefSpan, VersionRef and skip of d from its declaration u.
// A version from a property of another file (a parent pom, maven.config) moves the dep
// (File, Line) to the definition.
func (s scope) resolve(d *dep.Dep, u decl) {
	d.File, d.Line, d.Name, d.Version = s.poms[0].file, u.version.line, u.name, u.version.value
	d.RefSpan, d.VersionRef, d.Skip = dep.Span{}, "", ""
	name, ok := s.interpolate(u.name)
	if !ok {
		d.Skip = "unresolved property in " + u.name
		return
	}
	d.Name = name
	v, owner, ref := u.version, s.poms[0].file, ""
	for depth := 0; ; depth++ {
		m := propRe.FindStringSubmatch(v.value)
		if m == nil {
			break
		}
		if m[0] != v.value {
			d.Skip = "unsupported version " + v.value
			return
		}
		if depth == maxPropertyDepth {
			d.Skip = "unresolved property " + v.value
			return
		}
		def, file, ok := s.lookup(m[1])
		if !ok {
			d.Skip = s.missing(m[0], m[1])
			return
		}
		v, owner, ref = def, file, m[1]
	}
	switch {
	case strings.HasPrefix(v.value, "[") || strings.HasPrefix(v.value, "("):
		d.Skip, d.Version = "version range", v.value
	case v.span == (dep.Span{}):
		d.Skip, d.Version = "cannot locate value", v.value
	default:
		d.Version, d.RefSpan, d.VersionRef = v.value, v.span, ref
		if owner != s.poms[0].file {
			d.File, d.Line = owner, v.line
		}
	}
}

// lookup returns the file and nearest definition of the property name: a maven.config -D,
// the dep's profile, the only active profile defining it (several: none), the top-level
// properties, then likewise in each parent.
func (s scope) lookup(name string) (propDef, string, bool) {
	if s.cfg != nil {
		if d, ok := s.cfg.props[name]; ok {
			return d, s.cfg.file, true
		}
	}
	for i, p := range s.poms {
		if i == 0 && s.profile >= 0 {
			if d, ok := p.profiles[s.profile].props[name]; ok {
				return d, p.file, true
			}
		}
		var def propDef
		n := 0
		for _, pr := range p.profiles {
			if d, ok := pr.props[name]; ok && pr.active {
				def = d
				n++
			}
		}
		switch {
		case n == 1:
			return def, p.file, true
		case n > 1:
			return propDef{}, "", false
		}
		if d, ok := p.props[name]; ok {
			return d, p.file, true
		}
	}
	return propDef{}, "", false
}

// missing returns the skip reason of the undefined property name (written ref).
func (s scope) missing(ref, name string) string {
	for _, p := range s.poms {
		for _, pr := range p.profiles {
			if _, ok := pr.props[name]; ok {
				return "property " + ref + " only defined in profiles"
			}
		}
	}
	if s.linked && s.poms[len(s.poms)-1].parentGA != "" && !builtinRe.MatchString(name) {
		return "property " + ref + " not defined in this repository"
	}
	return "unresolved property " + ref
}

// interpolate expands the ${name} references in s from the properties and the project
// coordinates; ok is false when one cannot be resolved.
func (s scope) interpolate(str string) (string, bool) {
	for range maxPropertyDepth {
		if !strings.Contains(str, "${") {
			return str, true
		}
		ok := true
		str = propRe.ReplaceAllStringFunc(str, func(ref string) string {
			key := ref[2 : len(ref)-1]
			if d, _, found := s.lookup(key); found {
				return d.value
			}
			if b := s.poms[0].builtin[key]; b != "" {
				return b
			}
			ok = false
			return ref
		})
		if !ok {
			return str, false
		}
	}
	return str, false
}

// node is an XML element with its trimmed character data.
type node struct {
	name   string
	parent *node
	kids   []*node
	value  string   // trimmed character data
	span   dep.Span // of value in the source; zero when the raw bytes differ (entities, CDATA, comments)
	line   int      // 1-based line of the start tag
	pieces int      // non-blank character data tokens
}

// child returns the first child element named name, or nil (also on a nil n).
func (n *node) child(name string) *node {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

// text returns the value of the child named name, or "".
func (n *node) text(name string) string {
	if c := n.child(name); c != nil {
		return c.value
	}
	return ""
}

// parse builds the element tree of src; the returned node is the document (no name).
func parse(src []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(src))
	// Poms may declare ISO-8859-1; the values deps reads are ASCII.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	dec.Entity = xml.HTMLEntity
	doc := &node{}
	cur := doc
	line, pos := 1, 0
	lineAt := func(off int) int {
		line += bytes.Count(src[pos:off], []byte{'\n'})
		pos = off
		return line
	}
	for {
		start := int(dec.InputOffset())
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return doc, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name.Local, parent: cur, line: lineAt(start)}
			cur.kids = append(cur.kids, n)
			cur = n
		case xml.EndElement:
			cur = cur.parent
		case xml.CharData:
			b := bytes.TrimSpace(t)
			if len(b) == 0 {
				continue
			}
			s := string(b)
			cur.pieces++
			cur.value += s
			raw := src[start:dec.InputOffset()]
			i := bytes.Index(raw, b)
			if cur.pieces == 1 && i >= 0 && bytes.Equal(bytes.TrimSpace(raw), b) {
				cur.span = dep.Span{Start: start + i, End: start + i + len(s)}
			} else {
				cur.span = dep.Span{}
			}
		}
	}
}
