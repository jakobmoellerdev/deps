package pub

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

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/dart"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// scheme is the pub versioning scheme, which locked versions must be valid in.
var scheme, _ = versioning.Get(Versioning)

// lockFile is the lockfile of a package or workspace root; manifest the pubspec next to it.
const (
	lockFile = "pubspec.lock"
	manifest = "pubspec.yaml"
)

// pubspecHead is the part of a pubspec that decides where its lockfile is.
type pubspecHead struct {
	Resolution string   `yaml:"resolution"`
	Workspace  []string `yaml:"workspace"`
}

// readHead reads the pubspec at the absolute path p; ok is false when it cannot be read.
func readHead(p string) (h pubspecHead, ok bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return h, false
	}
	return h, yaml.Unmarshal(b, &h) == nil
}

// lockDir returns the directory (slash path relative to root) whose pubspec.lock locks the
// pubspec m: its own directory, or for a workspace member (`resolution: workspace`) the
// nearest ancestor whose pubspec.yaml lists a `workspace`. ok is false without such a lockfile.
func lockDir(root, m string) (string, bool) {
	dir := path.Dir(m)
	if h, _ := readHead(filepath.Join(root, filepath.FromSlash(m))); h.Resolution == "workspace" {
		found := false
		for dir != "." && !found {
			dir = path.Dir(dir)
			h, ok := readHead(filepath.Join(root, filepath.FromSlash(dir), manifest))
			found = ok && len(h.Workspace) > 0
		}
		if !found {
			return "", false
		}
	}
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), lockFile))
	return dir, err == nil && st.Mode().IsRegular()
}

// lockedPackage is a `packages` entry of pubspec.lock; other fields are skipped.
type lockedPackage struct {
	Source  string `yaml:"source"`
	Version string `yaml:"version"`
}

// lockContent is the part of pubspec.lock deps reads.
type lockContent struct {
	Packages map[string]lockedPackage `yaml:"packages"`
	SDKs     map[string]string        `yaml:"sdks"`
}

// parseLock parses pubspec.lock; a file without `sdks` is not a lockfile.
func parseLock(b []byte) (*lockContent, error) {
	var l lockContent
	if err := yaml.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	if l.SDKs == nil {
		return nil, errors.New("not a pubspec.lock (no sdks)")
	}
	return &l, nil
}

