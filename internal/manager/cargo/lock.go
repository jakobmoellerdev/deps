package cargo

import (
	"bytes"
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

	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

const (
	manifestFile = "Cargo.toml"
	lockFile     = "Cargo.lock"
)

// Locker is the cargo post-processor (engine.PostProcessor): after deps patched Cargo.toml
// files it updates the Cargo.lock of their workspace with `cargo update` in a temporary
// directory.
type Locker struct{}

// Siblings lists Cargo.lock.
func (Locker) Siblings() []string { return []string{lockFile} }

// Scope returns the workspace root of the Cargo.toml manifest (slash paths relative to root):
// the nearest directory at or above the manifest's, within root, that holds a Cargo.lock. It is
// accepted when it is the manifest's own directory or when its Cargo.toml makes that directory
// a workspace member (isMember); otherwise the manifest has no lockfile.
func (Locker) Scope(ctx context.Context, root, manifest string) (string, bool) {
	member := path.Dir(manifest)
	for dir := member; ; dir = path.Dir(dir) {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if st, err := os.Stat(filepath.Join(abs, lockFile)); err == nil && st.Mode().IsRegular() {
			if dir == member {
				return dir, true
			}
			rel := member
			if dir != "." {
				rel = strings.TrimPrefix(member, dir+"/")
			}
			if isMember(ctx, filepath.Join(abs, manifestFile), rel) {
				return dir, true
			}
			slog.DebugContext(ctx, "not a cargo workspace member", "dir", dir, "manifest", manifest)
			return "", false
		}
		if dir == "." {
			return "", false
		}
	}
}

// isMember reports whether the workspace manifest at file includes the member directory rel:
// its `[workspace]` `members` globs match rel, or one of its path dependencies resolves to
// rel, which cargo makes a member of the workspace too (a root `[package]` with an empty
// `[workspace]` and `foo = { path = "crates/foo" }` has no `members` list at all). A
// directory `exclude` matches is never a member.
func isMember(ctx context.Context, file, rel string) bool {
	src, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	root, err := tomlspan.Parse(src)
	if err != nil {
		slog.DebugContext(ctx, "cargo workspace manifest unreadable", "file", file, "err", err)
		return false
	}
	if root.Get("workspace") == nil {
		return false // not a workspace root: the manifest only describes its own package
	}
	strs := func(k string) []string {
		items := root.Get("workspace", k).Strings()
		out := make([]string, 0, len(items))
		for _, s := range items {
			out = append(out, s.Str)
		}
		return out
	}
	if matchAny(strs("exclude"), rel) {
		return false
	}
	if matchAny(strs("members"), rel) {
		return true
	}
	for _, p := range pathDepsOf(root) {
		if path.Clean(p) == rel {
			return true
		}
	}
	return false
}

func matchAny(globs []string, rel string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(path.Clean(g), rel); ok && err == nil {
			return true
		}
	}
	return false
}

// Process mirrors the workspace root dir into a temporary directory (Cargo.toml files copied,
// the patched content for manifests, everything else symlinked; .git, target and
// node_modules left out) and runs `cargo update --workspace` there, which re-resolves only
// the requirements that no longer match the lock. Then it pins every updated crate whose new
// requirement is a full version with `cargo update --package <name>@<locked> --precise
// <version>` when cargo resolved a newer one that was not locked before (an already locked
// version is never downgraded). It returns Cargo.lock when it changed; without a lockfile it
// does nothing.
func (Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return process(ctx, dir, manifests, files, nil, nil)
}

// ProcessLocked is Process plus the lockfile-only updates (engine.LockfileUpdater): each moves
// its crate to the new version with `cargo update --package <name>@<from> --precise <version>`,
// from the entry the earlier commands left in the locked version's compatibility range
// (pinning a crate can move the crates it depends on); it is skipped when that entry already
// has the version or a newer one. The manifests keep their requirements.
func (Locker) ProcessLocked(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
	if _, ok := files[lockFile]; !ok {
		return nil, fmt.Errorf("lockfile-only updates need %s in %s", lockFile, dir)
	}
	return process(ctx, dir, manifests, files, flatten(locks), nil)
}

// ProcessUpdates is ProcessLocked plus the manifest updates (engine.UpdatePinner). A rewritten
// requirement can still admit the version locked before (`<0.10` → `<2.0` with 0.9.0
// locked), which leaves `cargo update --workspace` nothing to do; such a crate is resolved to
// the update's NewVersion (pinStale), security updates included.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	if _, ok := files[lockFile]; !ok && len(locks) > 0 {
		return nil, fmt.Errorf("lockfile-only updates need %s in %s", lockFile, dir)
	}
	return process(ctx, dir, manifests, files, flatten(locks), ups)
}

