package helm

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// repositoriesFile is the name of a Helm repository config (`helm repo add` writes
// $HELM_CONFIG_HOME/repositories.yaml); a copy committed to the repository resolves the
// `@name` and `alias:name` repositories of Chart.yaml files.
const repositoriesFile = "repositories.yaml"

// repositoryConfig records the repositories of a Helm repository config in src for Link:
// `repositories[]` entries with `name` and an http(s) or oci:// `url`. Other files of that name
// (a Flux manifest listing HelmRepositories) record nothing.
func (m *helmManager) repositoryConfig(file string, src []byte) {
	var cfg struct {
		Repositories []struct {
			Name string `yaml:"name"`
			URL  string `yaml:"url"`
		} `yaml:"repositories"`
	}
	if yaml.Unmarshal(src, &cfg) != nil {
		return
	}
	var f fileLinks
	for _, r := range cfg.Repositories {
		if url, skip := chartRepository(r.URL); r.Name != "" && skip == "" {
			f.repoConfig = addTo(f.repoConfig, r.Name, url)
		}
	}
	m.links.add(file, &f)
}

// ReadLocks implements manager.LockReader: a Chart.yaml (requirements.yaml) dependency whose
// repository alias neither registryAliases nor a repositories.yaml resolved takes the
// repository its Chart.lock (requirements.lock) locks for the chart (`helm dependency update`
// writes the resolved URL), else Renovate's default alias `stable`. Lockfiles that are absent
// or do not parse resolve nothing; the post-processor reports them. No versions are recorded:
// ranges are rewritten without lockfile-only updates.
func (*helmManager) ReadLocks(root string, files [][]dep.Dep) []error {
	locks := map[string][]*chartDependency{} // by lock path; nil = unreadable
	for _, deps := range files {
		for i := range deps {
			d := &deps[i]
			if d.Skip != skipAlias {
				continue
			}
			name, _ := aliasName(d.Source)
			resolveAlias(d, lockedRepository(root, d, locks))
			resolveAlias(d, defaultAliases[name])
		}
	}
	return nil
}

// lockedRepository returns the repository the lock next to d's manifest locks d's chart at,
// "" when it has none or several; locks caches the parsed locks.
func lockedRepository(root string, d *dep.Dep, locks map[string][]*chartDependency) string {
	name := lockFile
	if d.DepType == TypeRequirements {
		name = requirementsLock
	}
	p := path.Join(path.Dir(d.File), name)
	locked, seen := locks[p]
	if !seen {
		if src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p))); err == nil {
			if l, err := parseLock(src); err == nil {
				locked = l.deps
			}
		}
		locks[p] = locked
	}
	url := ""
	for _, ld := range locked {
		if ld.Name != d.Name || ld.Repository == "" {
			continue
		}
		r := strings.TrimRight(ld.Repository, "/")
		if url != "" && r != url {
			return ""
		}
		url = r
	}
	return url
}
