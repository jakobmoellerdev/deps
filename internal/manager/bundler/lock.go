package bundler

import (
	"bytes"
	"cmp"
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

// Gemfile names and their lockfiles. Bundler uses gems.rb (with gems.locked) over a Gemfile
// in the same directory.
const (
	gemfileName = "Gemfile"
	gemfileLock = "Gemfile.lock"
	gemsRB      = "gems.rb"
	gemsLocked  = "gems.locked"
)

// lockName returns the lockfile name of the Gemfile named gemfile.
func lockName(gemfile string) string {
	if gemfile == gemsRB {
		return gemsLocked
	}
	return gemfileLock
}

// dirGemfile returns the name of the Gemfile Bundler reads in the absolute directory dir:
// gems.rb when it exists, else Gemfile.
func dirGemfile(dir string) string {
	if st, err := os.Stat(filepath.Join(dir, gemsRB)); err == nil && st.Mode().IsRegular() {
		return gemsRB
	}
	return gemfileName
}

// lockPath returns the slash path of the lockfile of manifest (relative to the absolute root):
// next to a Gemfile or gems.rb, its own; for a gemspec, the one of the Gemfile next to it.
func lockPath(root, manifest string) string {
	dir := path.Dir(manifest)
	if isGemspec(manifest) {
		return path.Join(dir, lockName(dirGemfile(filepath.Join(root, filepath.FromSlash(dir)))))
	}
	return path.Join(dir, lockName(path.Base(manifest)))
}

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

// ReadLocks records the version the lockfile of each Gemfile, gems.rb or gemspec (lockPath)
// locks its gems to (dep.Dep.Locked). A requirement without a floor (no requirement, `< 2`)
// takes the locked version as its Version, or is skipped without one. An unreadable lockfile
// is reported once and its deps are bumped (LockUnread).
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
			if d.Skip != "" || d.Range == "" || d.DepType == DepTypeRuby {
				continue
			}
			file := lockPath(root, d.File)
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

// LockRoots implements manager.LockMaintainer: the lockfile of each Gemfile, gems.rb and
// gemspec with one (a gemspec shares its Gemfile's), once.
func (bundlerManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if _, ok := (Locker{}).Scope(context.Background(), root, f); ok {
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: lockPath(root, f)})
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int {
		return cmp.Or(strings.Compare(a.Lockfile, b.Lockfile), strings.Compare(a.Manifest, b.Manifest))
	})
	return slices.CompactFunc(out, func(a, b manager.LockRoot) bool { return a.Lockfile == b.Lockfile })
}

// Locker is the bundler post-processor (engine.PostProcessor, UpdatePinner and
// LockMaintainer): it updates the Gemfile.lock (gems.locked) of a patched Gemfile (gems.rb) or
// gemspec with `bundle lock` in a temporary copy of the directory. The working tree is never
// touched.
type Locker struct{}

// Siblings lists Gemfile.lock and gems.locked.
func (Locker) Siblings() []string { return []string{gemfileLock, gemsLocked} }

// Scope returns the directory of a Gemfile, gems.rb or gemspec that has its lockfile
// (lockPath) next to it.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(lockPath(root, manifest))))
	return path.Dir(manifest), err == nil && st.Mode().IsRegular()
}