// flatten returns the updates of every manifest, in manifest path order.
func flatten(byManifest map[string][]dep.Update) []dep.Update {
	var ups []dep.Update
	for _, rel := range slices.Sorted(maps.Keys(byManifest)) {
		ups = append(ups, byManifest[rel]...)
	}
	return ups
}

func process(ctx context.Context, dir string, manifests, files map[string][]byte, lockOnly []dep.Update, ups map[string][]dep.Update) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	updated := updatedCrates(dir, manifests)
	tmp, base, err := mirrorRoot(dir, manifests, lock)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	lockPath := filepath.Join(base, lockFile)
	tc := rustupToolchain(ctx, dir)
	if err := cargo(ctx, base, dir, tc, "update", "--workspace"); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, err
	}
	pins, err := precise(lock, out, updated)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lockFile, err)
	}
	st, err := stale(lock, out, ups)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lockFile, err)
	}
	if len(st) > 0 {
		if err := pinStale(ctx, base, dir, tc, manifests, st); err != nil {
			return nil, err
		}
	}
	for _, p := range pins {
		if err := cargo(ctx, base, dir, tc, "update", "--package", p.name+"@"+p.locked, "--precise", p.version); err != nil {
			return nil, err
		}
	}
	var done []pin
	for _, u := range lockOnly {
		p := pin{name: u.Dep.Name, locked: u.Dep.Locked, version: u.NewVersion}
		if slices.Contains(done, p) {
			continue
		}
		done = append(done, p)
		if out, err = os.ReadFile(lockPath); err != nil {
			return nil, err
		}
		pkgs, err := registryPackages(out)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", lockFile, err)
		}
		from, ok := lockedNow(pkgs, p)
		if !ok {
			slog.DebugContext(ctx, "lockfile-only update already locked", "crate", p.name, "version", p.version)
			continue
		}
		if err := cargo(ctx, base, dir, tc, "update", "--package", p.name+"@"+from, "--precise", p.version); err != nil {
			return nil, err
		}
	}
	if out, err = os.ReadFile(lockPath); err != nil {
		return nil, err
	}
	if bytes.Equal(out, lock) {
		return nil, nil
	}
	return map[string][]byte{lockFile: out}, nil
}

// lockedNow returns the version lockfile-only pin p starts from in pkgs: the entry of the
// locked version's compatibility range (Cargo locks at most one per range), which an earlier
// command may have moved. ok is false when that entry already has p.version or a newer one,
// or the crate is gone.
func lockedNow(pkgs []lockedPackage, p pin) (string, bool) {
	want, err1 := semver.StrictNewVersion(p.version)
	old, err2 := semver.StrictNewVersion(p.locked)
	if err1 != nil || err2 != nil {
		return p.locked, true
	}
	var cur *semver.Version
	for _, q := range pkgs {
		if v, err := semver.StrictNewVersion(q.version); q.name == p.name && err == nil && compatible(v, old) {
			cur = v
		}
	}
	if cur == nil || !cur.LessThan(want) {
		return "", false
	}
	return cur.Original(), true
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `cargo update` in the
// mirror of the workspace root dir moves every locked crate to the newest version its
// requirement admits; the manifests are unchanged. It returns Cargo.lock when it changed;
// without a lockfile it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	tmp, base, err := mirrorRoot(dir, nil, lock)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := cargo(ctx, base, dir, rustupToolchain(ctx, dir), "update"); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{lockFile: out}, nil
}

// mirrorRoot mirrors the workspace root dir (toolrun.Mirror) with the patched manifests and
// lock into a new temporary directory tmp, which the caller removes; base is the mirror of
// dir. Every directory is recreated so .git, target and node_modules directories are left
// out at any depth; other entries are symlinked.
func mirrorRoot(dir string, manifests map[string][]byte, lock []byte) (tmp, base string, err error) {
	files := make(map[string][]byte, len(manifests)+1)
	maps.Copy(files, manifests)
	files[lockFile] = lock
	return toolrun.Mirror(dir, toolrun.MirrorOptions{Pattern: "deps-cargo-*", Files: files, Skip: skipDir, Recreate: everyDir})
}

