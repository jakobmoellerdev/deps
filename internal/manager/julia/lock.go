package julia

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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// Manifest names Pkg reads next to a project, unversioned ones first.
const (
	juliaManifest = "JuliaManifest.toml"
	manifestFile  = "Manifest.toml"
)

// versionedRe matches the manifests of one Julia release (`Manifest-v1.11.toml`), which Pkg
// prefers when it runs that release.
var versionedRe = regexp.MustCompile(`^(Julia)?Manifest-v(\d+)\.(\d+)\.toml$`)

// manifestName returns the manifest of the absolute directory dir: JuliaManifest.toml or
// Manifest.toml, else the versioned manifest of the newest Julia release; "" when there is
// none. Other versioned manifests are neither read nor updated.
func manifestName(dir string) string {
	for _, n := range []string{juliaManifest, manifestFile} {
		if regular(filepath.Join(dir, n)) {
			return n
		}
	}
	entries, _ := os.ReadDir(dir)
	best, bestV := "", [2]int{-1, -1}
	for _, e := range entries {
		m := versionedRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		major, err1 := strconv.Atoi(m[2])
		minor, err2 := strconv.Atoi(m[3])
		v := [2]int{major, minor}
		if err1 != nil || err2 != nil {
			continue
		}
		if slices.Compare(v[:], bestV[:]) > 0 {
			best, bestV = e.Name(), v
		}
	}
	return best
}

func regular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// projectIn returns the project file name of the absolute directory dir (JuliaProject.toml
// over Project.toml), "" when it has none.
func projectIn(dir string) string {
	for _, n := range ProjectNames {
		if regular(filepath.Join(dir, n)) {
			return n
		}
	}
	return ""
}

// workspaceRoot returns the slash path of the project whose [workspace] lists the directory
// dir (relative to the absolute root), searching dir's ancestors up to root as Pkg does; ""
// when dir is in no workspace.
func workspaceRoot(root, dir string) string {
	for a := dir; a != "." && a != "/"; {
		a = path.Dir(a)
		name := projectIn(filepath.Join(root, filepath.FromSlash(a)))
		if name == "" {
			continue
		}
		p := path.Join(a, name)
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		t, err := tomlspan.Parse(src)
		if err != nil {
			continue
		}
		for _, s := range t.Get("workspace", "projects").Strings() {
			if path.Join(a, s.Str) == dir {
				return p
			}
		}
	}
	return ""
}

// lockPath returns the slash path of the manifest of the project file project (relative to
// the absolute root): the one next to the root project of its workspace, else next to
// project; "" when there is none.
func lockPath(root, project string) string {
	for range 16 { // nested workspaces
		ws := workspaceRoot(root, path.Dir(project))
		if ws == "" {
			break
		}
		project = ws
	}
	dir := path.Dir(project)
	name := manifestName(filepath.Join(root, filepath.FromSlash(dir)))
	if name == "" {
		return ""
	}
	return path.Join(dir, name)
}

// lockedPackages returns the version a manifest (format 1 or 2) locks each registry package
// to, by UUID. Packages tracking a path or a git repository (`path`, `repo-url`) and
// standard libraries (no version) are left out.
func lockedPackages(b []byte) (map[string]string, error) {
	root, err := tomlspan.Parse(b)
	if err != nil {
		return nil, err
	}
	entries := root
	if f := root.Get("manifest_format"); f != nil {
		if !strings.HasPrefix(f.Str, "2.") {
			return nil, fmt.Errorf("unsupported manifest_format %q", f.Str)
		}
		if entries = root.Get("deps"); entries == nil {
			return map[string]string{}, nil
		}
		if entries.Kind != tomlspan.Table {
			return nil, errors.New("deps is not a table")
		}
	}
	out := map[string]string{}
	for _, name := range entries.Keys {
		arr := entries.Fields[name]
		if arr.Kind != tomlspan.Array {
			continue
		}
		for _, e := range arr.Items {
			if e.Kind != tomlspan.Table || e.Fields["path"] != nil || e.Fields["repo-url"] != nil {
				continue
			}
			u, v := e.Get("uuid"), e.Get("version")
			if u != nil && u.Kind == tomlspan.String && v != nil && v.Kind == tomlspan.String {
				out[strings.ToLower(u.Str)] = v.Str
			}
		}
	}
	return out, nil
}