// Process re-resolves the lockfile for the patched manifests with `bundle lock`.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates moves every updated gem, manifest updates (ups) and lockfile-only ones
// (locks) alike, to the update's version: `bundle lock --update <gems…> --conservative`, so
// only those gems (and what they need) move. Bundler would lock the newest version a rewritten
// requirement admits (`~> 8.0`), or keep the locked one an admitting requirement allows, so
// each gem the lock already has is pinned for that run: its requirement is written as
// `= <version>` in the temporary Gemfile (gems.rb) and gemspecs of the scope. When any was,
// the patched files are put back and `bundle lock` runs again, which keeps the versions and
// records the requirements under DEPENDENCIES (and a gemspec's under PATH). An update of the
// `ruby` directive moves the lock's RUBY VERSION in process (setLockedRuby) before Bundler
// runs, which keeps a locked Ruby version the Gemfile admits; with only such updates Bundler
// does not run. It returns the lockfile when it changed.
func (Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	gemfile := dirGemfile(dir)
	lf := lockName(gemfile)
	orig, ok := files[lf]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", lf, dir)
		}
		return nil, nil
	}
	// The scope's Gemfile (patched, or as checked out) and its patched gemspecs.
	patched := maps.Clone(manifests)
	if _, ok := patched[gemfile]; !ok {
		src, err := os.ReadFile(filepath.Join(dir, gemfile))
		if err != nil {
			return nil, err
		}
		if patched == nil {
			patched = map[string][]byte{}
		}
		patched[gemfile] = src
	}
	locked, err := lockedGems(orig)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", lf, err)
	}
	lock := orig
	var names []string
	pins := map[string]string{}
	onlyRuby := len(ups) > 0 && len(locks) == 0
	for _, u := range slices.Concat(flatten(ups), flatten(locks)) {
		if u.Dep.DepType == DepTypeRuby {
			lock = setLockedRuby(lock, u.NewVersion)
			continue
		}
		onlyRuby = false
		if _, ok := locked[u.Dep.Name]; !ok || slices.Contains(names, u.Dep.Name) {
			continue // a gem the lock does not have yet is resolved anyway
		}
		names = append(names, u.Dep.Name)
		pins[u.Dep.Name] = u.NewVersion
	}
	if onlyRuby {
		if bytes.Equal(lock, orig) {
			return nil, nil
		}
		return map[string][]byte{lf: lock}, nil
	}
	pinned := make(map[string][]byte, len(patched)+1)
	repin := false
	for rel, src := range patched {
		p, err := pin(src, isGemspec(rel), pins)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		pinned[rel] = p
		repin = repin || !bytes.Equal(p, src)
	}
	pinned[lf] = lock
	tmp, base, err := mirrorDir(dir, pinned)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	args := []string{"lock"}
	if len(names) > 0 {
		args = append(append(append(args, "--update"), names...), "--conservative")
	}
	if err := bundle(ctx, base, dir, gemfile, args...); err != nil {
		return nil, err
	}
	if repin {
		if err := toolrun.WriteFiles(base, patched); err != nil {
			return nil, err
		}
		if err := bundle(ctx, base, dir, gemfile, "lock"); err != nil {
			return nil, err
		}
	}
	return changedLock(base, lf, orig)
}

// lockedRubySpan returns the byte span of the version in the RUBY VERSION section of a
// Gemfile.lock (`   ruby 3.3.6p108`: `3.3.6p108`, the patchlevel older Bundlers write
// included); ok is false without one.
func lockedRubySpan(lock []byte) (start, end int, ok bool) {
	off := 0
	for line := range bytes.Lines(lock) {
		off += len(line)
		if string(bytes.TrimRight(line, "\r\n")) != "RUBY VERSION" {
			continue
		}
		l, _, _ := bytes.Cut(lock[off:], []byte("\n"))
		v, ok := bytes.CutPrefix(bytes.TrimLeft(l, " "), []byte("ruby "))
		if !ok {
			return 0, 0, false
		}
		start = off + len(l) - len(v)
		n := bytes.IndexAny(v, " (\r")
		if n < 0 {
			n = len(v)
		}
		return start, start + n, n > 0
	}
	return 0, 0, false
}

// lockedRuby is v as Bundler records it under RUBY VERSION: a prerelease's "-" read as "."
// (`3.5.0-preview1` → `3.5.0.preview1`), without a patchlevel (the new release's is not known;
// current Bundlers write none). ok is false for a version Bundler cannot record there (fewer
// than three segments).
func lockedRuby(v string) (string, bool) {
	v = strings.ReplaceAll(v, "-", ".")
	return v, strings.Count(v, ".") >= 2
}

// setLockedRuby returns lock with the version of its RUBY VERSION section set to v (see
// lockedRuby), or lock itself when it records none or v cannot be recorded.
func setLockedRuby(lock []byte, v string) []byte {
	start, end, ok := lockedRubySpan(lock)
	nv, valid := lockedRuby(v)
	if !ok || !valid || string(lock[start:end]) == nv {
		return lock
	}
	return slices.Concat(lock[:start], []byte(nv), lock[end:])
}