// skipDir is a toolrun.MirrorOptions.Skip leaving out .git, target and node_modules
// directories at any depth.
func skipDir(_ string, d fs.DirEntry) bool {
	switch d.Name() {
	case ".git", "target", "node_modules":
		return d.IsDir()
	}
	return false
}

// everyDir is a toolrun.MirrorOptions.Recreate recreating every directory.
func everyDir(string, fs.DirEntry) bool { return true }

// crateUpdate is a crates.io dependency whose requirement deps changed.
type crateUpdate struct{ name, version string }

// updatedCrates compares the crates.io deps of each patched manifest with the file on disk and
// returns each changed (crate, version) once. Manifests deps cannot extract are left to cargo
// to report.
func updatedCrates(dir string, manifests map[string][]byte) []crateUpdate {
	var out []crateUpdate
	m := cargoManager{}
	for rel, patched := range manifests {
		old, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		before, err1 := m.Extract(rel, old, manager.Env{})
		after, err2 := m.Extract(rel, patched, manager.Env{})
		if err1 != nil || err2 != nil || len(before) != len(after) {
			continue
		}
		for i, a := range after {
			u := crateUpdate{name: a.Name, version: a.Version}
			if b := before[i]; a.Datasource == Datasource && a.Skip == "" && a.Name == b.Name && a.Version != b.Version && !slices.Contains(out, u) {
				out = append(out, u)
			}
		}
	}
	return out
}

// pin is a `cargo update --precise` run.
type pin struct{ name, locked, version string }

// lockedPackage is a registry (or git) [[package]] of a Cargo.lock.
type lockedPackage struct{ name, version, source string }

// registryPackages returns the crates.io and other registry packages of the lockfile.
func registryPackages(lock []byte) ([]lockedPackage, error) {
	return packagesOf(lock, func(src string) bool {
		return strings.HasPrefix(src, "registry+") || strings.HasPrefix(src, "sparse+")
	})
}

// packagesOf returns the packages of the lockfile whose source keep accepts.
func packagesOf(lock []byte, keep func(src string) bool) ([]lockedPackage, error) {
	root, err := tomlspan.Parse(lock)
	if err != nil {
		return nil, err
	}
	pkgs := root.Get("package")
	if pkgs == nil || pkgs.Kind != tomlspan.Array {
		return nil, nil
	}
	out := make([]lockedPackage, 0, len(pkgs.Items))
	for _, t := range pkgs.Items {
		str := func(k string) string {
			if v := t.Get(k); v != nil && v.Kind == tomlspan.String {
				return v.Str
			}
			return ""
		}
		if src := str("source"); keep(src) {
			out = append(out, lockedPackage{str("name"), str("version"), src})
		}
	}
	return out, nil
}

// precise returns the pins that make the lock hold the exact version of each update whose
// requirement is a full version: when no registry entry of the crate has that version and the
// one in the same compatibility range (Cargo locks at most one) is newer and was not in the
// lock before (cargo just resolved it), it is pinned down. A version that was already locked is
// never downgraded.
func precise(before, after []byte, updates []crateUpdate) ([]pin, error) {
	old, err := registryPackages(before)
	if err != nil {
		return nil, err
	}
	pkgs, err := registryPackages(after)
	if err != nil {
		return nil, err
	}
	var pins []pin
	for _, u := range updates {
		want, err := semver.StrictNewVersion(u.version)
		if err != nil {
			continue // a partial requirement: cargo's newest match is the update
		}
		var newer string
		exact := false
		for _, p := range pkgs {
			if p.name != u.name {
				continue
			}
			v, err := semver.StrictNewVersion(p.version)
			switch {
			case err != nil:
			case v.Equal(want):
				exact = true
			case compatible(v, want) && v.GreaterThan(want) && !slices.Contains(old, p):
				newer = p.version
			}
		}
		dup := slices.ContainsFunc(pins, func(q pin) bool { return q.name == u.name && q.locked == newer })
		if !exact && newer != "" && !dup {
			pins = append(pins, pin{name: u.name, locked: newer, version: u.version})
		}
	}
	return pins, nil
}

