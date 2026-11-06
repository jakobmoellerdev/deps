package helm

import (
	"slices"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// key names a repository: a Flux object (namespace, name) or a k0s alias (name only).
type key struct{ namespace, name string }

type refKind uint8

const (
	refK0s  refKind = iota // the dep's repository is the k0s alias key.name
	refFlux                // the dep's repository is the HelmRepository key
	refOCI                 // the dep is the OCIRepository key: kept only if a chartRef names it
)

// ref is a dep of a file whose repository (or whether it is a dep at all) Link decides.
type ref struct {
	i    int // index into the file's deps
	kind refKind
	key  key
	// alias is the registryAliases URL of a refFlux sourceRef's name, which an unknown
	// HelmRepository falls back to (Renovate's flux manager); "" = none.
	alias string
}

// fileLinks are the definitions and references of one file.
type fileLinks struct {
	k0sRepos   map[string]string // k0s ClusterConfig repository alias → URL
	helmRepos  map[key]string    // Flux HelmRepository → URL (oci:// for OCI registries)
	chartRefs  []key             // OCIRepositories named by a HelmRelease chartRef
	repoConfig map[string]string // Helm repository config (repositories.yaml) name → URL
	refs       []ref
}

// k0sRepo is a k0s repository alias definition and the file declaring it.
type k0sRepo struct{ file, url string }

// links collects the definitions and references of every file of a run (Extract runs
// concurrently) until Link resolves them. It only holds repository names and URLs.
type links struct {
	mu         sync.Mutex
	k0sRepos   map[string][]k0sRepo
	helmRepos  map[key]string
	chartRefs  map[key]bool
	repoConfig map[string][]string // Helm repository config name → its distinct URLs
	refs       map[string][]ref    // by file
}

// add merges the definitions and references of file.
func (l *links) add(file string, f *fileLinks) {
	if f.k0sRepos == nil && f.helmRepos == nil && f.chartRefs == nil && f.repoConfig == nil && f.refs == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for alias, url := range f.k0sRepos {
		l.k0sRepos = addTo(l.k0sRepos, alias, append(l.k0sRepos[alias], k0sRepo{file, url}))
	}
	for k, url := range f.helmRepos {
		l.helmRepos = addTo(l.helmRepos, k, url)
	}
	for _, k := range f.chartRefs {
		l.chartRefs = addTo(l.chartRefs, k, true)
	}
	for name, url := range f.repoConfig {
		if !slices.Contains(l.repoConfig[name], url) {
			l.repoConfig = addTo(l.repoConfig, name, append(l.repoConfig[name], url))
		}
	}
	if f.refs != nil {
		l.refs = addTo(l.refs, file, f.refs)
	}
}

// addTo sets m[k] = v, allocating m when nil.
func addTo[K comparable, V any](m map[K]V, k K, v V) map[K]V {
	if m == nil {
		m = map[K]V{}
	}
	m[k] = v
	return m
}

// Link resolves the repository references of the run's deps: k0s chart aliases against the
// ClusterConfig repositories, Flux HelmRelease sourceRefs against the HelmRepositories (else
// registryAliases), Chart.yaml repository aliases against the repositories.yaml files (one
// URL per name), and drops the OCIRepositories that are not charts. Then it forgets the run's
// definitions.
func (m *helmManager) Link(files [][]dep.Dep) {
	l := &m.links
	l.mu.Lock()
	defer l.mu.Unlock()
	for f, deps := range files {
		if len(deps) == 0 {
			continue
		}
		refs := l.refs[deps[0].File]
		drop := false
		for _, r := range refs {
			d := &deps[r.i]
			switch r.kind {
			case refK0s:
				d.Source, d.Skip = l.k0sRepo(d.File, r.key.name)
			case refFlux:
				d.Source, d.Skip = l.helmRepo(r.key)
				if url, skip := chartRepository(r.alias); strings.HasPrefix(d.Skip, "unknown ") && skip == "" {
					d.Source, d.Skip = url, ""
				}
			case refOCI:
				if !l.chartRef(r.key) {
					d.Manager, drop = "", true
				}
			}
		}
		for i := range deps {
			if d := &deps[i]; d.Skip == skipAlias {
				if name, _ := aliasName(d.Source); len(l.repoConfig[name]) == 1 {
					resolveAlias(d, l.repoConfig[name][0])
				}
			}
		}
		if drop {
			files[f] = slices.DeleteFunc(deps, func(d dep.Dep) bool { return d.Manager == "" })
		}
	}
	l.k0sRepos, l.helmRepos, l.chartRefs, l.repoConfig, l.refs = nil, nil, nil, nil, nil
}

// k0sRepo resolves a k0s repository alias used in file. One URL for the alias wins; with
// several (one repository holding the configs of several clusters) the alias of file's own
// ClusterConfig wins, else the alias is ambiguous.
func (l *links) k0sRepo(file, alias string) (url, skip string) {
	defs := l.k0sRepos[alias]
	for _, d := range defs {
		if d.file == file {
			return d.url, ""
		}
	}
	switch {
	case len(defs) == 0:
		return "", "unknown chart repository " + alias
	case slices.ContainsFunc(defs[1:], func(d k0sRepo) bool { return d.url != defs[0].url }):
		return "", "ambiguous chart repository " + alias
	}
	return defs[0].url, ""
}

// helmRepo resolves a HelmRelease sourceRef. Without an exact match, a HelmRepository of that
// name matches when one of the namespaces is unset (a Kustomization's targetNamespace sets
// it); the match must be unique.
func (l *links) helmRepo(k key) (url, skip string) {
	if u, ok := l.helmRepos[k]; ok {
		return u, ""
	}
	for o, u := range l.helmRepos {
		if !sameObject(o, k) {
			continue
		}
		if url != "" && u != url {
			return "", "ambiguous HelmRepository " + k.name
		}
		url = u
	}
	if url == "" {
		return "", "unknown HelmRepository " + k.namespace + "/" + k.name
	}
	return url, ""
}

// chartRef reports whether a HelmRelease chartRef names the OCIRepository k, with the
// namespace matching of helmRepo.
func (l *links) chartRef(k key) bool {
	if l.chartRefs[k] {
		return true
	}
	for o := range l.chartRefs {
		if sameObject(o, k) {
			return true
		}
	}
	return false
}

// sameObject reports whether a and b name the same object when one namespace is unset.
func sameObject(a, b key) bool {
	return a.name == b.name && (a.namespace == "" || b.namespace == "")
}
