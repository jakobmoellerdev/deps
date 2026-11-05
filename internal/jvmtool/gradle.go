package jvmtool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// Gradle files GradleLocker reads and regenerates.
const (
	Lockfile             = "gradle.lockfile"
	BuildscriptLockfile  = "buildscript-gradle.lockfile"
	SettingsLockfile     = "settings-gradle.lockfile"
	legacyLocksDir       = "gradle/dependency-locks" // one <configuration>.lockfile each (Gradle < 6.4)
	VerificationMetadata = "gradle/verification-metadata.xml"
	// Keyrings of PGP verification, refreshed with --export-keys when present.
	KeysKeyring = "gradle/verification-keyring.keys"
	GPGKeyring  = "gradle/verification-keyring.gpg"
)

var settingsFiles = []string{"settings.gradle", "settings.gradle.kts"}

// buildFiles mark a Gradle project directory.
var buildFiles = []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"}

// verificationTypes are the --write-verification-metadata types, in the order they are
// passed, with the element that marks each in verification-metadata.xml.
var verificationTypes = [...]struct{ name, elem string }{
	{"sha256", "<sha256 "}, {"sha512", "<sha512 "}, {"sha1", "<sha1 "}, {"md5", "<md5 "}, {"pgp", "<pgp "},
}

// GradleLocker is the gradle post-processor (engine.PostProcessor, engine.LockfileUpdater,
// engine.LockMaintainer,
// engine.ScopeSiblingLister): it rewrites the dependency lockfiles and the dependency
// verification metadata of a Gradle build after deps patched its build files, by running
// the build's `dependencies` tasks on a temporary copy.
type GradleLocker struct{}

// Scope is the build root, the nearest directory at or above the manifest with a settings
// script (else the manifest's directory, or the parent of a gradle/ directory); sources under
// buildSrc/ (shared versions) belong to the build above buildSrc, whose scripts use them. ok
// only when the build locks dependencies or verifies them.
func (GradleLocker) Scope(_ context.Context, root, manifest string) (string, bool) {
	dir := gradleRoot(root, manifest)
	l := scanGradle(filepath.Join(root, filepath.FromSlash(dir)), true)
	return dir, len(l.lockfiles) > 0 || l.verification
}

// gradleRoot returns the build root of manifest (slash paths relative to root).
func gradleRoot(root, manifest string) string {
	start := path.Dir(manifest)
	if src, ok := buildSrcSource(manifest); ok {
		start = src
	}
	for dir := start; ; dir = path.Dir(dir) {
		if path.Base(dir) == "buildSrc" || hasAny(filepath.Join(root, filepath.FromSlash(dir)), settingsFiles...) {
			return dir
		}
		if dir == "." {
			break
		}
	}
	if path.Base(start) == "gradle" {
		return path.Dir(start)
	}
	return start
}

// buildSrcSource returns the directory above buildSrc when manifest is a source file of a
// buildSrc build (below buildSrc/src/).
func buildSrcSource(manifest string) (string, bool) {
	i := strings.LastIndex("/"+manifest, "/buildSrc/src/")
	switch {
	case i < 0:
		return "", false
	case i == 0:
		return ".", true
	}
	return manifest[:i-1], true
}

// Siblings lists the files of a single-project build; ScopeSiblings lists every project's.
func (GradleLocker) Siblings() []string {
	return []string{Lockfile, BuildscriptLockfile, SettingsLockfile, VerificationMetadata, KeysKeyring, GPGKeyring}
}

// ScopeSiblings lists the lockfiles of every project of the build at dir, its verification
// metadata and keyrings.
func (GradleLocker) ScopeSiblings(dir string) []string {
	l := scanGradle(dir, false)
	if !l.verification {
		return l.lockfiles
	}
	names := l.lockfiles
	names = append(names, VerificationMetadata)
	for _, k := range [...]string{KeysKeyring, GPGKeyring} {
		if hasAny(dir, filepath.FromSlash(k)) {
			names = append(names, k)
		}
	}
	return names
}

// gradleLayout is what a build root contains of the files GradleLocker maintains.
type gradleLayout struct {
	lockfiles    []string // slash paths relative to the build root, sorted
	verification bool
}