// ReadLocks records the version the manifest of each project (lockPath) locks its packages to
// (dep.Dep.Locked). A compat entry without a lower bound and a [deps] package without a
// compat entry take the locked version as their Version, or are skipped without one. An
// unreadable manifest is reported once and its deps are bumped (LockUnread).
func (juliaManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		pkgs map[string]string
		err  error
	}
	locks := map[string]*lockState{}
	paths := map[string]string{}
	var errs []error
	for _, f := range files {
		for j := range f {
			d := &f[j]
			if d.Skip != "" || d.Range == "" {
				continue
			}
			file, ok := paths[d.File]
			if !ok {
				file = lockPath(root, d.File)
				paths[d.File] = file
			}
			var st *lockState
			if file != "" {
				if st = locks[file]; st == nil {
					st = &lockState{}
					b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
					if err == nil {
						st.pkgs, err = lockedPackages(b)
					}
					if err != nil {
						st.err = fmt.Errorf("%s: %w", file, err)
						errs = append(errs, st.err)
					}
					locks[file] = st
				}
			}
			switch {
			case st == nil:
			case st.err != nil:
				d.LockUnread = true
			default:
				d.Locked = st.pkgs[d.Source]
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

// LockRoots implements manager.LockMaintainer: the manifest of each project with one, once
// (a workspace shares its root's).
func (juliaManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if l := lockPath(root, f); l != "" {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: l})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int {
		return cmp.Or(strings.Compare(a.Lockfile, b.Lockfile), strings.Compare(a.Manifest, b.Manifest))
	})
	return slices.CompactFunc(out, func(a, b manager.LockRoot) bool { return a.Lockfile == b.Lockfile })
}

// Locker is the julia post-processor (engine.PostProcessor, UpdatePinner, LockMaintainer):
// it updates the Manifest.toml of a patched project with Pkg, run by `julia` in a temporary
// copy of the manifest's directory. The working tree is never touched.
type Locker struct{}

// Siblings lists the unversioned manifest names; ScopeSiblings names the scope's own.
func (Locker) Siblings() []string { return []string{juliaManifest, manifestFile} }

// ScopeSiblings implements engine.ScopeSiblingLister: the manifest of dir (manifestName).
func (Locker) ScopeSiblings(dir string) []string {
	if n := manifestName(dir); n != "" {
		return []string{n}
	}
	return nil
}

// Scope returns the directory of the manifest of the project file manifest (lockPath): its
// workspace root's or its own.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	l := lockPath(root, manifest)
	return path.Dir(l), l != ""
}

// Process re-resolves the manifest for the patched projects.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// pkgSpec is a package Pkg.add moves to exactly version.
type pkgSpec struct{ name, uuid, version string }

// safeRe matches the package names, UUIDs and versions written into the Pkg script.
var safeRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ProcessUpdates moves every updated [deps] package the manifest locks, manifest updates (ups)
// and lockfile-only ones (locks) alike, to exactly the update's version: `Pkg.add` with the
// version for each project of the scope that has such updates, which keeps every other locked
// version Pkg can keep. A project whose updates moved no locked package (compat entries of
// [weakdeps] and [extras], packages the manifest does not have) runs `Pkg.resolve`, which
// records the new compat entries (the manifest's project_hash) and moves what they exclude.
// It returns the manifest when it changed.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	name := manifestName(dir)
	orig, ok := files[name]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need a Manifest.toml in %s", dir)
		}
		return nil, nil
	}
	locked, err := lockedPackages(orig)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	specs := map[string][]pkgSpec{} // by project directory, relative to dir
	for _, byProject := range []map[string][]dep.Update{ups, locks} {
		for rel, us := range byProject {
			pdir := path.Dir(rel)
			if specs[pdir] == nil {
				specs[pdir] = []pkgSpec{}
			}
			for _, u := range us {
				if u.Dep.DepType != DepTypeDeps || locked[u.Dep.Source] == "" {
					continue
				}
				v, _, _ := strings.Cut(u.NewVersion, "+") // a build is not a version specifier
				s := pkgSpec{name: u.Dep.Name, uuid: u.Dep.Source, version: v}
				if !safeRe.MatchString(s.name) || !safeRe.MatchString(s.uuid) || !safeRe.MatchString(s.version) {
					return nil, fmt.Errorf("%s: invalid package %s %s", rel, s.name, s.version)
				}
				if !slices.Contains(specs[pdir], s) {
					specs[pdir] = append(specs[pdir], s)
				}
			}
		}
	}
	for rel := range manifests {
		if specs[path.Dir(rel)] == nil {
			specs[path.Dir(rel)] = []pkgSpec{}
		}
	}
	var script strings.Builder
	var args []string
	for i, pdir := range slices.Sorted(maps.Keys(specs)) {
		args = append(args, filepath.FromSlash(pdir))
		fmt.Fprintf(&script, "Pkg.activate(ARGS[%d])\n", i+1)
		if len(specs[pdir]) == 0 {
			script.WriteString("Pkg.resolve()\n")
			continue
		}
		script.WriteString("Pkg.add([")
		for j, s := range specs[pdir] {
			if j > 0 {
				script.WriteString(", ")
			}
			fmt.Fprintf(&script, "Pkg.PackageSpec(name=%q, uuid=%q, version=%q)", s.name, s.uuid, s.version)
		}
		script.WriteString("])\n")
	}
	return runPkg(ctx, dir, name, manifests, orig, script.String(), args)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `Pkg.update()` in a copy of
