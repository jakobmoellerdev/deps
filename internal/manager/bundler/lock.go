package bundler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

const (
	manifestFile = "Gemfile"
	lockFile     = "Gemfile.lock"
)

// lockedGems returns the version each gem of the GEM sections (gems of rubygems servers) of a
// Gemfile.lock is locked to, without a platform suffix (`nokogiri (1.18.10-x86_64-linux-gnu)`
// is 1.18.10: gem versions have no "-"). Gems of GIT and PATH sections are left out. A file
// without any Bundler section is not a lockfile.
func lockedGems(b []byte) (map[string]string, error) {
	out := map[string]string{}
	section, known := "", false
	for line := range bytes.Lines(b) {
		l := string(bytes.TrimRight(line, "\r\n"))
		if l == "" {
			continue
		}
		if l[0] != ' ' {
			section = l
			switch section {
			case "GEM", "GIT", "PATH", "PLUGIN SOURCE", "DEPENDENCIES":
				known = true
			}
			continue
		}
		spec, ok := strings.CutPrefix(l, "    ")
		if section != "GEM" || !ok || spec == "" || spec[0] == ' ' {
			continue
		}
		name, v, ok := strings.Cut(spec, " (")
		if !ok || !strings.HasSuffix(v, ")") {
			continue
		}
		v, _, _ = strings.Cut(strings.TrimSuffix(v, ")"), "-")
		if _, dup := out[name]; !dup {
			out[name] = v
		}
	}
	if !known {
		return nil, errors.New("not a Gemfile.lock (no GEM, GIT, PATH or DEPENDENCIES section)")
	}
	return out, nil
}

// ReadLocks records the version the Gemfile.lock next to each Gemfile locks its gems to
// (dep.Dep.Locked). A requirement without a floor (no requirement, `< 2`) takes the locked
// version as its Version, or is skipped without one. An unreadable Gemfile.lock is reported
// once and its deps are bumped (LockUnread).
func (bundlerManager) ReadLocks(root string, files [][]dep.Dep) []error {
	type lockState struct {
		gems map[string]string
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
			file := path.Join(path.Dir(d.File), lockFile)
			st := locks[file]
			if st == nil {
				st = &lockState{}
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
				if err == nil {
					st.gems, err = lockedGems(b)
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					st.err = fmt.Errorf("%s: %w", file, err)
					errs = append(errs, st.err)
				}
				locks[file] = st
			}
			if st.err != nil {
				d.LockUnread = true
			} else {
				d.Locked = st.gems[d.Name]
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

// LockRoots implements manager.LockMaintainer: the Gemfile.lock next to each Gemfile.
func (bundlerManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if dir, ok := (Locker{}).Scope(context.Background(), root, f); ok {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: path.Join(dir, lockFile)})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// Locker is the bundler post-processor (engine.PostProcessor, UpdatePinner and
// LockMaintainer): it updates the Gemfile.lock next to a patched Gemfile with `bundle lock` in
// a temporary copy of the directory. The working tree is never touched.
type Locker struct{}

// Siblings lists Gemfile.lock.
func (Locker) Siblings() []string { return []string{lockFile} }

// Scope returns the directory of a Gemfile that has a Gemfile.lock next to it.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	dir := path.Dir(manifest)
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), lockFile))
	return dir, err == nil && st.Mode().IsRegular()
}

// Process re-resolves Gemfile.lock for the patched Gemfile with `bundle lock`.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates moves every updated gem, manifest updates (ups) and lockfile-only ones
// (locks) alike, to the update's version: `bundle lock --update <gems…> --conservative`, so
// only those gems (and what they need) move. Bundler would lock the newest version a rewritten
// requirement admits (`~> 8.0`), or keep the locked one an admitting requirement allows, so
// each gem the lock already has is pinned for that run: its requirement is written as
// `= <version>` in the temporary Gemfile. When any was, the patched Gemfile is put back and
// `bundle lock` runs again, which keeps the versions and records the Gemfile's requirements
// under DEPENDENCIES. It returns Gemfile.lock when it changed.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", lockFile, dir)
		}
		return nil, nil
	}
	src, ok := manifests[manifestFile]
	if !ok {
		var err error
		if src, err = os.ReadFile(filepath.Join(dir, manifestFile)); err != nil {
			return nil, err
		}
	}
	locked, err := lockedGems(lock)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lockFile, err)
	}
	var names []string
	pins := map[string]string{}
	for _, u := range slices.Concat(flatten(ups), flatten(locks)) {
		if _, ok := locked[u.Dep.Name]; !ok || slices.Contains(names, u.Dep.Name) {
			continue // a gem the lock does not have yet is resolved anyway
		}
		names = append(names, u.Dep.Name)
		pins[u.Dep.Name] = u.NewVersion
	}
	pinned, err := pin(src, pins)
	if err != nil {
		return nil, err
	}
	tmp, base, err := mirrorDir(dir, pinned, lock)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	args := []string{"lock"}
	if len(names) > 0 {
		args = append(append(append(args, "--update"), names...), "--conservative")
	}
	if err := bundle(ctx, base, dir, args...); err != nil {
		return nil, err
	}
	if !bytes.Equal(pinned, src) {
		if err := os.WriteFile(filepath.Join(base, manifestFile), src, 0o644); err != nil {
			return nil, err
		}
		if err := bundle(ctx, base, dir, "lock"); err != nil {
			return nil, err
		}
	}
	return changedLock(base, lock)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `bundle lock --update` in a