// scanGradle finds the lockfiles below the build root dir, leaving out .git, .gradle,
// projects' build directories and nested builds (buildSrc and directories with a settings
// script); with first it stops at the first one.
func scanGradle(dir string, first bool) gradleLayout {
	var l gradleLayout
	l.verification = hasAny(dir, filepath.FromSlash(VerificationMetadata))
	if first && l.verification {
		return l
	}
	errStop := errors.New("stop")
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable parts of the tree are not scanned
		}
		if d.IsDir() {
			if p != dir && (gradleSkip(p, d) || d.Name() == "buildSrc" || hasAny(p, settingsFiles...)) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel = filepath.ToSlash(rel); isLockfile(rel) {
			l.lockfiles = append(l.lockfiles, rel)
			if first {
				return errStop
			}
		}
		return nil
	})
	slices.Sort(l.lockfiles)
	return l
}

// isLockfile reports whether the slash path is a Gradle lockfile.
func isLockfile(rel string) bool {
	dir, name := path.Split(rel)
	switch name {
	case Lockfile, BuildscriptLockfile, SettingsLockfile:
		return true
	}
	dir = strings.TrimSuffix(dir, "/")
	return strings.HasSuffix(name, ".lockfile") && (dir == legacyLocksDir || strings.HasSuffix(dir, "/"+legacyLocksDir))
}

// gradleSkip reports the directories at p that are not copied or scanned: .git, .gradle and
// a project's build directory.
func gradleSkip(p string, d fs.DirEntry) bool {
	switch d.Name() {
	case ".git", ".gradle":
		return true
	case "build":
		return hasAny(filepath.Dir(p), buildFiles...)
	}
	return false
}

// lockTasks returns the tasks that resolve the locked configurations of lockfiles:
// `<project>:dependencies` per project with a lockfile, `<project>:buildEnvironment` for
// buildscript locks. The project path is derived from the directory (":" root, ":a:b" for
// a/b).
func lockTasks(lockfiles []string) []string {
	tasks := make([]string, 0, len(lockfiles))
	for _, lf := range lockfiles {
		dir, name := path.Split(lf)
		dir = strings.TrimSuffix(dir, "/")
		task := "dependencies"
		if p, ok := strings.CutSuffix(dir, legacyLocksDir); ok && (p == "" || strings.HasSuffix(p, "/")) {
			dir = strings.TrimSuffix(p, "/")
			if name == "buildscript-classpath.lockfile" {
				task = "buildEnvironment"
			}
		} else if name == BuildscriptLockfile {
			task = "buildEnvironment"
		}
		project := ":"
		if dir != "" {
			project = ":" + strings.ReplaceAll(dir, "/", ":") + ":"
		}
		tasks = append(tasks, project+task)
	}
	slices.Sort(tasks)
	return slices.Compact(tasks)
}

// verificationArg returns the --write-verification-metadata value for the checksum kinds
// present in a verification-metadata.xml (pgp with signature entries or verification); sha256
// when it has none.
func verificationArg(meta []byte) string {
	var kinds []string
	for _, t := range verificationTypes {
		if bytes.Contains(meta, []byte(t.elem)) || t.name == "pgp" && bytes.Contains(meta, []byte("<verify-signatures>true<")) {
			kinds = append(kinds, t.name)
		}
	}
	if len(kinds) == 0 || len(kinds) == 1 && kinds[0] == "pgp" {
		kinds = append([]string{"sha256"}, kinds...)
	}
	return strings.Join(kinds, ",")
}

// lockModule is the module coordinate Gradle locks for a dep: group:artifact, or the plugin
// marker `<id>:<id>.gradle.plugin` of a plugin id.
func lockModule(d dep.Dep) string {
	if strings.Contains(d.Name, ":") {
		return d.Name
	}
	return d.Name + ":" + d.Name + ".gradle.plugin"
}

// Process rewrites the lockfiles and verification metadata of the build at dir for the
// patched manifests (`--write-locks`).
func (g GradleLocker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return g.ProcessLocked(ctx, dir, manifests, files, nil)
}

