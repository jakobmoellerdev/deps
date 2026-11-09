package conda

import (
	"bytes"
	"cmp"
	"context"
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

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// lockFile is the unified lockfile conda-lock writes next to the environment file.
const lockFile = "conda-lock.yml"

// lockPath returns the slash path of the lockfile of manifest (both relative to the root).
func lockPath(manifest string) string { return path.Join(path.Dir(manifest), lockFile) }

// condaLock is the part of a conda-lock.yml deps reads.
type condaLock struct {
	Version  int `yaml:"version"`
	Metadata struct {
		Platforms []string `yaml:"platforms"`
		Sources   []string `yaml:"sources"`
	} `yaml:"metadata"`
	Package []struct {
		Name     string `yaml:"name"`
		Version  string `yaml:"version"`
		Manager  string `yaml:"manager"`
		Platform string `yaml:"platform"`
	} `yaml:"package"`
}

// parseLock reads a conda-lock.yml; one without `version` and `package` is not a lockfile.
func parseLock(b []byte) (*condaLock, error) {
	var l condaLock
	if err := yaml.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	if l.Version == 0 || l.Package == nil {
		return nil, errors.New("not a conda-lock.yml (no version and package)")
	}
	return &l, nil
}

// locked returns the version conda-lock.yml locks each package to, by manager ("conda",
// "pip") and name (lower-case; pip names PEP 503-normalized). A package locked to different
// versions on different platforms gets its lowest one, so every platform's update is proposed.
func (l *condaLock) locked() map[[2]string]string {
	out := map[[2]string]string{}
	for _, p := range l.Package {
		name := strings.ToLower(p.Name)
		if p.Manager == "pip" {
			name = pyreq.Normalize(p.Name)
		}
		key := [2]string{p.Manager, name}
		if cur, ok := out[key]; !ok || scheme.Compare(p.Version, cur) < 0 {
			out[key] = p.Version
		}
	}
	return out
}

// lockKey returns the key of a dep in condaLock.locked.
func lockKey(d dep.Dep) [2]string {
	if d.DepType == DepTypePip {
		return [2]string{"pip", pyreq.Normalize(d.Name)}
	}
	return [2]string{"conda", strings.ToLower(d.Name)}
}

// ReadLocks records the version the conda-lock.yml next to each environment file locks its
// packages to (dep.Dep.Locked), conda packages and pip requirements alike, so versions a spec
// admits (`numpy>=1.26`) become lockfile-only updates. An unreadable lockfile is reported once
// and its deps are bumped (LockUnread).
func (condaManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		pkgs map[[2]string]string
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
				var l *condaLock
				if err == nil {
					if l, err = parseLock(b); err == nil {
						st.pkgs = l.locked()
					}
				}
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					st.err = fmt.Errorf("%s: %w", file, err)
					errs = append(errs, st.err)
				}
				locks[file] = st
			}
			if st.err != nil {
				d.LockUnread = true
			} else if v, ok := st.pkgs[lockKey(*d)]; ok && scheme.Valid(v) {
				d.Locked = v
			}
		}
	}
	return errs
}

// LockRoots implements manager.LockMaintainer: the conda-lock.yml of each environment file
// with one.
func (condaManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if _, ok := (Locker{}).Scope(context.Background(), root, f); ok {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: lockPath(f)})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// Locker is the conda post-processor (engine.PostProcessor, UpdatePinner and LockMaintainer):
// it updates the conda-lock.yml next to a patched environment file with `conda-lock lock` in
// a temporary copy of the directory. The working tree is never touched.
type Locker struct{}

// Siblings lists conda-lock.yml.
func (Locker) Siblings() []string { return []string{lockFile} }

// Scope returns the directory of an environment file that has a conda-lock.yml next to it.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(lockPath(manifest))))
	return path.Dir(manifest), err == nil && st.Mode().IsRegular()
}

// Process relocks for the patched environment file.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates relocks with `conda-lock lock --update <name>…` for every updated package,
// manifest updates (ups) and lockfile-only ones (locks) alike: only those packages move (to the
// newest versions the specs admit, and their dependencies where needed), every other package
// stays at its locked version. Without updates (a patched manifest only) the environment is
// relocked whole. It returns the lockfile when it changed.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	var names []string
	for _, byFile := range []map[string][]dep.Update{ups, locks} {
		for _, rel := range slices.Sorted(maps.Keys(byFile)) {
			for _, u := range byFile[rel] {
				n := lockKey(u.Dep)[1]
				if !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
		}
	}
	return relock(ctx, dir, manifests, files, names, len(locks) > 0)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `conda-lock lock` in a copy
// of dir re-solves the environment, which moves every locked package to the newest version the
// specs admit; the environment file is unchanged. It returns the lockfile when it changed;
// without one it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	return relock(ctx, dir, nil, files, nil, false)
}

// relock runs `conda-lock lock -f <source>… --lockfile conda-lock.yml -p <platform>…
// [--update <name>…]` in a mirror of dir holding the patched manifests and the lockfile as
// copies, with the sources and platforms the lockfile records, and returns the lockfile when
// it changed.
func relock(ctx context.Context, dir string, manifests, files map[string][]byte, names []string, needLock bool) (map[string][]byte, error) {
	orig, ok := files[lockFile]
	if !ok {
		if needLock {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", lockFile, dir)
		}
		return nil, nil
	}
	l, err := parseLock(orig)
	if err != nil {
		return nil, fmt.Errorf("%s in %s: %w", lockFile, dir, err)
	}
	bin, err := exec.LookPath("conda-lock")
	if err != nil {
		return nil, fmt.Errorf("conda-lock not found (needed to update %s)", lockFile)
	}
	sources := l.Metadata.Sources
	if len(sources) == 0 {
		sources = slices.Sorted(maps.Keys(manifests))
	}
	args := []string{"lock"}
	for _, s := range sources {
		args = append(args, "-f", s)
	}
	args = append(args, "--lockfile", lockFile)
	for _, p := range l.Metadata.Platforms {
		args = append(args, "-p", p)
	}
	for _, n := range names {
		args = append(args, "--update", n)
	}
	copies := map[string][]byte{lockFile: orig}
	maps.Copy(copies, manifests)
	tmp, base, err := toolrun.Mirror(dir, toolrun.MirrorOptions{Pattern: "deps-conda-lock-*", Files: copies, Skip: toolrun.SkipGit})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := condaLockCmd(ctx, bin, base, dir, args); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil || bytes.Equal(out, orig) {
		return nil, err
	}
	return map[string][]byte{lockFile: out}, nil
}

// condaLockCmd runs `conda-lock args...` in dir (the mirror of scope); a failure carries the
// tail of its output, with the mirror's paths shown as scope's.
func condaLockCmd(ctx context.Context, bin, dir, scope string, args []string) error {
	cmd, cancel := toolrun.Command(ctx, bin, args...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env("NO_COLOR=1")
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "conda-lock command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("conda-lock lock in %s: %s", scope, msg)
}
