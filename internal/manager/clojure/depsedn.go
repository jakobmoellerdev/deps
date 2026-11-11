package clojure

import (
	"path"
	"strings"

	clojureds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/clojure"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// DepsEdnName is the deps-edn manager's name (Renovate's).
const DepsEdnName = "deps-edn"

// deps-edn dep types: the map a lib is found in.
const (
	DepTypeDeps         = "deps"          // top-level :deps
	DepTypeExtraDeps    = "extra-deps"    // an alias's :extra-deps
	DepTypeOverrideDeps = "override-deps" // an alias's :override-deps
	DepTypeReplaceDeps  = "replace-deps"  // an alias's :replace-deps or :deps
	DepTypeDefaultDeps  = "default-deps"  // an alias's :default-deps
)

// aliasKeys maps the dependency maps of an alias to their dep types.
var aliasKeys = []struct{ key, depType string }{
	{"extra-deps", DepTypeExtraDeps}, {"override-deps", DepTypeOverrideDeps},
	{"replace-deps", DepTypeReplaceDeps}, {"deps", DepTypeReplaceDeps}, {"default-deps", DepTypeDefaultDeps},
}

// Skip reasons of git libs.
const (
	skipGitURL = "unsupported git url"
	skipGitRef = "git lib without tag or sha"
	skipGitSHA = "invalid git sha"
)

type depsEdn struct{ rangeManager }

// NewDepsEdn returns the deps-edn manager.
func NewDepsEdn() manager.Manager { return depsEdn{} }

func (depsEdn) Name() string { return DepsEdnName }

// Match reports whether p is a deps.edn or bb.edn.
func (depsEdn) Match(p string) bool {
	b := path.Base(p)
	return b == "deps.edn" || b == "bb.edn"
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (depsEdn) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{"deps.edn", "bb.edn"}}
}

// Extract returns the Maven and git libs of the top-level map's `:deps` and of its aliases.
func (depsEdn) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	forms, err := readForms(src)
	if err != nil || len(forms) == 0 || forms[0].kind != kindMap {
		return nil, err
	}
	root := forms[0]
	source := mvnRepos(root.mapGet("mvn/repos"))
	var deps []dep.Dep
	section := func(m *form, depType string) {
		if m == nil || m.kind != kindMap {
			return
		}
		for i := 0; i+1 < len(m.kids); i += 2 {
			lib := m.kids[i]
			if lib.kind != kindSymbol {
				continue
			}
			if d, ok := ednLib(file, src, depType, lib.text, m.kids[i+1], source); ok {
				deps = append(deps, d)
			}
		}
	}
	section(root.mapGet("deps"), DepTypeDeps)
	if aliases := root.mapGet("aliases"); aliases != nil && aliases.kind == kindMap {
		for i := 1; i < len(aliases.kids); i += 2 {
			for _, k := range aliasKeys {
				section(aliases.kids[i].mapGet(k.key), k.depType)
			}
		}
	}
	return deps, nil
}

// mvnRepos returns the dep Source of `:mvn/repos`: Maven Central and Clojars (the Clojure
// CLI's defaults, unless set to nil or given another `:url`) and the other repositories in
// file order; "" without `:mvn/repos` (the datasource's defaults).
func mvnRepos(m *form) string {
	if m == nil || m.kind != kindMap {
		return ""
	}
	names := []string{"central", "clojars"}
	urls := map[string]string{"central": mavends.Central, "clojars": clojureds.Clojars}
	for i := 0; i+1 < len(m.kids); i += 2 {
		k, v := m.kids[i], m.kids[i+1]
		if k.kind != kindString {
			continue
		}
		if _, known := urls[k.text]; !known {
			names = append(names, k.text)
		}
		switch u := v.mapGet("url"); {
		case v.kind == kindSymbol && v.text == "nil":
			urls[k.text] = ""
		case u != nil && u.kind == kindString && u.plain:
			urls[k.text] = strings.TrimSuffix(u.text, "/") + "/"
		}
	}
	var out []string
	for _, n := range names {
		if u := urls[n]; u != "" {
			out = append(out, u)
		}
	}
	return strings.Join(out, " ")
}