// stale returns the manifest updates cargo left alone, by manifest: the crate's registry
// entries are the same before and after `cargo update --workspace`, its locked version is
// still among them and older than NewVersion, and NewVersion is not locked. That happens when
// the rewritten requirement still admits the locked version (`<0.10` → `<2.0`).
func stale(before, after []byte, ups map[string][]dep.Update) (map[string][]dep.Update, error) {
	if len(ups) == 0 {
		return nil, nil
	}
	old, err := registryPackages(before)
	if err != nil {
		return nil, err
	}
	pkgs, err := registryPackages(after)
	if err != nil {
		return nil, err
	}
	of := func(pkgs []lockedPackage, name string) []lockedPackage {
		return slices.DeleteFunc(slices.Clone(pkgs), func(p lockedPackage) bool { return p.name != name })
	}
	has := func(pkgs []lockedPackage, version string) bool {
		return slices.ContainsFunc(pkgs, func(p lockedPackage) bool { return p.version == version })
	}
	var out map[string][]dep.Update
	for _, rel := range slices.Sorted(maps.Keys(ups)) {
		for _, u := range ups[rel] {
			d := u.Dep
			from, err1 := semver.StrictNewVersion(d.Locked)
			to, err2 := semver.StrictNewVersion(u.NewVersion)
			if d.Range == "" || err1 != nil || err2 != nil || !to.GreaterThan(from) {
				continue
			}
			now := of(pkgs, d.Name)
			if slices.Equal(now, of(old, d.Name)) && has(now, d.Locked) && !has(now, u.NewVersion) {
				if out == nil {
					out = map[string][]dep.Update{}
				}
				out[rel] = append(out[rel], u)
			}
		}
	}
	return out, nil
}

// pinStale resolves the stale updates to their NewVersion: their requirements are written as
// `=<version>` into the mirrored manifests for one `cargo update --workspace`, then the
// patched manifests are restored and resolved again, which keeps that version. Unlike
// `cargo update --precise` on the locked entry, this leaves the old version locked for other
// dependents that still require it (rustc_version 0.2 needs semver ^0.9).
func pinStale(ctx context.Context, base, dir, toolchain string, manifests map[string][]byte, st map[string][]dep.Update) error {
	for rel, ups := range st {
		src := manifests[rel]
		deps, err := cargoManager{}.Extract(rel, src, manager.Env{})
		if err != nil {
			return err
		}
		var edits []patch.Edit
		for _, u := range ups {
			i := slices.IndexFunc(deps, func(d dep.Dep) bool {
				return d.Range != "" && d.Name == u.Dep.Name && d.DepType == u.Dep.DepType && d.Line == u.Dep.Line
			})
			if i < 0 || slices.ContainsFunc(edits, func(e patch.Edit) bool { return e.Start == deps[i].RefSpan.Start }) {
				continue
			}
			edits = append(edits, patch.Edit{Start: deps[i].RefSpan.Start, End: deps[i].RefSpan.End, Old: deps[i].Range, New: "=" + u.NewVersion})
		}
		exact, err := patch.Apply(src, edits)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(base, filepath.FromSlash(rel)), exact, 0o644); err != nil {
			return err
		}
	}
	if err := cargo(ctx, base, dir, toolchain, "update", "--workspace"); err != nil {
		return err
	}
	for rel := range st {
		if err := os.WriteFile(filepath.Join(base, filepath.FromSlash(rel)), manifests[rel], 0o644); err != nil {
			return err
		}
	}
	return cargo(ctx, base, dir, toolchain, "update", "--workspace")
}

// compatible reports whether a and b are in the same Cargo compatibility range: the same
// leftmost non-zero part.
func compatible(a, b *semver.Version) bool {
	switch {
	case a.Major() != b.Major():
		return false
	case a.Major() > 0:
		return true
	case a.Minor() != b.Minor():
		return false
	case a.Minor() > 0:
		return true
	}
	return a.Patch() == b.Patch()
}

// cargo runs `cargo args...` in dir (the mirror of scope), with RUSTUP_TOOLCHAIN set to
// toolchain unless it is "" (rustupToolchain); a failure carries the tail of cargo's output,
// with the mirror's paths shown as scope's.
func cargo(ctx context.Context, dir, scope, toolchain string, args ...string) error {
	bin, err := exec.LookPath("cargo")
	if err != nil {
		return fmt.Errorf("cargo not found (needed to update %s)", lockFile)
	}
	cmd, cancel := toolrun.Command(ctx, bin, args...)
	defer cancel()
	cmd.Dir = dir
	env := []string{"CARGO_TERM_COLOR=never", "CARGO_TERM_PROGRESS_WHEN=never"}
	if toolchain != "" {
		env = append(env, "RUSTUP_TOOLCHAIN="+toolchain)
	}
	cmd.Env = toolrun.Env(env...)
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "cargo command", "args", args, "dir", scope, "toolchain", toolchain, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("cargo %s in %s: %s", args[0], scope, msg)
}
