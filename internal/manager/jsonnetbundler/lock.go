package jsonnetbundler

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// vendorDir is jb's default package directory (--jsonnetpkg-home), regenerated when the
// checkout has one.
const vendorDir = "vendor"

// Locker is the jsonnet-bundler post-processor (engine.PostProcessor, UpdatePinner,
// TreeRegenerator and LockMaintainer): it updates the jsonnetfile.lock.json of a
// jsonnetfile.json with `jb update <uri>…` in a temporary mirror of its directory, and the
// vendor directory next to it when the checkout has one. The working tree is never touched.
type Locker struct{}

// Siblings lists jsonnetfile.lock.json.
func (Locker) Siblings() []string { return []string{lockFile} }

// Scope returns the directory of a jsonnetfile.json that has a jsonnetfile.lock.json next to
// it.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	if path.Base(manifest) != manifestName {
		return "", false
	}
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(path.Join(path.Dir(manifest), lockFile))))
	return path.Dir(manifest), err == nil && st.Mode().IsRegular()
}

// Trees implements engine.TreeRegenerator: the vendor directory of dir when there is one (a
// committed vendor directory, as Renovate commits every file jb changed).
func (Locker) Trees(dir string) []string {
	if st, err := os.Stat(filepath.Join(dir, vendorDir)); err == nil && st.IsDir() {
		return []string{vendorDir}
	}
	return nil
}

// Process brings the lock in line with the patched jsonnetfile.json (`jb install`).
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates re-locks every updated dependency (manifest updates ups and lockfile-only
// ones locks alike) with `jb update <uri>…`, the uri being the remote as jsonnetfile.json
// writes it joined with the subdir, as Renovate does: a branch moves to its newest commit, a
// tag or commit jsonnetfile.json now names is locked. Without updates `jb install` brings the
// lock in line with jsonnetfile.json. It returns jsonnetfile.lock.json when it changed and
// the changed files of the vendor directory (Trees); without a lock it does nothing.
func (l Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	src, ok := manifests[manifestName]
	if !ok {
		var err error
		if src, err = os.ReadFile(filepath.Join(dir, manifestName)); err != nil {
			return nil, err
		}
	}
	uris, err := uriByName(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", manifestName, err)
	}
	var args []string
	for _, u := range slices.Concat(ups[manifestName], locks[manifestName]) {
		if uri, ok := uris[u.Dep.Name]; ok && !slices.Contains(args, uri) {
			args = append(args, uri)
		}
	}
	if len(args) == 0 {
		return l.run(ctx, dir, src, lock, "install", nil)
	}
	return l.run(ctx, dir, src, lock, "update", args)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `jb update` in a mirror of
// dir moves every dependency, transitive ones included, to the newest commit its version
// admits; jsonnetfile.json is unchanged. It returns jsonnetfile.lock.json when it changed (and
// the vendor files); without a lock it does nothing.
func (l Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	return l.run(ctx, dir, src, lock, "update", nil)
}

// uriByName returns the `jb update` argument of each git dependency of the jsonnetfile.json
// src by dep name: the remote as written, joined with the subdir (Renovate's dependencyUrl).
func uriByName(src []byte) (map[string]string, error) {
	var f struct {
		Dependencies []lockEntry `json:"dependencies"`
	}
	if err := json.Unmarshal(src, &f); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(f.Dependencies))
	for _, e := range f.Dependencies {
		if g := e.Source.Git; g != nil {
			if name, _ := depName(g.Remote, g.Subdir); name != "" {
				out[name] = strings.TrimSuffix(g.Remote+"/"+strings.Trim(g.Subdir, "/"), "/")
			}
		}
	}
	return out, nil
}

// run runs `jb <sub> [uris…]` in a mirror of dir holding src as jsonnetfile.json and lock as
// jsonnetfile.lock.json (mirrorDir) and returns the lock when it changed, with the files of
// the vendor directory jb wrote that differ from the checkout's when dir has one.
func (Locker) run(ctx context.Context, dir string, src, lock []byte, sub string, uris []string) (map[string][]byte, error) {
	tmp, base, err := mirrorDir(dir, map[string][]byte{manifestName: src, lockFile: lock})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := jb(ctx, base, dir, append([]string{sub}, uris...)...); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	b, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(b, lock) {
		out[lockFile] = b
	}
	if len((Locker{}).Trees(dir)) > 0 {
		if err := toolrun.DiffTree(filepath.Join(dir, vendorDir), filepath.Join(base, vendorDir), vendorDir, out); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// mirrorDir builds a temporary tree (toolrun.Mirror) in which the files of dir in copies
// (jsonnetfile.json and jsonnetfile.lock.json) are written and every other entry of dir but
// .git and vendor is symlinked, so local dependencies (`source.local.directory`) resolve as
// in the checkout; jb writes a fresh vendor directory. The caller removes tmp; base is the
// mirror of dir.
func mirrorDir(dir string, copies map[string][]byte) (tmp, base string, err error) {
	return toolrun.Mirror(dir, toolrun.MirrorOptions{
		Pattern: "deps-jb-*",
		Files:   copies,
		Skip:    func(rel string, _ fs.DirEntry) bool { return rel == ".git" || rel == vendorDir },
	})
}

// jb runs `jb args...` in dir (the mirror of scope); a failure carries the tail of jb's
// output, with the mirror's paths shown as scope's.
func jb(ctx context.Context, dir, scope string, args ...string) error {
	bin, err := exec.LookPath("jb")
	if err != nil {
		return fmt.Errorf("jb not found (needed to update %s)", lockFile)
	}
	cmd, cancel := toolrun.Command(ctx, bin, args...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env()
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "jb command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("jb %s in %s: %s", args[0], scope, msg)
}