// VersionFileEdits returns the lockfile side of an update of the Ruby version in file
// (`.ruby-version`, `.tool-versions`, a mise config) to v: when the Gemfile (gems.rb) next to
// file reads its Ruby version from it (`ruby file: ".ruby-version"`) and its lockfile records
// one (RUBY VERSION), the edit moving that to v, by the lock's slash path relative to root;
// nil otherwise. The version managers return it as their manager.FileEditor edits.
func VersionFileEdits(root, file, v string) (map[string][]patch.Edit, error) {
	dir := path.Dir(file)
	read := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path.Join(dir, name))))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return b, err
	}
	gemfile := dirGemfile(filepath.Join(root, filepath.FromSlash(dir)))
	src, err := read(gemfile)
	if src == nil {
		return nil, err
	}
	if r := parse(src).ruby; r == nil || r.file == "" || path.Join(dir, r.file) != file {
		return nil, nil
	}
	lf := lockName(gemfile)
	lock, err := read(lf)
	if lock == nil {
		return nil, err
	}
	start, end, ok := lockedRubySpan(lock)
	nv, valid := lockedRuby(v)
	if !ok || !valid || string(lock[start:end]) == nv {
		return nil, nil
	}
	return map[string][]patch.Edit{path.Join(dir, lf): {{Start: start, End: end, Old: string(lock[start:end]), New: nv}}}, nil
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `bundle lock --update` in a
// copy of dir moves every locked gem to the newest version its requirements admit; the Gemfile
// is unchanged. It returns the lockfile when it changed; without one it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	gemfile := dirGemfile(dir)
	lf := lockName(gemfile)
	lock, ok := files[lf]
	if !ok {
		return nil, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, gemfile))
	if err != nil {
		return nil, err
	}
	tmp, base, err := mirrorDir(dir, map[string][]byte{gemfile: src, lf: lock})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := bundle(ctx, base, dir, gemfile, "lock", "--update"); err != nil {
		return nil, err
	}
	return changedLock(base, lf, lock)
}

// changedLock returns the lockfile name bundle wrote in base when it differs from lock.
func changedLock(base, name string, lock []byte) (map[string][]byte, error) {
	out, err := os.ReadFile(filepath.Join(base, name))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{name: out}, nil
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
// the Gemfile, or with gemspec the gemspec, src: the requirement arguments are replaced, a gem
// without any gets one after its name. Requirements that already are exactly the version stay.
func pin(src []byte, gemspec bool, pins map[string]string) ([]byte, error) {
	var edits []patch.Edit
	decls := parse(src).gems
	if gemspec {
		decls = parseGemspec(src).deps
	}
	for _, g := range decls {
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

// mirrorDir builds a temporary tree (toolrun.Mirror) in which the files of dir in copies (the
// Gemfile, the lockfile and patched gemspecs, by name) are written and every other entry of
// dir but .git is symlinked, so `path:` gems, `gemspec` and `eval_gemfile` resolve as in the
// checkout. The caller removes tmp; base is the mirror of dir.
func mirrorDir(dir string, copies map[string][]byte) (tmp, base string, err error) {
	return toolrun.Mirror(dir, toolrun.MirrorOptions{Pattern: "deps-bundler-*", Files: copies, Skip: toolrun.SkipGit})
}

// bundle runs `bundle args...` in dir (the mirror of scope) with BUNDLE_GEMFILE set to its
// gemfile (Gemfile or gems.rb); a failure carries the tail of Bundler's output, with the
// mirror's paths shown as scope's.
func bundle(ctx context.Context, dir, scope, gemfile string, args ...string) error {
	bin, err := exec.LookPath("bundle")
	if err != nil {
		return fmt.Errorf("bundle not found (needed to update %s)", lockName(gemfile))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BUNDLE_GEMFILE="+filepath.Join(dir, gemfile), "NO_COLOR=1")
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
