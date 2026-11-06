package composer

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// lockFile is the lockfile next to composer.json.
const lockFile = "composer.lock"

// lockPath returns the slash path of the lockfile of manifest (both relative to the root).
func lockPath(manifest string) string { return path.Join(path.Dir(manifest), lockFile) }

// lockedPackage is a package entry of composer.lock; other fields are skipped.
type lockedPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Dist    struct {
		Type string `json:"type"`
	} `json:"dist"`
}

// lockedPackages returns the entries of a composer.lock's `packages` and `packages-dev` by
// lower-cased name. A file without either is not a lockfile.
func lockedPackages(b []byte) (map[string]lockedPackage, error) {
	var lock struct {
		Packages    *[]lockedPackage `json:"packages"`
		PackagesDev []lockedPackage  `json:"packages-dev"`
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		return nil, err
	}
	if lock.Packages == nil {
		return nil, errors.New("not a composer.lock (no packages)")
	}
	out := make(map[string]lockedPackage, len(*lock.Packages)+len(lock.PackagesDev))
	for _, p := range slices.Concat(*lock.Packages, lock.PackagesDev) {
		out[strings.ToLower(p.Name)] = p
	}
	return out, nil
}

// ReadLocks records the version the composer.lock next to each composer.json locks its
// packages to (dep.Dep.Locked). A package the lock installs from a path repository (dist type
// `path`) is skipped, a branch version (`dev-main`) is not a locked version. A constraint
// without a lower bound (`*`, `<2.0`) takes the locked version as its Version, or is skipped
// without one. An unreadable lockfile is reported once and its deps are bumped (LockUnread).
func (composerManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		pkgs map[string]lockedPackage
		err  error
	}
	locks := map[string]*lockState{}
	var errs []error
	for _, f := range files {
		for j := range f {
			d := &f[j]
			if d.Skip != "" || d.Range == "" {
				continue
			}
			file := lockPath(d.File)
			st := locks[file]
			if st == nil {
				st = &lockState{}
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
				if err == nil {
					st.pkgs, err = lockedPackages(b)
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					st.err = fmt.Errorf("%s: %w", file, err)
					errs = append(errs, st.err)
				}
				locks[file] = st
			}
			if st.err != nil {
				d.LockUnread = true
			} else if p, ok := st.pkgs[strings.ToLower(d.Name)]; ok {
				if p.Dist.Type == "path" {
					d.Skip = skipPath
					continue
				}
				if _, _, ok := versioning.ComposerVersionParts(p.Version); ok {
					d.Locked = p.Version
				}
			}
			if d.Version == "" {
				if d.Locked == "" {
					d.Skip = skipNoLock
					continue
				}
				d.Version = d.Locked
			}
		}
	}
	return errs
}

// LockRoots implements manager.LockMaintainer: the composer.lock of each composer.json with one.
func (composerManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if _, ok := (Locker{}).Scope(context.Background(), root, f); ok {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: lockPath(f)})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// Locker is the composer post-processor (engine.PostProcessor, UpdatePinner and
// LockMaintainer): it updates the composer.lock of a patched composer.json with `composer
// update` in a temporary copy of the directory. The working tree is never touched.
type Locker struct{}

// Siblings lists composer.lock.
func (Locker) Siblings() []string { return []string{lockFile} }

// Scope returns the directory of a composer.json that has a composer.lock next to it.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(lockPath(manifest))))
	return path.Dir(manifest), err == nil && st.Mode().IsRegular()
}

