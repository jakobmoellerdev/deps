package paket

import (
	"bytes"
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

// Locker is the paket post-processor (engine.PostProcessor, UpdatePinner and LockMaintainer):
// it updates the paket.lock of a patched paket.dependencies with `paket update --no-install` in
// a temporary mirror of its directory. The working tree is never touched.
type Locker struct{}

// Siblings lists paket.lock.
func (Locker) Siblings() []string { return []string{lockFile} }

// Scope returns the directory of a paket.dependencies that has a paket.lock next to it.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(lockPath(manifest))))
	return path.Dir(manifest), err == nil && st.Mode().IsRegular()
}

// Process has nothing to do without updates: Paket keeps a lock the dependencies still admit.
func (Locker) Process(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
	return nil, nil
}

// ProcessUpdates moves every updated package, manifest updates (ups) and lockfile-only ones
// (locks) alike, to exactly the update's version, as Renovate does: one `paket update
// --no-install --group <group> --version <version> <id>` per package in a mirror holding the
// patched paket.dependencies, which moves the package's own dependencies only where the new
// version needs it. Only paket.lock is read back (`--version` may rewrite the mirror's
// paket.dependencies). It returns paket.lock when it changed.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	updates := slices.Concat(ups[manifestFile], locks[manifestFile])
	if len(updates) == 0 {
		return nil, nil
	}
	lock, ok := files[lockFile]
	if !ok {
		return nil, fmt.Errorf("updates need %s in %s", lockFile, dir)
	}
	src, ok := manifests[manifestFile]
	if !ok {
		var err error
		if src, err = os.ReadFile(filepath.Join(dir, manifestFile)); err != nil {
			return nil, err
		}
	}
	groups := map[int]string{}
	for _, e := range parseDependencies(src).entries {
		groups[e.line] = e.group
	}
	var cmds [][]string
	seen := map[string]bool{}
	for _, u := range updates {
		g, ok := groups[u.Dep.Line]
		if !ok || !nugetScheme.Valid(u.NewVersion) {
			return nil, fmt.Errorf("%s: no nuget line of %s at line %d", manifestFile, u.Dep.Name, u.Dep.Line)
		}
		if key := strings.ToLower(g + "\x00" + u.Dep.Name); !seen[key] {
			seen[key] = true
			cmds = append(cmds, []string{"update", "--no-install", "--group", g, "--version", u.NewVersion, u.Dep.Name})
		}
	}
	return run(ctx, dir, src, lock, cmds)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `paket update --no-install`
// moves every package to the newest version the constraints admit; paket.dependencies is
// unchanged. It returns paket.lock when it changed; without one it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return nil, err
	}
	return run(ctx, dir, src, lock, [][]string{{"update", "--no-install"}})
}

// skipDirs are left out of the mirror: Paket's own directory (bootstrapper, restore targets),
// the packages it installs and its file dependencies, so nothing it writes reaches the
// checkout through a symlink.
var skipDirs = []string{".git", ".paket", "packages", "paket-files", "node_modules"}

// run runs each `paket <args>` in a mirror of dir holding src as paket.dependencies and lock as
// paket.lock, and returns paket.lock when it changed. The other entries of dir are symlinked
// whole (toolrun.Mirror's default), skipDirs left out: `paket update --no-install` writes
// paket.dependencies and paket.lock, which the mirror holds as real files, so recreating the
// directories of the scope to symlink the checkout's files one by one only cost a walk of the
// whole tree per scope.
func run(ctx context.Context, dir string, src, lock []byte, cmds [][]string) (map[string][]byte, error) {
	bin, err := exec.LookPath("paket")
	if err != nil {
		return nil, fmt.Errorf("paket not found (needed to update %s)", lockFile)
	}
	tmp, base, err := toolrun.Mirror(dir, toolrun.MirrorOptions{
		Pattern: "deps-paket-*",
		Files:   map[string][]byte{manifestFile: src, lockFile: lock},
		Skip:    func(rel string, d fs.DirEntry) bool { return d.IsDir() && slices.Contains(skipDirs, rel) },
	})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	for _, args := range cmds {
		if err := paket(ctx, bin, base, dir, args); err != nil {
			return nil, err
		}
	}
	out, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{lockFile: out}, nil
}

// paket runs `paket args...` in dir (the mirror of scope); a failure carries the tail of its
// output, with the mirror's paths shown as scope's.
func paket(ctx context.Context, bin, dir, scope string, args []string) error {
	cmd, cancel := toolrun.Command(ctx, bin, args...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env("NO_COLOR=1", "DOTNET_CLI_TELEMETRY_OPTOUT=1")
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "paket command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("paket update in %s: %s", scope, msg)
}
