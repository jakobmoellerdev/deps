package composer

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"path"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// skipNoRepository is the skip reason of a package no repository can provide: Packagist is
// disabled and no composer repository admits the name.
const skipNoRepository = "no composer repository"

// composerRepo is a repository of type composer with its only/exclude name filters.
type composerRepo struct {
	url           string
	only, exclude []string
}

// admits reports whether the repository's filters let it provide name (lower-cased).
func (r composerRepo) admits(name string) bool {
	match := func(patterns []string) bool {
		return slices.ContainsFunc(patterns, func(p string) bool {
			ok, _ := path.Match(strings.ToLower(p), name)
			return ok
		})
	}
	return (len(r.only) == 0 || match(r.only)) && !match(r.exclude)
}

// repositories is what composer.json's `repositories` says about where packages come from.
type repositories struct {
	composer    []composerRepo
	noPackagist bool              // `"packagist.org": false`
	git         map[string]string // vcs repositories keyed by the package they provide → URL
	path        map[string]bool   // path repositories keyed by the package they provide
	inline      map[string]bool   // packages defined by package repositories
}

// parseRepositories reads the `repositories` value: an array of repository objects, or an
// object of them by label (a package name for vcs and path repositories), in order, where
// `"packagist.org": false` (or `"packagist": false`) disables Packagist. Repository types
// other than composer, vcs (git, github, gitlab, bitbucket), path and package are ignored; a
// path repository also provides the packages its `options.versions` names.
func parseRepositories(raw jsontext.Value) repositories {
	var r repositories
	add := func(label string, val any) {
		if b, ok := val.(bool); ok {
			if !b && (label == "packagist.org" || label == "packagist") {
				r.noPackagist = true
			}
			return
		}
		m, ok := val.(map[string]any)
		if !ok {
			return
		}
		if b, ok := m["packagist.org"].(bool); ok && !b && len(m) == 1 { // [{"packagist.org": false}]
			r.noPackagist = true
			return
		}
		typ, _ := m["type"].(string)
		u, _ := m["url"].(string)
		name, _ := m["name"].(string)
		if name == "" && strings.Contains(label, "/") {
			name = label
		}
		name = strings.ToLower(name)
		switch typ {
		case "composer":
			if u != "" {
				r.composer = append(r.composer, composerRepo{url: u, only: stringList(m["only"]), exclude: stringList(m["exclude"])})
			}
		case "vcs", "git", "github", "gitlab", "bitbucket":
			if name != "" && u != "" {
				r.git = setDefault(r.git)
				r.git[name] = u
			}
		case "path":
			names := []string{name}
			if opts, ok := m["options"].(map[string]any); ok {
				if vs, ok := opts["versions"].(map[string]any); ok {
					names = slices.AppendSeq(names, maps.Keys(vs))
				}
			}
			for _, n := range names {
				if n != "" {
					r.path = setDefault(r.path)
					r.path[strings.ToLower(n)] = true
				}
			}
		case "package":
			defs, ok := m["package"].([]any)
			if !ok {
				defs = []any{m["package"]}
			}
			for _, def := range defs {
				if p, ok := def.(map[string]any); ok {
					if n, ok := p["name"].(string); ok {
						r.inline = setDefault(r.inline)
						r.inline[strings.ToLower(n)] = true
					}
				}
			}
		}
	}
	dec := jsontext.NewDecoder(bytes.NewReader(raw), jsontext.AllowDuplicateNames(true))
	tok, err := dec.ReadToken()
	if err != nil {
		return r
	}
	for dec.PeekKind() != jsontext.KindEndArray && dec.PeekKind() != jsontext.KindEndObject && dec.PeekKind() != 0 {
		label := ""
		if tok.Kind() == '{' {
			k, err := dec.ReadToken()
			if err != nil {
				return r
			}
			label = k.String()
		} else if tok.Kind() != '[' {
			return r
		}
		var v any
		if json.UnmarshalDecode(dec, &v) != nil {
			return r
		}
		add(label, v)
	}
	return r
}

func setDefault[V any](m map[string]V) map[string]V {
	if m == nil {
		m = map[string]V{}
	}
	return m
}

func stringList(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// apply sets where each dep is looked up: a package of a package repository or a path
// repository is skipped, one of a vcs repository keyed by its name is looked up as git-tags of
// that repository, any other in the composer repositories that admit it (in order), then
// Packagist unless it is disabled (Source "" when only Packagist is left).
func (r repositories) apply(deps []dep.Dep) {
	for i := range deps {
		d := &deps[i]
		if d.Skip != "" {
			continue
		}
		name := strings.ToLower(d.Name)
		switch {
		case r.inline[name]:
			d.Skip = skipPackageRepo
			continue
		case r.path[name]:
			d.Skip = skipPath
			continue
		}
		if u, ok := r.git[name]; ok {
			if _, src := manager.GitRepo(u); src != "" {
				d.Datasource, d.Source = GitDatasource, src
			} else {
				d.Skip = skipGitURL
			}
			continue
		}
		var srcs []string
		for _, c := range r.composer {
			if c.admits(name) {
				srcs = append(srcs, c.url)
			}
		}
		switch {
		case len(srcs) == 0 && r.noPackagist:
			d.Skip = skipNoRepository
		case len(srcs) > 0 && !r.noPackagist:
			srcs = append(srcs, PackagistURL)
		}
		d.Source = strings.Join(srcs, " ")
	}
}