// dir moves every locked package to the newest version the compat entries admit; the projects
// are unchanged. It returns the manifest when it changed; without one it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	name := manifestName(dir)
	lock, ok := files[name]
	if !ok {
		return nil, nil
	}
	return runPkg(ctx, dir, name, nil, lock, "Pkg.activate(ARGS[1])\nPkg.update()\n", []string{"."})
}

// runPkg mirrors dir (mirror) with the patched projects and the manifest, runs the Pkg
// script with the project directories args (relative to dir) and returns the manifest under
// name when it changed.
func runPkg(ctx context.Context, dir, name string, projects map[string][]byte, lock []byte, script string, args []string) (map[string][]byte, error) {
	tmp, err := os.MkdirTemp("", "deps-julia-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	base, err := toolrun.MirrorAncestors(dir, tmp)
	if err != nil {
		return nil, err
	}
	if err := mirror(dir, base, projects); err != nil {
		return nil, err
	}
	// Pkg picks a versioned manifest by its own Julia version: the copy is the one it reads.
	if err := os.WriteFile(filepath.Join(base, manifestFile), lock, 0o644); err != nil {
		return nil, err
	}
	for i, a := range args {
		args[i] = filepath.Join(base, a)
	}
	if err := julia(ctx, base, dir, name, pkgPrelude+script, args); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(base, manifestFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{name: out}, nil
}

// pkgPrelude loads Pkg and installs the General registry into a depot without any (a fresh
// CI runner), as Pkg.add would, so Pkg.resolve finds the packages too.
const pkgPrelude = "import Pkg\nisempty(Pkg.Registry.reachable_registries()) && Pkg.Registry.add(\"General\")\n"

// mirror recreates the tree below src in dst: directories are created, project files copied
// (projects, by slash path, replace their content), manifests left out, other files
// symlinked. .git and node_modules directories are left out.
func mirror(src, dst string, projects map[string][]byte) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir() && rel != "." && (d.Name() == ".git" || d.Name() == "node_modules"):
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case rel == d.Name() && (d.Name() == manifestFile || d.Name() == juliaManifest || versionedRe.MatchString(d.Name())):
			return nil
		case slices.Contains(ProjectNames, d.Name()):
			b, ok := projects[filepath.ToSlash(rel)]
			if !ok {
				if b, err = os.ReadFile(p); err != nil {
					return err
				}
			}
			return os.WriteFile(target, b, 0o644)
		}
		return os.Symlink(p, target)
	})
}

// julia runs the Pkg script in dir (the mirror of scope) with args; precompilation is off. A
// failure carries the tail of Julia's output before the backtrace, with the mirror's paths
// shown as scope's.
func julia(ctx context.Context, dir, scope, lock, script string, args []string) error {
	bin, err := exec.LookPath("julia")
	if err != nil {
		return fmt.Errorf("julia not found (needed to update %s)", lock)
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"--startup-file=no", "--history-file=no", "--color=no", "-e", script}, args...)...)
	cmd.Dir = dir
	cmd.Env = toolrun.Env("JULIA_PKG_PRECOMPILE_AUTO=0")
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "julia command", "script", script, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	b := out.Bytes()
	if i := bytes.Index(b, []byte("\nStacktrace:")); i >= 0 {
		b = b[:i] // Julia's backtrace follows the ERROR message
	}
	msg := strings.ReplaceAll(toolrun.Tail(b, 20), dir, scope)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("julia Pkg in %s: %s", scope, msg)
}
