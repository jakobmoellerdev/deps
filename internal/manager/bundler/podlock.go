package bundler

import (
	"cmp"
	"context"
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

// PodLocker is the cocoapods post-processor (engine.PostProcessor, LockfileUpdater and
// LockMaintainer): it updates the Podfile.lock of a patched Podfile with CocoaPods in a
// temporary copy of its directory, as Renovate does. The working tree is never touched.
type PodLocker struct{}

// Siblings lists Podfile.lock.
func (PodLocker) Siblings() []string { return []string{podfileLock} }

// Scope returns the directory of a Podfile with a Podfile.lock next to it.
func (PodLocker) Scope(_ context.Context, root, manifest string) (string, bool) {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(podLockPath(manifest))))
	return path.Dir(manifest), err == nil && st.Mode().IsRegular()
}

// Process re-resolves the Podfile.lock for the patched Podfile with `pod install`, which
// resolves the pods whose requirements changed anew and keeps the others locked.
func (l PodLocker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessLocked(ctx, dir, manifests, files, nil)
}

// ProcessLocked is Process plus lockfile-only updates: `pod update <pods…>` unlocks the pods
// of locks (a subspec's update its pod), which move to the newest version their requirements
// admit, and installs the patched Podfile.
func (PodLocker) ProcessLocked(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
	lock, ok := files[podfileLock]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", podfileLock, dir)
		}
		return nil, nil
	}
	args := []string{"install"}
	for _, u := range flatten(locks) {
		if name := podName(u.Dep.Name); !slices.Contains(args[1:], name) {
			args = append(args, name)
		}
	}
	if len(args) > 1 {
		args[0] = "update"
		slices.Sort(args[1:])
	}
	return runPod(ctx, dir, manifests[podfileName], lock, args)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `pod update` in a copy of
// dir moves every pod to the newest version the Podfile admits; the Podfile is unchanged. It
// returns the lockfile when it changed; without one it does nothing.
func (PodLocker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[podfileLock]
	if !ok {
		return nil, nil
	}
	return runPod(ctx, dir, nil, lock, []string{"update"})
}

// runPod runs `pod args…` in a mirror of dir holding podfile (nil = the checked-out Podfile)
// and lock as Podfile.lock (podMirror) and returns Podfile.lock when it changed.
func runPod(ctx context.Context, dir string, podfile, lock []byte, args []string) (map[string][]byte, error) {
	if podfile == nil {
		var err error
		if podfile, err = os.ReadFile(filepath.Join(dir, podfileName)); err != nil {
			return nil, err
		}
	}
	tmp, base, err := podMirror(dir, map[string][]byte{podfileName: podfile, podfileLock: lock})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := pod(ctx, base, dir, args...); err != nil {
		return nil, err
	}
	return changedLock(base, podfileLock, lock)
}

// podSkip are the entries of the Podfile's directory left out of the mirror: the installed pods
// (CocoaPods installs them anew in the mirror instead of writing through a symlink), build
// output and the repository metadata.
var podSkip = map[string]bool{"Pods": true, "build": true, ".git": true}

// podMirror builds a temporary tree (toolrun.Mirror) of dir in which the files (Podfile,
// Podfile.lock) and every file below an Xcode project or workspace (`*.xcodeproj`,
// `*.xcworkspace`, which `pod install` integrates the pods into) are copies, and every other
// entry but podSkip is symlinked, so `:path` pods and the scripts a Podfile requires resolve as
// in the checkout. The caller removes tmp; base is the mirror of dir.
func podMirror(dir string, files map[string][]byte) (tmp, base string, err error) {
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir() && (podSkip[d.Name()] && !strings.Contains(rel, "/") || d.Name() == "node_modules"):
			return filepath.SkipDir
		case d.Type().IsRegular() && xcodeBundle(rel):
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[rel] = b
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return toolrun.Mirror(dir, toolrun.MirrorOptions{
		Pattern: "deps-cocoapods-*",
		Files:   files,
		Skip:    func(rel string, _ fs.DirEntry) bool { return podSkip[rel] },
	})
}

// xcodeBundle reports whether the slash path is below an Xcode project or workspace.
func xcodeBundle(rel string) bool {
	for seg := range strings.SplitSeq(path.Dir(rel), "/") {
		if strings.HasSuffix(seg, ".xcodeproj") || strings.HasSuffix(seg, ".xcworkspace") {
			return true
		}
	}
	return false
}

// pod runs `pod args… --no-ansi` in dir (the mirror of scope), with `--allow-root` when deps
// runs as root (CI containers), which CocoaPods refuses otherwise; a failure carries the tail
// of CocoaPods' output, with the mirror's paths shown as scope's.
func pod(ctx context.Context, dir, scope string, args ...string) error {
	bin, err := exec.LookPath("pod")
	if err != nil {
		return fmt.Errorf("pod not found (needed to update %s)", podfileLock)
	}
	full := append(slices.Clone(args), "--no-ansi")
	if os.Geteuid() == 0 {
		full = append(full, "--allow-root")
	}
	cmd, cancel := toolrun.Command(ctx, bin, full...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env()
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "pod command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("pod %s in %s: %s", args[0], scope, msg)
}
