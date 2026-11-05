package pytool

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// ReadLocks records, for the deps with a range of each file (a manager.LockReader's files),
// the version the lockfile of their lock root locks them to (dep.Dep.Locked). The lock root
// is Locker.Scope's and its lockfile the one Locker updates: Pipfile.lock for a Pipfile (the
// section of the dep's Pipfile table), else uv.lock, poetry.lock or pdm.lock, in that order.
// Each lockfile is read once; a name locked at several versions (uv and pdm forks by marker)
// counts as not locked. A lockfile that does not parse is returned as an error prefixed with
// its slash path, and its deps stay unlocked.
func ReadLocks(root string, files [][]dep.Dep) []error {
	locks := map[string]map[string]map[string]string{} // by slash path, then as parseLock
	lockOf := map[string]string{}                      // lockfile slash path by manifest, "" = none
	pipfiles := map[string]func(off int) string{}      // Pipfile categories by slash path
	var errs []error
	for _, f := range files {
		for j := range f {
			d := &f[j]
			if d.Range == "" || d.Skip != "" {
				continue
			}
			p, seen := lockOf[d.File]
			if !seen {
				if dir, ok := (Locker{}).Scope(root, d.File); ok {
					if name := lockName(root, dir, d.File); name != "" {
						p = path.Join(dir, name)
					}
				}
				lockOf[d.File] = p
			}
			if p == "" {
				continue
			}
			name := path.Base(p)
			versions, read := locks[p]
			if !read {
				src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
				if err == nil {
					versions, err = parseLock(name, src)
				}
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", p, err))
				}
				locks[p] = versions
			}
			section := ""
			if name == PipfileLock {
				category, ok := pipfiles[d.File]
				if !ok {
					src, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(d.File)))
					category = pipfileCategories(src)
					pipfiles[d.File] = category
				}
				section = pipfileSection(category(d.RefSpan.Start))
			}
			d.Locked = versions[section][pyreq.Normalize(d.Name)]
		}
	}
	return errs
}

// lockName is the lockfile Locker.Process updates for manifest in its lock directory dir.
func lockName(root, dir, manifest string) string {
	names := []string{UVLock, PoetryLock, PDMLock}
	if path.Base(manifest) == Pipfile {
		names = []string{PipfileLock}
	}
	for _, n := range names {
		if isFile(filepath.Join(root, filepath.FromSlash(dir), n)) {
			return n
		}
	}
	return ""
}

// pipfileSection is the Pipfile.lock section of a Pipfile category.
func pipfileSection(category string) string {
	switch category {
	case "packages":
		return "default"
	case "dev-packages":
		return "develop"
	}
	return category
}

// parseLock returns the locked versions of lockfile name by section and normalized name.
func parseLock(name string, src []byte) (map[string]map[string]string, error) {
	if name == PipfileLock {
		return parsePipfileLock(src)
	}
	doc, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	ambiguous := map[string]bool{}
	if a := doc.Get("package"); a != nil && a.Kind == tomlspan.Array {
		for _, p := range a.Items {
			n, v := p.Get("name"), p.Get("version")
			if n == nil || v == nil || n.Kind != tomlspan.String || v.Kind != tomlspan.String {
				continue
			}
			k := pyreq.Normalize(n.Str)
			if prev, ok := out[k]; ok && prev != v.Str {
				ambiguous[k] = true
			}
			out[k] = v.Str
		}
	}
	for k := range ambiguous {
		delete(out, k)
	}
	return map[string]map[string]string{"": out}, nil
}

// parsePipfileLock reads the `"==<version>"` pins of every section but `_meta`.
func parsePipfileLock(src []byte) (map[string]map[string]string, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for section, raw := range doc {
		if section == "_meta" {
			continue
		}
		var pkgs map[string]struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &pkgs); err != nil {
			return nil, fmt.Errorf("%s: %w", section, err)
		}
		m := map[string]string{}
		for n, p := range pkgs {
			if v, ok := strings.CutPrefix(p.Version, "=="); ok {
				m[pyreq.Normalize(n)] = v
			}
		}
		out[section] = m
	}
	return out, nil
}