// ProcessLocked is Process plus lockfile-only updates: without a manifest change only their
// modules move (`--update-locks g:a,…`).
func (GradleLocker) ProcessLocked(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
	lockfiles := lockfilesOf(files)
	if len(files) == 0 {
		return nil, nil
	}
	if len(locks) > 0 && len(lockfiles) == 0 {
		return nil, fmt.Errorf("lockfile-only updates need a %s", Lockfile)
	}
	return runGradle(ctx, dir, manifests, files, lockfiles, toolrun.Changed(dir, manifests), locks)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `--write-locks` on every
// locked project's `dependencies`/`buildEnvironment` tasks re-resolves all their
// configurations, moving dynamic versions to the newest they admit, and regenerates the
// verification metadata when the build has it; build files are unchanged. It returns the
// changed lockfiles and metadata; without a lockfile it does nothing.
func (GradleLocker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lockfiles := lockfilesOf(files)
	if len(lockfiles) == 0 {
		return nil, nil
	}
	out, err := runGradle(ctx, dir, nil, files, lockfiles, true, nil)
	if len(out) == 0 {
		return nil, err
	}
	return out, err
}

// GradleLockRoot returns the build root of manifest (slash paths relative to root) and the
// lockfiles of its projects (relative to the build root, sorted).
func GradleLockRoot(root, manifest string) (string, []string) {
	dir := gradleRoot(root, manifest)
	return dir, scanGradle(filepath.Join(root, filepath.FromSlash(dir)), false).lockfiles
}

// lockfilesOf returns the sorted lockfile names of files.
func lockfilesOf(files map[string][]byte) []string {
	var lockfiles []string
	for n := range files {
		if isLockfile(n) {
			lockfiles = append(lockfiles, n)
		}
	}
	slices.Sort(lockfiles)
	return lockfiles
}

// runGradle runs Gradle (gradleArgs) on a temporary copy of the build at dir with manifests
// overlaid and returns the files that changed.
func runGradle(ctx context.Context, dir string, manifests, files map[string][]byte, lockfiles []string, changed bool, locks map[string][]dep.Update) (map[string][]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	target := VerificationMetadata
	if len(lockfiles) > 0 {
		target = lockfiles[0]
	}
	if err := checkJava(); err != nil {
		return nil, fmt.Errorf("java not found (needed to update %s)", target)
	}
	tmp, err := copyBuild(dir, manifests, gradleSkip)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(tmp))
	tool, err := toolPath(tmp, "gradlew", "gradle")
	if err != nil {
		return nil, fmt.Errorf("gradle not found (needed to update %s)", target)
	}
	keyring := files[KeysKeyring] != nil || files[GPGKeyring] != nil
	argv := gradleArgs(tool, lockfiles, files[VerificationMetadata], keyring, changed, locks)
	if err := run(ctx, tmp, argv); err != nil {
		return nil, fmt.Errorf("gradle in %s: %w", dir, err)
	}
	return readBack(tmp, files, names...)
}

// gradleArgs is the command line of a GradleLocker run. meta is the verification metadata,
// nil when the build has none; with PGP entries and a keyring the keys are exported too.
func gradleArgs(tool string, lockfiles []string, meta []byte, keyring, changed bool, locks map[string][]dep.Update) []string {
	argv := []string{tool, "--console=plain", "--no-daemon", "-q"}
	if len(lockfiles) > 0 {
		argv = append(argv, lockTasks(lockfiles)...)
		if changed || len(locks) == 0 {
			argv = append(argv, "--write-locks")
		} else {
			var mods []string
			for _, ups := range locks {
				for _, u := range ups {
					mods = append(mods, lockModule(u.Dep))
				}
			}
			slices.Sort(mods)
			argv = append(argv, "--update-locks", strings.Join(slices.Compact(mods), ","))
		}
	} else {
		argv = append(argv, "dependencies")
	}
	if meta != nil {
		types := verificationArg(meta)
		argv = append(argv, "--write-verification-metadata", types, "--dependency-verification", "lenient")
		if keyring && strings.HasSuffix(types, "pgp") {
			argv = append(argv, "--export-keys")
		}
	}
	return argv
}
