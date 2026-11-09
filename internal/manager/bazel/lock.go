package bazel

import (
	"bytes"
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

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// lockFile is the module lockfile next to the root MODULE.bazel.
const lockFile = "MODULE.bazel.lock"

// Locker is the bazel-module and bazelisk post-processor (engine.PostProcessor and
// LockMaintainer): it updates the MODULE.bazel.lock of the workspace of a patched module file
// or `.bazelversion` with `bazel mod deps --lockfile_mode=update` in a temporary mirror of the
// workspace, as Renovate does for module files. The working tree is never touched. Without a
// MODULE.bazel.lock there is nothing to do.
type Locker struct{}

// Siblings lists MODULE.bazel.lock.
func (Locker) Siblings() []string { return []string{lockFile} }

// SharedScope implements engine.SharedScope: module files and the `.bazelversion` of one
// workspace regenerate the same MODULE.bazel.lock, together.
func (Locker) SharedScope() {}

// Scope returns the workspace of a module file (the nearest directory from its own up to root
// holding a MODULE.bazel), or of a `.bazelversion` (its own directory, which Bazelisk reads it
// from, when it holds a MODULE.bazel), when the workspace has a MODULE.bazel.lock.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	var ws string
	if path.Base(manifest) == bazelVersionFile {
		ws = path.Dir(manifest)
		if !regular(filepath.Join(root, filepath.FromSlash(ws), moduleFile)) {
			return "", false
		}
	} else {
		var ok bool
		if ws, ok = workspaceDir(root, manifest); !ok {
			return "", false
		}
	}
	return ws, regular(filepath.Join(root, filepath.FromSlash(ws), lockFile))
}

// regular reports whether p is a regular file.
func regular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// workspaceDir returns the slash directory (relative to root) of the nearest MODULE.bazel from
// the directory of the slash path file upwards.
func workspaceDir(root, file string) (string, bool) {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), moduleFile)); err == nil && st.Mode().IsRegular() {
			return dir, true
		}
		if dir == "." || dir == "/" {
			return "", false
		}
	}
}

// LockRoots implements manager.LockMaintainer: the MODULE.bazel.lock of each workspace of
// files, once.
func (moduleManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if ws, ok := (Locker{}).Scope(context.Background(), root, f); ok {
			out = append(out, manager.LockRoot{Manifest: path.Join(ws, moduleFile), Lockfile: path.Join(ws, lockFile)})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return slices.Compact(out)
}

// Process re-resolves the module graph of the patched module files (manifests, by slash path
// relative to dir) and returns MODULE.bazel.lock when it changed.
func (Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	copies := maps.Clone(manifests)
	if copies == nil {
		copies = map[string][]byte{}
	}
	copies[lockFile] = lock
	return regenerate(ctx, dir, copies, lock)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): MODULE.bazel.lock is
// deleted and regenerated from scratch, as Renovate does; the module files are unchanged.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	return regenerate(ctx, dir, map[string][]byte{}, lock)
}

// regenerate runs `bazel mod deps --lockfile_mode=update` in a mirror of the workspace dir with
// copies written over it (a MODULE.bazel.lock not among them is absent) and returns the new
// lockfile when it differs from lock. A patched `.bazelversion` among copies runs Bazelisk
// first, so the lockfile is written by the new Bazel version.
func regenerate(ctx context.Context, dir string, copies map[string][]byte, lock []byte) (map[string][]byte, error) {
	bin, err := bazelBinary(copies[bazelVersionFile] != nil)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "deps-bazel-*")
	if err != nil {
		return nil, err
	}
	defer removeAll(tmp)
	mirror := filepath.Join(tmp, "src")
	if err := os.Mkdir(mirror, 0o755); err != nil {
		return nil, err
	}
	base, err := toolrun.MirrorAncestors(dir, mirror)
	if err != nil {
		return nil, err
	}
	if err := link(dir, base, ""); err != nil {
		return nil, err
	}
	if _, ok := copies[lockFile]; !ok {
		if err := os.Remove(filepath.Join(base, lockFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	for rel, b := range copies {
		if err := materialize(dir, base, rel); err != nil {
			return nil, err
		}
		if err := toolrun.WriteFiles(base, map[string][]byte{rel: b}); err != nil {
			return nil, err
		}
	}
	outBase := filepath.Join(tmp, "output")
	runErr := runBazel(ctx, bin, base, dir, outBase, "mod", "deps", "--lockfile_mode=update")
	_ = runBazel(context.WithoutCancel(ctx), bin, base, dir, outBase, "shutdown") // the server would idle for hours
	if runErr != nil {
		return nil, runErr
	}
	out, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{lockFile: out}, nil
}

// link symlinks every entry of the directory src/rel into dst/rel except .git and Bazel's
// convenience symlinks (`bazel-out`, `bazel-<workspace>`), which Bazel recreates.
func link(src, dst, rel string) error {
	entries, err := os.ReadDir(filepath.Join(src, rel))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" || strings.HasPrefix(e.Name(), "bazel-") && e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if err := os.Symlink(filepath.Join(src, rel, e.Name()), filepath.Join(dst, rel, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// materialize turns the parent directories of the slash path rel in the mirror dst, symlinks
// into src after link, into real directories (with their entries symlinked) and removes a
// symlink at rel itself, so writing rel never writes through to the working tree.
func materialize(src, dst, rel string) error {
	parts := strings.Split(rel, "/")
	cur := ""
	for _, p := range parts[:len(parts)-1] {
		cur = path.Join(cur, p)
		target := filepath.Join(dst, filepath.FromSlash(cur))
		st, err := os.Lstat(target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(target, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case st.Mode()&fs.ModeSymlink != 0:
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := os.Mkdir(target, 0o755); err != nil {
				return err
			}
			if err := link(src, dst, filepath.FromSlash(cur)); err != nil {
				return err
			}
		}
	}
	err := os.Remove(filepath.Join(dst, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// bazelBinary returns the path of `bazel`, else `bazelisk` (Bazelisk picks the Bazel version
// of .bazelversion); with versionChanged (an updated .bazelversion) `bazelisk` comes first, as a
// plain Bazel binary would ignore the new version (a `bazel` that is Bazelisk, as npm's
// @bazel/bazelisk and most CI images install it, honors it too).
func bazelBinary(versionChanged bool) (string, error) {
	names := []string{"bazel", "bazelisk"}
	if versionChanged {
		names[0], names[1] = names[1], names[0]
	}
	for _, n := range names {
		if bin, err := exec.LookPath(n); err == nil {
			return bin, nil
		}
	}
	return "", errors.New("bazel not found (needed to update " + lockFile + ")")
}

// runBazel runs `<bin> --output_base=<outBase> args...` in dir, the mirror of scope; a
// failure carries the tail of Bazel's output with the mirror's paths shown as scope's.
func runBazel(ctx context.Context, bin, dir, scope, outBase string, args ...string) error {
	cmd, cancel := toolrun.Command(ctx, bin, append([]string{"--output_base=" + outBase}, args...)...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env()
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "bazel command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("bazel %s in %s: %s", strings.Join(args, " "), scope, msg)
}

// removeAll removes the temporary tree, making read-only directories Bazel leaves in its
// output base writable first.
func removeAll(dir string) {
	if os.RemoveAll(dir) == nil {
		return
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}
