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
// `<2`) takes the locked version as its Version, or is skipped without one. An abbreviated
// git `rev` takes the full commit Cargo.lock resolved it to as its Digest, or is skipped
// without one (a digest update could not tell it from the branch head). An unreadable
// Cargo.lock is reported once and its deps are bumped (LockUnread).
func (cargoManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		pkgs, git []lockedPackage
		err       error
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
			shortRev := d.Datasource == DatasourceGitRefs && len(d.Digest) < fullCommit
			if (d.Range == "" && !shortRev) || d.Skip != "" {
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
						var all []lockedPackage
						all, err = packagesOf(b, func(src string) bool {
							return strings.HasPrefix(src, "registry+") || strings.HasPrefix(src, "sparse+") || strings.HasPrefix(src, "git+")
						})
						for _, p := range all {
							if strings.HasPrefix(p.source, "git+") {
								st.git = append(st.git, p)
							} else {
								st.pkgs = append(st.pkgs, p)
							}
						}
					}
					if err != nil {
						st.err = fmt.Errorf("%s: %w", file, err)
						errs = append(errs, st.err)
					}
					locks[dir] = st
				}
				switch {
				case st.err != nil:
					d.LockUnread = true
				case shortRev:
					d.Digest = lockedCommit(st.git, d)
				default:
					d.Locked = lockedVersion(st.pkgs, d)
				}
			}
			if shortRev {
				if len(d.Digest) < fullCommit {
					d.Skip = "abbreviated git rev without a Cargo.lock commit"
				}
				continue
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

// lockedCommit returns d's rev completed to the full commit of the Cargo.lock git package of
// its crate and rev (source `git+<url>?rev=<rev>#<commit>`, also with a branch), keeping the
// case of the written prefix; d.Digest when there is none.
func lockedCommit(pkgs []lockedPackage, d *dep.Dep) string {
	for _, p := range pkgs {
		rest, commit, ok := strings.Cut(p.source, "#")
		_, query, _ := strings.Cut(rest, "?")
		if !ok || p.name != d.Name || len(commit) != fullCommit || !strings.HasPrefix(commit, strings.ToLower(d.Digest)) {
			continue
		}
		for kv := range strings.SplitSeq(query, "&") {
			if strings.EqualFold(kv, "rev="+d.Digest) {
				return d.Digest + commit[len(d.Digest):]
			}
		}
	}
	return d.Digest
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
