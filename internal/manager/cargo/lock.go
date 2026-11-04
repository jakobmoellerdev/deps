package cargo

import (
	"bytes"
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

	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
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
// accepted when it is the manifest's own directory or when its Cargo.toml `[workspace]`
// `members` globs include the manifest's directory and `exclude` does not; otherwise the
// manifest has no lockfile.
func (Locker) Scope(root, manifest string) (string, bool) {
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
			if isMember(filepath.Join(abs, manifestFile), rel) {
				return dir, true
			}
			slog.Debug("not a cargo workspace member", "dir", dir, "manifest", manifest)
			return "", false
		}
		if dir == "." {
			return "", false
		}
	}
}

// isMember reports whether the workspace manifest at file includes the member directory rel.
func isMember(file, rel string) bool {
	src, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	var members, exclude []string
	err = scanTOML(src, func(it item) {
		if it.kind != 'a' || len(it.key) != 2 || it.key[0] != "workspace" {
			return
		}
		switch it.key[1] {
		case "members":
			members = it.strs
		case "exclude":
			exclude = it.strs
		}
	})
	if err != nil {
		slog.Debug("cargo workspace manifest unreadable", "file", file, "err", err)
		return false
	}
	return matchAny(members, rel) && !matchAny(exclude, rel)
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
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	updated := updatedCrates(dir, manifests)
	tmp, err := os.MkdirTemp("", "deps-cargo-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := mirror(dir, tmp, manifests); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(tmp, lockFile)
	if err := os.WriteFile(lockPath, lock, 0o644); err != nil {
		return nil, err
	}
	if err := cargo(ctx, tmp, dir, "update", "--workspace"); err != nil {
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
	for _, p := range pins {
		if err := cargo(ctx, tmp, dir, "update", "--package", p.name+"@"+p.locked, "--precise", p.version); err != nil {
			return nil, err
		}
	}
	if len(pins) > 0 {
		if out, err = os.ReadFile(lockPath); err != nil {
			return nil, err
		}
	}
	if bytes.Equal(out, lock) {
		return nil, nil
	}
	return map[string][]byte{lockFile: out}, nil
}

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

// lockedPackage is a registry [[package]] of a Cargo.lock.
type lockedPackage struct{ name, version string }

// registryPackages returns the crates.io and other registry packages of the lockfile.
func registryPackages(lock []byte) ([]lockedPackage, error) {
	type pkg struct{ name, version, source string }
	var pkgs []pkg
	err := scanTOML(lock, func(it item) {
		switch {
		case it.kind == 'H' && len(it.key) == 1 && it.key[0] == "package":
			pkgs = append(pkgs, pkg{})
		case len(pkgs) == 0 || len(it.key) != 2 || it.key[0] != "package" || it.kind != 's':
		case it.key[1] == "name":
			pkgs[len(pkgs)-1].name = it.str
		case it.key[1] == "version":
			pkgs[len(pkgs)-1].version = it.str
		case it.key[1] == "source":
			pkgs[len(pkgs)-1].source = it.str
		}
	})
	out := make([]lockedPackage, 0, len(pkgs))
	for _, p := range pkgs {
		if strings.HasPrefix(p.source, "registry+") || strings.HasPrefix(p.source, "sparse+") {
			out = append(out, lockedPackage{p.name, p.version})
		}
	}
	return out, err
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

// mirror recreates the tree below src in dst: directories are created, Cargo.toml files are
// copied (manifests replace their content), other files are symlinked. .git, target and
// node_modules directories are left out.
func mirror(src, dst string, manifests map[string][]byte) error {
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
		case d.IsDir() && rel != "." && (d.Name() == ".git" || d.Name() == "target" || d.Name() == "node_modules"):
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Name() == lockFile && rel == lockFile:
			return nil
		case d.Name() == manifestFile:
			b, ok := manifests[filepath.ToSlash(rel)]
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

// cargo runs `cargo args...` in dir (the mirror of scope); a failure carries the tail of
// cargo's output, with the mirror's paths shown as scope's.
func cargo(ctx context.Context, dir, scope string, args ...string) error {
	bin, err := exec.LookPath("cargo")
	if err != nil {
		return fmt.Errorf("cargo not found (needed to update %s)", lockFile)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CARGO_TERM_COLOR=never", "CARGO_TERM_PROGRESS_WHEN=never")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "cargo command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := strings.ReplaceAll(tail(out.Bytes(), 20), dir, scope)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("cargo %s in %s: %s", args[0], scope, msg)
}

// tail returns the last n lines of b, trimmed.
func tail(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}