// copy of dir moves every locked gem to the newest version its requirements admit; the Gemfile
// is unchanged. It returns Gemfile.lock when it changed; without one it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return nil, err
	}
	tmp, base, err := mirrorDir(dir, src, lock)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := bundle(ctx, base, dir, "lock", "--update"); err != nil {
		return nil, err
	}
	return changedLock(base, lock)
}

// changedLock returns the Gemfile.lock bundle wrote in base when it differs from lock.
func changedLock(base string, lock []byte) (map[string][]byte, error) {
	out, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{lockFile: out}, nil
}

// flatten returns the updates of every manifest, in manifest path order.
func flatten(byManifest map[string][]dep.Update) []dep.Update {
	var ups []dep.Update
	for _, rel := range slices.Sorted(maps.Keys(byManifest)) {
		ups = append(ups, byManifest[rel]...)
	}
	return ups
}

// pin writes the requirements of every gem in pins (name → version) as `"= <version>"` in
// the Gemfile src: the requirement arguments are replaced, a gem without any gets one after
// its name. Requirements that already are exactly the version stay.
func pin(src []byte, pins map[string]string) ([]byte, error) {
	var edits []patch.Edit
	for _, g := range parse(src).gems {
		v, ok := pins[g.name]
		if !ok || g.skip != "" || g.nonLiteral {
			continue
		}
		if len(g.reqs) == 1 {
			if c, err := versioning.ParseRubyRequirement(g.reqs[0].text); err == nil && (c.Op == "" || c.Op == "=") && c.Version == v {
				continue
			}
		}
		e := patch.Edit{Start: g.reqStart, End: g.reqEnd, Old: string(src[g.reqStart:g.reqEnd]), New: `"= ` + v + `"`}
		if len(g.reqs) == 0 {
			e.New = `, "= ` + v + `"`
		}
		edits = append(edits, e)
	}
	if len(edits) == 0 {
		return src, nil
	}
	return patch.Apply(src, edits)
}

// mirrorDir builds a temporary tree in which dir's Gemfile (with content gemfile) and
// Gemfile.lock are copies and every other entry of dir is symlinked, below the mirrored
// repository ancestors (toolrun.MirrorAncestors), so `path:` gems, `gemspec` and
// `eval_gemfile` resolve as in the checkout. The caller removes tmp; base is the mirror of dir.
func mirrorDir(dir string, gemfile, lock []byte) (tmp, base string, err error) {
	if tmp, err = os.MkdirTemp("", "deps-bundler-*"); err != nil {
		return "", "", err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()
	if base, err = toolrun.MirrorAncestors(dir, tmp); err != nil {
		return "", "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}
	for _, e := range entries {
		switch e.Name() {
		case manifestFile, lockFile, ".git":
			continue
		}
		if err := os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(base, e.Name())); err != nil {
			return "", "", err
		}
	}
	if err := toolrun.WriteFiles(base, map[string][]byte{manifestFile: gemfile, lockFile: lock}); err != nil {
		return "", "", err
	}
	return tmp, base, nil
}

// bundle runs `bundle args...` in dir (the mirror of scope) with BUNDLE_GEMFILE set to its
// Gemfile; a failure carries the tail of Bundler's output, with the mirror's paths shown as
// scope's.
func bundle(ctx context.Context, dir, scope string, args ...string) error {
	bin, err := exec.LookPath("bundle")
	if err != nil {
		return fmt.Errorf("bundle not found (needed to update %s)", lockFile)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BUNDLE_GEMFILE="+filepath.Join(dir, manifestFile), "NO_COLOR=1")
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "bundle command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("bundle %s in %s: %s", args[0], scope, msg)
}