// coordGet returns the value of the qualified key ns/name of a coordinate map, also written
// unqualified in a namespaced map `#:ns{…}`, or the legacy unqualified key.
func coordGet(coord *form, ns, name string, legacy bool) *form {
	if v := coord.mapGet(ns + "/" + name); v != nil {
		return v
	}
	if legacy || coord.text == ns {
		return coord.mapGet(name)
	}
	return nil
}

// ednLib returns the dep of lib with the coordinate map coord: `:mvn/version` libs use the
// clojure datasource, git libs (`:git/url`, `:git/tag`, `:git/sha`) git-tags or git-refs;
// others (`:local/root`) are no deps.
func ednLib(file string, src []byte, depType, lib string, coord *form, source string) (dep.Dep, bool) {
	if coord.kind != kindMap {
		return dep.Dep{}, false
	}
	if v := coordGet(coord, "mvn", "version", false); v != nil && v.kind == kindString {
		d := mavenDep(DepsEdnName, depType, file, src, lib, v, source)
		setVersion(&d, v)
		return d, true
	}
	url := coordGet(coord, "git", "url", false)
	tag := coordGet(coord, "git", "tag", true)
	sha := coordGet(coord, "git", "sha", true)
	if url == nil && tag == nil && sha == nil {
		return dep.Dep{}, false
	}
	at := coord
	for _, f := range []*form{url, sha, tag} {
		if f != nil {
			at = f
		}
	}
	d := dep.Dep{Manager: DepsEdnName, Datasource: "git-tags", Versioning: "tag", DepType: depType,
		File: file, Line: lineOf(src, at.start), Name: lib}
	switch {
	case url != nil && url.kind == kindString && url.plain:
		_, d.Source = manager.GitRepo(url.text)
	case url == nil:
		d.Source = inferredRepo(lib)
	}
	if d.Source == "" {
		d.Skip = skipGitURL
		return d, true
	}
	if sha != nil {
		if sha.kind != kindString || !sha.plain || len(sha.text) < 7 || len(sha.text) > 40 || !manager.IsHex(sha.text, len(sha.text)) {
			d.Skip = skipGitSHA
			return d, true
		}
		d.Digest, d.DigestSpan = sha.text, dep.Span{Start: sha.end - 1 - len(sha.text), End: sha.end - 1}
	}
	switch {
	case tag != nil && (tag.kind != kindString || !tag.plain):
		d.Skip = skipLocate
	case tag != nil:
		d.Version, d.RefSpan = tag.text, dep.Span{Start: tag.end - 1 - len(tag.text), End: tag.end - 1}
	case sha != nil:
		d.Datasource, d.Version = "git-refs", "HEAD"
	default:
		d.Skip = skipGitRef
	}
	return d, true
}

// inferredRepo returns the repository the Clojure CLI infers from a git lib's name without
// `:git/url`: `io.github.<owner>/<repo>` and `com.github.…` on GitHub, `io.gitlab`/`com.gitlab`
// on GitLab, `io.bitbucket`/`org.bitbucket` on Bitbucket, `ht.sr.~<user>/<repo>` on
// SourceHut; "" otherwise.
func inferredRepo(lib string) string {
	group, repo, ok := strings.Cut(lib, "/")
	if !ok || repo == "" {
		return ""
	}
	repo, _, _ = strings.Cut(repo, "$")
	for _, p := range []struct{ prefix, host string }{
		{"io.github.", "github.com"}, {"com.github.", "github.com"},
		{"io.gitlab.", "gitlab.com"}, {"com.gitlab.", "gitlab.com"},
		{"io.bitbucket.", "bitbucket.org"}, {"org.bitbucket.", "bitbucket.org"},
		{"ht.sr.", "git.sr.ht"},
	} {
		if owner, ok := strings.CutPrefix(group, p.prefix); ok && owner != "" && !strings.Contains(owner, ".") {
			return "https://" + p.host + "/" + owner + "/" + repo
		}
	}
	return ""
}
