package glasskube

import (
	"cmp"
	"slices"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// repository is a PackageRepository: its metadata.name, spec.url and whether it is the
// default repository.
type repository struct {
	name, url string
	isDefault bool
}

// fileRepos are the PackageRepositories of one file, in document order.
type fileRepos struct {
	file  string
	repos []repository
}

// links collects the PackageRepositories and the packages' repository names of every file of
// a run (Extract runs concurrently) until Link resolves them.
type links struct {
	mu    sync.Mutex
	repos []fileRepos
	refs  map[string][]string // by file: the repositoryName of each dep, in dep order
}

// add records the repositories and the deps' repository names of file.
func (l *links) add(file string, repos []repository, refs []string) {
	if repos == nil && refs == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if repos != nil {
		l.repos = append(l.repos, fileRepos{file, repos})
	}
	if refs != nil {
		if l.refs == nil {
			l.refs = map[string][]string{}
		}
		l.refs[file] = refs
	}
}

// Link resolves the repository of every package as Renovate's extractAllPackageFiles does,
// against the PackageRepositories of all files of the run in path order: the first one
// named like the package's repositoryName, or, without a repositoryName, the first default
// repository. Its spec.url becomes the dep's source; a package without one is skipped
// (Renovate's unknown-registry). Then it forgets the run's definitions.
func (m *glasskubeManager) Link(files [][]dep.Dep) {
	l := &m.links
	l.mu.Lock()
	defer l.mu.Unlock()
	slices.SortFunc(l.repos, func(a, b fileRepos) int { return cmp.Compare(a.file, b.file) })
	for _, deps := range files {
		if len(deps) == 0 {
			continue
		}
		refs := l.refs[deps[0].File]
		for i := range min(len(deps), len(refs)) {
			d := &deps[i]
			url, ok := l.find(refs[i])
			switch {
			case ok:
				d.Source = url
			case d.Skip != "":
			case refs[i] == "":
				d.Skip = "no default PackageRepository"
			default:
				d.Skip = "unknown PackageRepository " + refs[i]
			}
		}
	}
	l.repos, l.refs = nil, nil
}

// find returns the URL of the repository a package with repositoryName name uses.
func (l *links) find(name string) (string, bool) {
	for _, f := range l.repos {
		for _, r := range f.repos {
			if name != "" && r.name == name || name == "" && r.isDefault {
				return r.url, true
			}
		}
	}
	return "", false
}
