package cargo

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// ReadLocks records the version the workspace's Cargo.lock (Locker.Scope) locks each registry
// requirement to: the highest registry package of the crate that the requirement admits
// (Cargo locks at most one per compatibility range). A requirement without a floor (`*`,
// `<2`) takes the locked version as its Version, or is skipped without one. An unreadable
// Cargo.lock is reported once and its deps are bumped (LockUnread).
func (cargoManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		pkgs []lockedPackage
		err  error
	}
	type scope struct {
		dir string
		ok  bool
	}
	scopes := map[string]scope{} // manifest -> workspace, so each manifest is scoped once
	locks := map[string]*lockState{}
	var errs []error
	for _, f := range files {
		for j := range f {
			d := &f[j]
			if d.Range == "" || d.Skip != "" {
				continue
			}
			sc, seen := scopes[d.File]
			if !seen {
				sc.dir, sc.ok = Locker{}.Scope(context.Background(), root, d.File)
				scopes[d.File] = sc
			}
			dir, ok := sc.dir, sc.ok
			var st *lockState
			if ok {
				if st = locks[dir]; st == nil {
					st = &lockState{}
					file := path.Join(dir, lockFile)
					b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
					if err == nil {
						st.pkgs, err = registryPackages(b)
					}
					if err != nil {
						st.err = fmt.Errorf("%s: %w", file, err)
						errs = append(errs, st.err)
					}
					locks[dir] = st
				}
				if st.err != nil {
					d.LockUnread = true
				} else {
					d.Locked = lockedVersion(st.pkgs, d)
				}
			}
			if d.Version == "" {
				if d.Locked == "" {
					d.Skip = "no Cargo.lock version for a requirement without a floor"
					continue
				}
				d.Version = d.Locked
			}
		}
	}
	return errs
}

// lockedVersion returns the highest locked version of d's crate that its requirement admits.
func lockedVersion(pkgs []lockedPackage, d *dep.Dep) string {
	c, err := semver.NewConstraint(cmp.Or(d.Constraint, d.Range))
	if err != nil {
		return ""
	}
	var best *semver.Version
	for _, p := range pkgs {
		if p.name != d.Name {
			continue
		}
		v, err := semver.StrictNewVersion(p.version)
		if err == nil && c.Check(v) && (best == nil || v.GreaterThan(best)) {
			best = v
		}
	}
	if best == nil {
		return ""
	}
	return best.Original()
}

// LockRoots implements manager.LockMaintainer: the Cargo.lock of each file's workspace
// (Locker.Scope) with the Cargo.toml next to it.
func (cargoManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	seen := map[string]bool{}
	for _, f := range files {
		dir, ok := Locker{}.Scope(context.Background(), root, f)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, manager.LockRoot{Manifest: path.Join(dir, manifestFile), Lockfile: path.Join(dir, lockFile)})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}
