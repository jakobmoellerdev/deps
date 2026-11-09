package nuget

import (
	"bytes"
	"cmp"
	"context"
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

// lockFile is the NuGet lockfile of a project (RestorePackagesWithLockFile), next to it.
const lockFile = "packages.lock.json"

// isProject reports whether the slash path is an MSBuild project (`.csproj`, `.fsproj`,
// `.vbproj`).
func isProject(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".csproj", ".fsproj", ".vbproj":
		return true
	}
	return false
}

// isMSBuild reports whether the slash path is an MSBuild file the manager scans (a project or
// an import), the files whose changes can move a packages.lock.json.
func isMSBuild(p string) bool {
	base := strings.ToLower(path.Base(p))
	return isProject(p) || (path.Ext(base) == ".props" || path.Ext(base) == ".targets") &&
		!strings.HasSuffix(base, ".nuget.g.props") && !strings.HasSuffix(base, ".nuget.g.targets")
}

// skipDir reports whether the walk of a repository (and its mirror) leaves the directory
// name out: version control, build output and package folders hold no projects deps updates.
func skipDir(name string) bool {
	switch name {
	case ".git", "bin", "obj", "node_modules":
		return true
	}
	return false
}

// locks returns the slash paths, relative to dir, of every packages.lock.json below dir that
// sits next to a project, sorted.
func locks(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil //nolint:nilerr // unreadable parts of the tree are not scanned
		case d.IsDir():
			if p != dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		case d.Name() != lockFile || !d.Type().IsRegular():
			return nil
		}
		if len(projects(filepath.Dir(p))) == 0 {
			return nil
		}
		if rel, err := filepath.Rel(dir, p); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// projects returns the names of the MSBuild projects in the directory dir, sorted.
func projects(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if !e.IsDir() && isProject(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// LockRoots implements manager.LockMaintainer: the packages.lock.json next to each project.
func (*nugetManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if !isProject(f) {
			continue
		}
		l := path.Join(path.Dir(f), lockFile)
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(l))); err == nil && st.Mode().IsRegular() &&
			!slices.ContainsFunc(out, func(r manager.LockRoot) bool { return r.Lockfile == l }) {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: l})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// Locker is the nuget post-processor (engine.PostProcessor, ScopeSiblingLister and
// LockMaintainer): it refreshes the packages.lock.json files of the repository with `dotnet
// restore --use-lock-file` in a temporary copy of it. A version an MSBuild file sets can reach
// any project below it (Directory.Packages.props, Directory.Build.props, imports), so the
// scope is the repository and every lockfile in it is restored. The working tree is never
// touched.
type Locker struct{}

// Scope returns the repository (".") for an MSBuild file when it holds a packages.lock.json
// next to a project; global.json, dotnet-tools.json and nuget.config have no lockfile.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	return ".", isMSBuild(manifest) && len(locks(root)) > 0
}

// Siblings is unused: ScopeSiblings lists the lockfiles.
func (Locker) Siblings() []string { return nil }

// ScopeSiblings lists every packages.lock.json below dir next to a project.
func (Locker) ScopeSiblings(dir string) []string { return locks(dir) }

// Process restores every project with a packages.lock.json against the patched MSBuild files:
// NuGet re-resolves a project whose package references no longer match its lockfile, and
// keeps the others. It returns the lockfiles that changed.
func (Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return restore(ctx, dir, manifests, files, false)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `dotnet restore
// --use-lock-file --force-evaluate` re-resolves every project with a packages.lock.json, which
// moves floating versions and ranges to the newest versions they admit. It returns the
// lockfiles that changed.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	return restore(ctx, dir, nil, files, true)
}

// restore runs `dotnet restore <project> --use-lock-file` (--force-evaluate when force) for
// each project next to one of the lockfiles in files, in a mirror of dir holding the patched
// manifests and the lockfiles as copies. Every directory of the mirror is recreated, its files
// symlinked, so the obj and bin directories restore writes stay in the mirror.
func restore(ctx context.Context, dir string, manifests, files map[string][]byte, force bool) (map[string][]byte, error) {
	if len(files) == 0 {
		return nil, nil
	}
	bin, err := exec.LookPath("dotnet")
	if err != nil {
		return nil, fmt.Errorf("dotnet not found (needed to update %s)", lockFile)
	}
	copies := make(map[string][]byte, len(manifests)+len(files))
	maps.Copy(copies, files)
	maps.Copy(copies, manifests)
	tmp, base, err := toolrun.Mirror(dir, toolrun.MirrorOptions{
		Pattern:  "deps-nuget-*",
		Files:    copies,
		Skip:     func(rel string, d fs.DirEntry) bool { return d.IsDir() && skipDir(d.Name()) },
		Recreate: func(string, fs.DirEntry) bool { return true },
	})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	args := []string{"--use-lock-file", "-p:RestoreLockedMode=false"}
	if force {
		args = append(args, "--force-evaluate")
	}
	names := slices.Sorted(maps.Keys(files))
	for _, l := range names {
		for _, p := range projects(filepath.Join(base, filepath.FromSlash(path.Dir(l)))) {
			if err := dotnet(ctx, bin, base, dir, slices.Concat([]string{"restore", filepath.Join(filepath.FromSlash(path.Dir(l)), p)}, args)); err != nil {
				return nil, err
			}
		}
	}
	out := map[string][]byte{}
	for _, l := range names {
		b, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(l)))
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(b, files[l]) {
			out[l] = b
		}
	}
	return out, nil
}

// dotnet runs `dotnet args...` in dir (the mirror of scope); a failure carries the tail of its
// output, with the mirror's paths shown as scope's.
func dotnet(ctx context.Context, bin, dir, scope string, args []string) error {
	cmd, cancel := toolrun.Command(ctx, bin, args...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env("DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1", "NUGET_XMLDOC_MODE=skip")
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "dotnet command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("dotnet restore in %s: %s", scope, msg)
}