// Process updates the lockfile for the patched composer.json.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates moves every updated package, manifest updates (ups) and lockfile-only ones
// (locks) alike, to exactly the update's version: `composer update <name>:<version>…
// --with-dependencies --minimal-changes`, so only those packages move, and their dependencies
// only where the new versions need it; the inline constraint keeps Composer from locking a
// newer version the rewritten constraint admits. Without updates (a patched manifest only)
// `composer update --lock` refreshes the lock's content-hash. It returns the lockfile when it
// changed.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	orig, ok := files[lockFile]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", lockFile, dir)
		}
		return nil, nil
	}
	src, ok := manifests[manifest]
	if !ok {
		var err error
		if src, err = os.ReadFile(filepath.Join(dir, manifest)); err != nil {
			return nil, err
		}
	}
	var pkgs []string
	for _, u := range slices.Concat(flatten(ups), flatten(locks)) {
		p := strings.ToLower(u.Dep.Name) + ":" + u.NewVersion
		if !slices.Contains(pkgs, p) {
			pkgs = append(pkgs, p)
		}
	}
	args := []string{"update", "--lock"}
	if len(pkgs) > 0 {
		args = append(append([]string{"update"}, pkgs...), "--with-dependencies", "--minimal-changes")
	}
	return run(ctx, dir, src, orig, args)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `composer update` in a copy
// of dir moves every locked package to the newest version the constraints admit; composer.json
// is unchanged. It returns the lockfile when it changed; without one it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, manifest))
	if err != nil {
		return nil, err
	}
	return run(ctx, dir, src, lock, []string{"update"})
}

// flatten returns the updates of every manifest, in manifest path order.
func flatten(byManifest map[string][]dep.Update) []dep.Update {
	var ups []dep.Update
	for _, rel := range slices.Sorted(maps.Keys(byManifest)) {
		ups = append(ups, byManifest[rel]...)
	}
	return ups
}

// run runs `composer <args> <flags>` in a mirror of dir holding src as composer.json and lock
// as composer.lock (mirrorDir) and returns composer.lock when it changed.
func run(ctx context.Context, dir string, src, lock []byte, args []string) (map[string][]byte, error) {
	tmp, base, err := mirrorDir(dir, map[string][]byte{manifest: src, lockFile: lock})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := composer(ctx, base, dir, append(args, flags(src)...)...); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{lockFile: out}, nil
}

// flags are the options of every composer update: only the lock is written (no install, no
// scripts, plugins or audit), and platform requirements are ignored, since the runner's PHP
// and extensions are not the project's; with a `config.platform.php` in composer.json, which
// stands in for the PHP version, only extensions and libraries are.
func flags(src []byte) []string {
	out := []string{"--no-install", "--no-scripts", "--no-plugins", "--no-audit", "--no-interaction", "--no-ansi"}
	var cfg struct {
		Config struct {
			Platform map[string]any `json:"platform"`
		} `json:"config"`
	}
	if json.Unmarshal(src, &cfg) == nil && cfg.Config.Platform["php"] != nil {
		return append(out, "--ignore-platform-req=ext-*", "--ignore-platform-req=lib-*")
	}
	return append(out, "--ignore-platform-reqs")
}

// mirrorDir builds a temporary tree (toolrun.Mirror) in which the files of dir in copies
// (composer.json and composer.lock) are written and every other entry of dir but .git and
// vendor is symlinked, so path repositories (`../packages/*`) resolve as in the checkout. The
// caller removes tmp; base is the mirror of dir.
func mirrorDir(dir string, copies map[string][]byte) (tmp, base string, err error) {
	return toolrun.Mirror(dir, toolrun.MirrorOptions{
		Pattern: "deps-composer-*",
		Files:   copies,
		Skip:    func(rel string, _ fs.DirEntry) bool { return rel == ".git" || rel == "vendor" },
	})
}

// composer runs `composer args...` in dir (the mirror of scope) with COMPOSER naming its
// composer.json; a failure carries the tail of Composer's output, with the mirror's paths
// shown as scope's.
func composer(ctx context.Context, dir, scope string, args ...string) error {
	bin, err := exec.LookPath("composer")
	if err != nil {
		return fmt.Errorf("composer not found (needed to update %s)", lockFile)
	}
	cmd, cancel := toolrun.Command(ctx, bin, args...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env("COMPOSER="+filepath.Join(dir, manifest), "COMPOSER_NO_INTERACTION=1", "NO_COLOR=1")
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "composer command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("composer %s in %s: %s", args[0], scope, msg)
}