// ReadLocks records the version the pubspec.lock of each pubspec (lockDir) locks its hosted
// packages to (dep.Dep.Locked). A constraint without a lower bound (`<2.0.0`) takes the locked
// version as its Version, or is skipped without one. An unreadable lockfile is reported once
// and its deps are bumped (LockUnread).
func (pubManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		lock *lockContent
		err  error
	}
	locks := map[string]*lockState{}
	var errs []error
	for _, f := range files {
		file := ""
		for j := range f {
			d := &f[j]
			if d.Skip != "" || d.Range == "" || d.Datasource != dart.Name {
				continue
			}
			if file == "" {
				dir, ok := lockDir(root, d.File)
				file = "-"
				if ok {
					file = path.Join(dir, lockFile)
				}
			}
			st := locks[file]
			if st == nil {
				st = &lockState{}
				if file != "-" {
					b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
					if err == nil {
						st.lock, err = parseLock(b)
					}
					if err != nil {
						st.err = fmt.Errorf("%s: %w", file, err)
						errs = append(errs, st.err)
					}
				}
				locks[file] = st
			}
			switch {
			case st.err != nil:
				d.LockUnread = true
			case st.lock != nil:
				if p, ok := st.lock.Packages[d.Name]; ok && p.Source == "hosted" && scheme.Valid(p.Version) {
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

// LockRoots implements manager.LockMaintainer: the pubspec.lock of each package or workspace
// root, once.
func (pubManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	seen := map[string]bool{}
	for _, f := range files {
		dir, ok := lockDir(root, f)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, manager.LockRoot{Manifest: path.Join(dir, manifest), Lockfile: path.Join(dir, lockFile)})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// Locker is the pub post-processor (engine.PostProcessor, UpdatePinner and LockMaintainer): it
// updates the pubspec.lock of a patched pubspec with `dart pub` in a temporary copy of the
// package or workspace. The working tree is never touched.
type Locker struct{}

// Siblings lists pubspec.lock.
func (Locker) Siblings() []string { return []string{lockFile} }

// Scope returns the directory of the pubspec.lock that locks manifest (lockDir).
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	return lockDir(root, manifest)
}

// Process updates the lockfile for the patched pubspecs (`pub get`).
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates moves every updated package, manifest updates (ups) and lockfile-only ones
// (locks) alike, to exactly the update's version: in the copy, each updated hosted package's
// constraint is pinned to that version, then `pub upgrade <name>…` unlocks only those packages
// (their dependencies move where the new versions need it). Updates of the SDK constraints
// alone run `pub get`. The pinned pubspecs are not written back; it returns the lockfile when
// it changed.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	orig, ok := files[lockFile]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", lockFile, dir)
		}
		return nil, nil
	}
	copies, err := scopeManifests(dir, manifests)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, byManifest := range []map[string][]dep.Update{ups, locks} {
		for _, rel := range slices.Sorted(maps.Keys(byManifest)) {
			us := byManifest[rel]
			if src, ok := copies[rel]; ok {
				if copies[rel], err = pin(rel, src, us); err != nil {
					return nil, err
				}
			}
			for _, u := range us {
				if u.Dep.DepType != DepTypeEnvironment && !slices.Contains(names, u.Dep.Name) {
					names = append(names, u.Dep.Name)
				}
			}
		}
	}
	args := []string{"get"}
	if len(names) > 0 {
		args = append([]string{"upgrade"}, names...)
	}
	return run(ctx, dir, copies, orig, args)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `pub upgrade` in a copy of
// dir moves every locked package to the newest version the constraints admit; the pubspecs are
// unchanged. It returns the lockfile when it changed; without one it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	copies, err := scopeManifests(dir, nil)
	if err != nil {
		return nil, err
	}
	return run(ctx, dir, copies, lock, []string{"upgrade"})
}

// scopeManifests returns the pubspecs of the scope dir by slash path relative to it: its own
// pubspec.yaml and those of its `workspace` members (recursively), from patched when there,
// else from disk. Each is written into the copy, so pub never writes through a symlink into a
// member's directory.
func scopeManifests(dir string, patched map[string][]byte) (map[string][]byte, error) {
	out := maps.Clone(patched)
	if out == nil {
		out = map[string][]byte{}
	}
	var walk func(rel string) error
	walk = func(rel string) error {
		if _, ok := out[rel]; !ok {
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			out[rel] = b
		}
		var h pubspecHead
		_ = yaml.Unmarshal(out[rel], &h) // a pubspec pub cannot read lists no members; pub reports it
		for _, w := range h.Workspace {
			ms, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(path.Join(path.Dir(rel), w)), manifest))
			for _, m := range ms {
				r, err := filepath.Rel(dir, m)
				if err != nil {
					return err
				}
				if err := walk(filepath.ToSlash(r)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return out, walk(manifest)
}

// pin returns the pubspec src (rel) with the constraint of every updated hosted package of ups
// set to exactly its new version.
func pin(rel string, src []byte, ups []dep.Update) ([]byte, error) {
	ds, err := pubManager{}.Extract(rel, src, manager.Env{})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	var edits []patch.Edit
	for _, u := range ups {
		if u.Dep.Datasource != dart.Name {
			continue
		}
		for _, d := range ds {
			if d.Name == u.Dep.Name && d.DepType == u.Dep.DepType && d.Range != "" {
				edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: u.NewVersion})
			}
		}
	}
	slices.SortFunc(edits, func(a, b patch.Edit) int { return b.Start - a.Start })
	out := slices.Clone(src)
	for _, e := range edits {
		out = slices.Concat(out[:e.Start], []byte(e.New), out[e.End:])
	}
	return out, nil
}

// run runs `pub <args>` in a mirror of dir holding the pubspecs copies and lock as
// pubspec.lock (mirrorDir) and returns pubspec.lock when it changed.
func run(ctx context.Context, dir string, copies map[string][]byte, lock []byte, args []string) (map[string][]byte, error) {
	all := maps.Clone(copies)
	all[lockFile] = lock
	tmp, base, err := mirrorDir(dir, all)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := runPub(ctx, base, dir, usesFlutterSDK(lock, copies), append(args, "--no-precompile", "--no-example")...); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{lockFile: out}, nil
}

// usesFlutterSDK reports whether the scope needs the Flutter SDK: its lock constrains Flutter
// (`sdks: flutter`) or a pubspec depends on it.
func usesFlutterSDK(lock []byte, pubspecs map[string][]byte) bool {
	if l, err := parseLock(lock); err == nil && l.SDKs["flutter"] != "" {
		return true
	}
	for _, src := range pubspecs {
		var doc yaml.Node
		if yaml.Unmarshal(src, &doc) == nil && len(doc.Content) > 0 && usesFlutter(doc.Content[0]) {
			return true
		}
	}
	return false
}

// skipEntries are not mirrored: pub's own state, build output and the repository metadata.
var skipEntries = map[string]bool{".dart_tool": true, "build": true, ".git": true}

// mirrorDir builds a temporary tree (toolrun.Mirror) in which the files (slash paths relative
// to dir) are written as copies, the directories leading to them recreated, and every other
// entry of those directories but skipEntries symlinked, so path dependencies resolve as in
// the checkout. The caller removes tmp; base is the mirror of dir.
func mirrorDir(dir string, files map[string][]byte) (tmp, base string, err error) {
	return toolrun.Mirror(dir, toolrun.MirrorOptions{
		Pattern: "deps-pub-*",
		Files:   files,
		Skip:    func(_ string, d fs.DirEntry) bool { return skipEntries[d.Name()] },
	})
}

// runPub runs `dart pub args...` in dir (the mirror of scope). For a Flutter scope it runs the
// Dart SDK of the `flutter` on PATH with FLUTTER_ROOT set, which resolves the Flutter SDK
// packages as `flutter pub` does without regenerating platform files; a failure carries the
// tail of pub's output, with the mirror's paths shown as scope's.
func runPub(ctx context.Context, dir, scope string, flutter bool, args ...string) error {
	bin, env, err := dartTool(flutter)
	if err != nil {
		return err
	}
	cmd, cancel := toolrun.Command(ctx, bin, append([]string{"--suppress-analytics", "pub"}, args...)...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env(env...)
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "pub command", "bin", bin, "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("dart pub %s in %s: %s", args[0], scope, msg)
}

// dartTool returns the dart binary and its extra environment: `dart` from PATH, or for a
// Flutter scope the Dart SDK bundled with the `flutter` on PATH (bin/cache/dart-sdk, else the
// bin/dart wrapper) with FLUTTER_ROOT.
func dartTool(flutter bool) (string, []string, error) {
	if !flutter {
		bin, err := exec.LookPath("dart")
		if err != nil {
			return "", nil, fmt.Errorf("dart not found (needed to update %s)", lockFile)
		}
		return bin, nil, nil
	}
	bin, err := exec.LookPath("flutter")
	if err == nil {
		bin, err = filepath.EvalSymlinks(bin)
	}
	if err != nil {
		return "", nil, fmt.Errorf("flutter not found (needed to update %s of a Flutter package)", lockFile)
	}
	root := filepath.Dir(filepath.Dir(bin))
	dartBin := filepath.Join(root, "bin", "cache", "dart-sdk", "bin", "dart")
	if _, err := os.Stat(dartBin); err != nil {
		dartBin = filepath.Join(root, "bin", "dart")
	}
	return dartBin, []string{"FLUTTER_ROOT=" + root}, nil
}
