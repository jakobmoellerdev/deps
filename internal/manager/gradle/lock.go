package gradle

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Lockfile names (Gradle dependency locking): per project, its buildscript and settings
// classpaths, and the legacy per-configuration files below gradle/dependency-locks/.
const (
	lockfileName        = "gradle.lockfile"
	buildscriptLockfile = "buildscript-gradle.lockfile"
	settingsLockfile    = "settings-gradle.lockfile"
	legacyLockDir       = "gradle/dependency-locks"
	legacyBuildscript   = "buildscript-classpath.lockfile"
)

// lockKind selects the lockfiles of a dep.
type lockKind uint8

const (
	lockProject     lockKind = iota // gradle.lockfile, legacy *.lockfile
	lockBuildscript                 // buildscript-gradle.lockfile, legacy buildscript-classpath.lockfile
	lockSettings                    // settings-gradle.lockfile
)

// lockState is what Link leaves for ReadLocks, by index of the files passed to Link.
type lockState struct {
	dirs     [][]string  // project directories to look in, in order
	meta     [][]depMeta // the file's dep metadata
	settings []bool      // the file is a settings file
}

// locks are the locked versions of one lockfile set by group:artifact.
type locks map[string][]string

// ReadLocks sets the locked version of every dynamic-version dep: the version the lockfile of
// its project locks group:artifact to (the highest one when configurations differ).
// Buildscript classpath deps and plugins (as their marker `<id>:<id>.gradle.plugin`) are read
// from buildscript-gradle.lockfile, settings plugins from settings-gradle.lockfile. Catalog
// and buildSrc deps take the first project of the build, root project first, whose lockfile
// has the module. A locked dep is no longer skipped; its Version is the locked version.
func (m *gradleManager) ReadLocks(root string, files [][]dep.Dep) []error {
	m.mu.Lock()
	ls := m.lock
	m.lock = lockState{}
	m.mu.Unlock()
	scheme, _ := versioning.Get("maven")
	type key struct {
		dir  string
		kind lockKind
	}
	cache := map[key]locks{}
	var errs []error
	load := func(dir string, kind lockKind) locks {
		if l, ok := cache[key{dir, kind}]; ok {
			return l
		}
		l, err := readLockfiles(root, dir, kind)
		if err != nil {
			errs = append(errs, err)
		}
		cache[key{dir, kind}] = l
		return l
	}
	aligned := len(ls.dirs) == len(files)
	for fi, deps := range files {
		for i := range deps {
			d := &deps[i]
			if d.Range == "" || !strings.HasPrefix(d.Skip, "dynamic version ") {
				continue
			}
			dirs, class, settings := []string{path.Dir(d.File)}, classLibrary, kindOf(d.File) == kindSettings
			if d.DepType == DepTypePlugin {
				class = classPlugin
			}
			if aligned && len(ls.meta[fi]) == len(deps) {
				meta := ls.meta[fi][i]
				dirs, class, settings = ls.dirs[fi], meta.class, ls.settings[fi] && !meta.imported
			}
			kind, name := lockProject, d.Name
			switch {
			case class == classPlugin && settings:
				kind, name = lockSettings, d.Name+":"+d.Name+".gradle.plugin"
				dirs = dirs[:1]
			case class == classPlugin:
				kind, name = lockBuildscript, d.Name+":"+d.Name+".gradle.plugin"
			case class == classClasspath:
				kind = lockBuildscript
			}
			for _, dir := range dirs {
				if v := best(scheme, load(dir, kind)[name]); v != "" {
					d.Locked, d.Version, d.Skip = v, v, ""
					break
				}
			}
		}
	}
	return errs
}

// best returns the highest of the locked versions.
func best(s versioning.Scheme, vs []string) string {
	var out string
	for _, v := range vs {
		if out == "" || s != nil && s.Valid(v) && s.Valid(out) && s.Compare(v, out) > 0 {
			out = v
		}
	}
	return out
}

// readLockfiles reads the lockfiles of kind in the project directory dir (slash, relative
// to root); missing files lock nothing.
func readLockfiles(root, dir string, kind lockKind) (locks, error) {
	out := locks{}
	abs := filepath.Join(root, filepath.FromSlash(dir))
	name := [...]string{lockfileName, buildscriptLockfile, settingsLockfile}[kind]
	var errs []error
	if err := readLockfile(out, filepath.Join(abs, name)); err != nil {
		errs = append(errs, err)
	}
	if kind == lockSettings {
		return out, errors.Join(errs...)
	}
	entries, err := os.ReadDir(filepath.Join(abs, filepath.FromSlash(legacyLockDir)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".lockfile") || (n == legacyBuildscript) != (kind == lockBuildscript) {
			continue
		}
		if err := readLockfile(out, filepath.Join(abs, filepath.FromSlash(legacyLockDir), n)); err != nil {
			errs = append(errs, err)
		}
	}
	return out, errors.Join(errs...)
}

// readLockfile adds the `group:artifact:version[=configurations]` lines of file to out.
func readLockfile(out locks, file string) error {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gradle lockfile: %w", err)
	}
	for line := range bytes.Lines(b) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if i := bytes.IndexByte(line, '='); i >= 0 {
			line = line[:i]
		}
		g, rest, ok := bytes.Cut(line, []byte{':'})
		a, v, ok2 := bytes.Cut(rest, []byte{':'})
		if !ok || !ok2 || len(g) == 0 || len(a) == 0 || len(v) == 0 {
			continue // empty=…
		}
		ga := string(line[:len(g)+1+len(a)])
		out[ga] = append(out[ga], string(v))
	}
	return nil
}

// LockRoots implements manager.LockMaintainer: one root per Gradle build with lockfiles,
// the build root's settings script (else its build script, else the first of its files) as
// the manifest and the root project's gradle.lockfile (else the build's first lockfile) as
// the lockfile. Builds that only verify dependencies have none.
func (*gradleManager) LockRoots(root string, files []string) []manager.LockRoot {
	type build struct {
		manifest string
		rank     int
		lockfile string
	}
	builds := map[string]*build{}
	locks := map[string][]string{} // lockfiles per build root, each build is scanned once
	for _, f := range slices.Sorted(slices.Values(files)) {
		dir := jvmtool.GradleRoot(root, f)
		lockfiles, ok := locks[dir]
		if !ok {
			lockfiles = jvmtool.GradleLockfiles(root, dir)
			locks[dir] = lockfiles
		}
		if len(lockfiles) == 0 {
			continue
		}
		rank := 2
		if path.Dir(f) == dir {
			switch kindOf(f) {
			case kindSettings:
				rank = 0
			case kindScript:
				rank = 1
			}
		}
		if b, ok := builds[dir]; ok {
			if rank < b.rank {
				b.manifest, b.rank = f, rank
			}
			continue
		}
		lf := lockfiles[0]
		if slices.Contains(lockfiles, lockfileName) {
			lf = lockfileName
		}
		builds[dir] = &build{manifest: f, rank: rank, lockfile: path.Join(dir, lf)}
	}
	out := make([]manager.LockRoot, 0, len(builds))
	for _, b := range builds {
		out = append(out, manager.LockRoot{Manifest: b.manifest, Lockfile: b.lockfile})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}
