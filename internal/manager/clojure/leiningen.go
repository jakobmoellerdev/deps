package clojure

import (
	"path"
	"slices"
	"strings"

	clojureds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/clojure"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// LeiningenName is the leiningen manager's name (Renovate's).
const LeiningenName = "leiningen"

// Leiningen dep types: the defproject key a dependency vector is found under.
const (
	DepTypeDependencies = "dependencies"
	DepTypeManaged      = "managed-dependencies"
	DepTypePlugins      = "plugins"
	DepTypePomPlugins   = "pom-plugins"
	DepTypeParent       = "parent-project"
)

// leinKeys are the keys whose vector of dependency vectors is extracted, wherever they are
// in the defproject form (also in profiles), as in Renovate.
var leinKeys = []string{DepTypeDependencies, DepTypeManaged, DepTypePlugins, DepTypePomPlugins}

type leiningen struct{ rangeManager }

// NewLeiningen returns the leiningen manager.
func NewLeiningen() manager.Manager { return leiningen{} }

func (leiningen) Name() string { return LeiningenName }

// Match reports whether p is a project.clj.
func (leiningen) Match(p string) bool { return path.Base(p) == "project.clj" }

// leinFile is what extraction needs of a project.clj.
type leinFile struct {
	file    string
	src     []byte
	vars    map[string]*form // top-level `(def name "value")`
	repos   string           // dep Source of dependencies
	plugins string           // dep Source of plugins
	deps    []dep.Dep
}

// Extract returns the dependencies of the defproject form.
func (leiningen) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	forms, err := readForms(src)
	if err != nil {
		return nil, err
	}
	x := &leinFile{file: file, src: src, vars: map[string]*form{}}
	var project *form
	for _, f := range forms {
		if f.kind != kindList || len(f.kids) < 2 || f.kids[0].kind != kindSymbol {
			continue
		}
		switch f.kids[0].text {
		case "def":
			if len(f.kids) == 3 && f.kids[1].kind == kindSymbol && f.kids[2].kind == kindString && !f.kids[2].unquote {
				x.vars[f.kids[1].text] = f.kids[2]
			}
		case "defproject":
			if project == nil {
				project = f
			}
		}
	}
	if project == nil {
		return nil, nil
	}
	x.repositories(project)
	x.walk(project)
	return x.deps, nil
}

// repositories reads the `:repositories` and `:plugin-repositories` of the project: the
// http(s) URLs in them follow Maven Central and Clojars (Leiningen's defaults), or replace
// them under `^:replace`. Plugins use `:repositories` without `:plugin-repositories`.
func (x *leinFile) repositories(project *form) {
	var repos, plugins *form
	for i := 3; i+1 < len(project.kids); i += 2 {
		switch k := project.kids[i]; {
		case k.isKeyword("repositories"):
			repos = project.kids[i+1]
		case k.isKeyword("plugin-repositories"):
			plugins = project.kids[i+1]
		}
	}
	x.repos = repoSource(repos)
	x.plugins = x.repos
	if plugins != nil {
		x.plugins = repoSource(plugins)
	}
}

// repoSource returns the dep Source of a repositories form ("" = the datasource's defaults).
func repoSource(f *form) string {
	if f == nil {
		return ""
	}
	var urls []string
	if !f.hasMeta("replace") {
		urls = append(urls, mavends.Central, clojureds.Clojars)
	}
	var collect func(*form)
	collect = func(f *form) {
		if f.kind == kindString && f.plain && (strings.HasPrefix(f.text, "https://") || strings.HasPrefix(f.text, "http://")) {
			if u := strings.TrimSuffix(f.text, "/") + "/"; !slices.Contains(urls, u) {
				urls = append(urls, u)
			}
		}
		for _, k := range f.kids {
			collect(k)
		}
	}
	collect(f)
	return strings.Join(urls, " ")
}

// walk extracts the dependency vectors of the keys of leinKeys and the `:coords` of
// `:parent-project` in f and the forms inside it.
func (x *leinFile) walk(f *form) {
	for i, k := range f.kids {
		if k.kind == kindKeyword && i+1 < len(f.kids) {
			if v := f.kids[i+1]; v.kind == kindVector && !v.unquote {
				switch {
				case slices.Contains(leinKeys, k.text):
					for _, entry := range v.kids {
						x.entry(k.text, entry)
					}
				case k.text == "coords":
					x.entry(DepTypeParent, v)
				}
			}
		}
		x.walk(k)
	}
}

// entry extracts a dependency vector `[group/artifact "version" & options]`; vectors without a
// version (managed by `:managed-dependencies`) or with a computed name are none.
func (x *leinFile) entry(depType string, e *form) {
	if e.kind != kindVector || e.unquote || len(e.kids) < 2 {
		return
	}
	name, v := e.kids[0], e.kids[1]
	if name.unquote || name.kind != kindSymbol && (name.kind != kindString || !name.plain) {
		return
	}
	if v.kind != kindString && !v.unquote {
		return
	}
	source := x.repos
	if depType == DepTypePlugins {
		source = x.plugins
	}
	d := mavenDep(LeiningenName, depType, x.file, x.src, name.text, v, source)
	switch {
	case v.unquote && v.kind == kindSymbol:
		def, ok := x.vars[v.text]
		if !ok {
			d.Skip = skipUnresolved(v.text)
			break
		}
		setVersion(&d, def)
		if d.Skip == "" {
			d.VersionRef = v.text
		}
	case v.unquote:
		d.Skip = skipNonLiteral
	default:
		setVersion(&d, v)
	}
	x.deps = append(x.deps, d)
}
