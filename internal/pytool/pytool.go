// Package pytool runs the Python package managers where deps cannot avoid it: regenerating
// uv.lock, poetry.lock, pdm.lock and Pipfile.lock after deps patched pyproject.toml or Pipfile,
// and the `--hash` options of requirements files (compile.go).
package pytool

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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// Manifests and lockfiles.
const (
	Pyproject   = "pyproject.toml"
	Pipfile     = "Pipfile"
	UVLock      = "uv.lock"
	PoetryLock  = "poetry.lock"
	PipfileLock = "Pipfile.lock"
	PDMLock     = "pdm.lock"
)

// maxCopy is the largest top-level file of a directory copied for pip-compile (compile.go)
// and listed as a lock input (ScopeInputs).
const maxCopy = 4 << 20

// Locker is the post-processor (engine.PostProcessor) of the pep621, poetry and pipenv
// managers: it regenerates the lockfile of a project with its tool in a temporary copy.
type Locker struct{}

// Siblings lists the lockfiles.
func (Locker) Siblings() []string { return []string{UVLock, PoetryLock, PDMLock, PipfileLock} }

// Scope returns the lock directory of manifest: a Pipfile's own directory when it holds
// Pipfile.lock; a pyproject.toml's own directory when it holds uv.lock, poetry.lock or
// pdm.lock, else the nearest directory above (within root) with uv.lock whose
// `[tool.uv.workspace]` members match the manifest's directory.
func (Locker) Scope(ctx context.Context, root, manifest string) (string, bool) {
	return scope(ctx, root, manifest, nil)
}

// scope is Scope with the uv workspaces read so far (nil: none kept).
func scope(ctx context.Context, root, manifest string, ws workspaces) (string, bool) {
	member := path.Dir(manifest)
	abs := func(dir, name string) string { return filepath.Join(root, filepath.FromSlash(dir), name) }
	switch path.Base(manifest) {
	case Pipfile:
		return member, isFile(abs(member, PipfileLock))
	case Pyproject:
	default:
		return "", false
	}
	if isFile(abs(member, UVLock)) || isFile(abs(member, PoetryLock)) || isFile(abs(member, PDMLock)) {
		return member, true
	}
	for dir := member; dir != "."; {
		dir = path.Dir(dir)
		if !isFile(abs(dir, UVLock)) {
			continue
		}
		w := ws.get(root, dir)
		if w.unreadable {
			return "", false
		}
		rel := member
		if dir != "." {
			rel = strings.TrimPrefix(member, dir+"/")
		}
		ok := matchAny(w.include, rel) && !matchAny(w.exclude, rel)
		if !ok {
			slog.DebugContext(ctx, "not a uv workspace member", "dir", dir, "manifest", manifest)
		}
		return dir, ok
	}
	return "", false
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// workspaces holds the `[tool.uv.workspace]` globs of uv workspace roots by directory, so the
// members of one workspace read and parse its pyproject.toml once.
type workspaces map[string]*uvWorkspace

type uvWorkspace struct {
	include, exclude []string
	unreadable       bool // reading pyproject.toml failed
}

// get returns the workspace of the uv.lock in dir, reading its pyproject.toml unless ws has it.
func (ws workspaces) get(root, dir string) *uvWorkspace {
	if w, ok := ws[dir]; ok {
		return w
	}
	w := &uvWorkspace{}
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), Pyproject))
	if err != nil {
		w.unreadable = true
	} else {
		w.include, w.exclude = workspaceGlobs(src)
	}
	if ws != nil {
		ws[dir] = w
	}
	return w
}

// workspaceGlobs returns the `[tool.uv.workspace]` members and exclude globs of a
// pyproject.toml.
func workspaceGlobs(src []byte) (include, exclude []string) {
	doc, err := tomlspan.Parse(src)
	if err != nil {
		return nil, nil
	}
	globs := func(key string) []string {
		vs := doc.Get("tool", "uv", "workspace", key).Strings()
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			out = append(out, path.Clean(v.Str))
		}
		return out
	}
	return globs("members"), globs("exclude")
}

func matchAny(globs []string, rel string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, rel); ok {
			return true
		}
	}
	return false
}

// Process runs the lock command of the scope's lockfile in a mirror of dir (lockIn) with the
// patched manifests and returns the lockfile when it changed. Without a lockfile it does
// nothing.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessLocked(ctx, dir, manifests, files, nil)
}

// ProcessLocked is Process plus the lockfile-only updates of the lock root (dep.Update.Lockfile,
// by manifest path relative to dir), each moved to exactly its new version:
//   - uv.lock and Pipfile.lock: after the plain lock command (only when a manifest changed or
//     there are no lockfile-only updates), the tool pins them (lockOnlyCmds);
//   - poetry.lock and pdm.lock, whose update commands only move to the newest version the
//     constraints admit: the lock command runs with every updated dep's constraint replaced
//     by `==<version>` (pinManifests), then again with the manifests restored, which keeps
//     those versions (they are in range) and recomputes the lockfile's content hash.
func (Locker) ProcessLocked(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
	var lockName string
	var c cmdSpec
	switch {
	case manifests[Pipfile] != nil && files[PipfileLock] != nil:
		lockName, c = PipfileLock, pipenvCmd()
	case files[UVLock] != nil:
		lockName, c = UVLock, uvCmd()
	case files[PoetryLock] != nil:
		lockName, c = PoetryLock, poetryCmd(ctx)
	case files[PDMLock] != nil:
		lockName, c = PDMLock, pdmCmd()
	case len(locks) > 0:
		return nil, errors.New("lockfile-only updates without a lockfile")
	default:
		return nil, nil
	}
	if len(locks) > 0 && (lockName == PoetryLock || lockName == PDMLock) {
		pinned, restore, err := pinManifests(dir, manifests, locks)
		if err != nil {
			return nil, err
		}
		return lockIn(ctx, dir, lockName, manifests, files, []lockStep{{cmd: c, write: pinned}, {cmd: c, write: restore}})
	}
	var pipfile []byte
	if lockName == PipfileLock && len(locks) > 0 {
		var err error
		if pipfile, err = os.ReadFile(filepath.Join(dir, Pipfile)); err != nil {
			return nil, err
		}
	}
	runs := lockOnlyCmds(lockName, pipfile, c, locks)
	if len(locks) == 0 || toolrun.Changed(dir, manifests) {
		runs = slices.Insert(runs, 0, c)
	}
	steps := make([]lockStep, len(runs))
	for i, r := range runs {
		steps[i].cmd = r
	}
	return lockIn(ctx, dir, lockName, manifests, files, steps)
}

// pinManifests returns the manifests holding lockfile-only updates (by path relative to dir)
// with each updated dep's constraint replaced by `==<version>`, and their content without
// the pins (the patched manifest, else the file in dir).
//
// The spans of the updates were taken on the file in the checkout, while the manifest to pin
// is the patched one, where a bump of a different length has moved everything behind it. Each
// span is therefore mapped onto the patched manifest through the TOML value holding it
// (spanMap).
func pinManifests(dir string, manifests map[string][]byte, locks map[string][]dep.Update) (pinned, restore map[string][]byte, err error) {
	pinned, restore = map[string][]byte{}, map[string][]byte{}
	for m, ups := range locks {
		orig, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(m)))
		if err != nil {
			return nil, nil, err
		}
		src := orig
		if patched := manifests[m]; patched != nil {
			src = patched
		}
		move, err := spanMap(orig, src)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", m, err)
		}
		edits := make([]patch.Edit, 0, len(ups))
		for _, u := range ups {
			sp := u.Dep.RefSpan
			at, ok := move(sp)
			if !ok {
				return nil, nil, fmt.Errorf("%s: cannot pin %s to %s: no constraint", m, u.Dep.Name, u.NewVersion)
			}
			old := string(orig[sp.Start:sp.End])
			edits = append(edits, patch.Edit{Start: at, End: at + len(old), Old: old, New: "==" + u.NewVersion})
		}
		out, err := patch.Apply(slices.Clone(src), edits)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", m, err)
		}
		pinned[m], restore[m] = out, src
	}
	return pinned, restore, nil
}

// spanMap maps a span of orig onto patched, which holds the same TOML document with some of
// its values replaced in place: an offset inside a value both share keeps its offset within
// that value. The mapping reports false for a span outside any string or scalar value, one
// the patch changed, and one outside orig.
func spanMap(orig, patched []byte) (func(dep.Span) (int, bool), error) {
	inOrig := func(s dep.Span) bool { return s.Start >= 0 && s.Start < s.End && s.End <= len(orig) }
	if bytes.Equal(orig, patched) {
		return func(s dep.Span) (int, bool) { return s.Start, inOrig(s) }, nil
	}
	from, err := tomlspan.Parse(orig)
	if err != nil {
		return nil, err
	}
	to, err := tomlspan.Parse(patched)
	if err != nil {
		return nil, err
	}
	was, is := atoms(from, nil), atoms(to, nil)
	if len(was) != len(is) {
		return nil, fmt.Errorf("the patched manifest holds %d values, the file %d", len(is), len(was))
	}
	return func(s dep.Span) (int, bool) {
		if !inOrig(s) {
			return 0, false
		}
		for i, v := range was {
			if v.Start > s.Start || s.End > v.End {
				continue
			}
			w := is[i]
			if !bytes.Equal(orig[v.Start:v.End], patched[w.Start:w.End]) {
				return 0, false
			}
			return w.Start + s.Start - v.Start, true
		}
		return 0, false
	}, nil
}

// atoms appends the string and scalar values below v to out, in document order: the same
// values in the same order for a document and its patched copy.
func atoms(v *tomlspan.Value, out []*tomlspan.Value) []*tomlspan.Value {
	switch v.Kind {
	case tomlspan.String, tomlspan.Scalar:
		return append(out, v)
	case tomlspan.Array:
		for _, it := range v.Items {
			out = atoms(it, out)
		}
	case tomlspan.Table:
		for _, k := range v.Keys {
			out = atoms(v.Fields[k], out)
		}
	}
	return out
}

// lockStep is one command lockIn runs and the files (slash paths relative to dir) it writes
// into the mirror before.
type lockStep struct {
	cmd   cmdSpec
	write map[string][]byte
}

// lockIn runs steps in a mirror of dir (mirrorScope) holding files, manifests and the files
// the steps write, and returns lockName when it changed from files[lockName].
func lockIn(ctx context.Context, dir, lockName string, manifests, files map[string][]byte, steps []lockStep) (map[string][]byte, error) {
	write := maps.Clone(files)
	maps.Copy(write, manifests)
	for _, s := range steps {
		for p, b := range s.write {
			if _, ok := write[p]; !ok {
				write[p] = b // a real file in the mirror, not a link into dir
			}
		}
	}
	tmp, err := os.MkdirTemp("", "deps-python-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	base, err := mirrorScope(dir, tmp, write)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		if err := toolrun.WriteFiles(base, s.write); err != nil {
			return nil, err
		}
		c := s.cmd
		if err := c.run(ctx, base); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return nil, fmt.Errorf("%s not found (needed to update %s)", c.tool, lockName)
			}
			return nil, fmt.Errorf("%s %s in %s: %w", c.tool, strings.Join(c.args, " "), dir, err)
		}
	}
	out, err := os.ReadFile(filepath.Join(base, lockName))
	if err != nil {
		return nil, err
	}
	if bytes.Equal(out, files[lockName]) {
		return nil, nil
	}
	return map[string][]byte{lockName: out}, nil
}

// mirrorScope builds the tree the lock commands run in below tmp and returns the mirror of
// dir: the repository ancestors are mirrored (toolrun.MirrorAncestors), so path dependencies
// outside dir resolve; every entry of dir is symlinked (linkTree) except the files of write,
// which are written, and `.venv`; the repository's `.git` is linked at the mirrored top, so
// build backends that derive the version from git (setuptools-scm, hatch-vcs, …) can build
// the project's metadata. Build backends then see the checkout's sources, local backends
// (`backend-path`) and workspace members as they are.
func mirrorScope(dir, tmp string, write map[string][]byte) (string, error) {
	base, err := toolrun.MirrorAncestors(dir, tmp)
	if err != nil {
		return "", err
	}
	// The nearest repository of dir (dir's own, else the top MirrorAncestors mirrored at tmp).
	for top, at := dir, base; ; top, at = filepath.Dir(top), filepath.Dir(at) {
		if _, err := os.Lstat(filepath.Join(top, ".git")); err == nil {
			if err := os.Symlink(filepath.Join(top, ".git"), filepath.Join(at, ".git")); err != nil {
				return "", err
			}
			break
		}
		if at == tmp {
			break
		}
	}
	if err := linkTree(dir, base, write); err != nil {
		return "", err
	}
	return base, toolrun.WriteFiles(base, write)
}

// linkTree symlinks every entry of src into dst except `.git`, `.venv` and the paths of
// write (slash paths relative to src): a written file is left out, a directory holding
// written files is created and linked the same way, so no write reaches the checkout.
func linkTree(src, dst string, write map[string][]byte) error {
	written := map[string]bool{}
	nested := map[string]map[string][]byte{}
	for p, b := range write {
		first, rest, ok := strings.Cut(p, "/")
		if !ok {
			written[first] = true
			continue
		}
		if nested[first] == nil {
			nested[first] = map[string][]byte{}
		}
		nested[first][rest] = b
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		name := e.Name()
		switch sub := nested[name]; {
		case name == ".git" || name == ".venv" || written[name]:
		case sub != nil:
			if err := os.Mkdir(filepath.Join(dst, name), 0o755); err != nil {
				return err
			}
			if e.IsDir() {
				if err := linkTree(filepath.Join(src, name), filepath.Join(dst, name), sub); err != nil {
					return err
				}
			}
		default:
			if err := os.Symlink(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// lockOnlyCmds returns the commands that move the lockfile-only updates of a uv.lock or
// Pipfile.lock (pipfile is the unpatched Pipfile of a Pipfile.lock) to exactly their new
// versions, with base's tool, environment and uvx fallback:
//   - uv.lock: `uv lock --no-progress --upgrade-package <name>==<version>…`
//   - Pipfile.lock: `pipenv upgrade --lock-only --categories <category> <name>==<version>…`
//     per Pipfile category
func lockOnlyCmds(lockName string, pipfile []byte, base cmdSpec, locks map[string][]dep.Update) []cmdSpec {
	if len(locks) == 0 {
		return nil
	}
	// Arguments by command (one per Pipfile category, else one).
	byCmd := map[string][]string{}
	var category func(off int) string
	if lockName == PipfileLock {
		category = pipfileCategories(pipfile)
	}
	for _, m := range slices.Sorted(maps.Keys(locks)) {
		for _, u := range locks[m] {
			pin := u.Dep.Name + "==" + u.NewVersion
			if lockName == UVLock {
				byCmd[""] = append(byCmd[""], "--upgrade-package", pin)
				continue
			}
			cat := category(u.Dep.RefSpan.Start)
			byCmd[cat] = append(byCmd[cat], pin)
		}
	}
	var out []cmdSpec
	for _, k := range slices.Sorted(maps.Keys(byCmd)) {
		c := base
		if lockName == UVLock {
			c.args = []string{"lock", "--no-progress"}
		} else {
			c.args = []string{"upgrade", "--lock-only", "--categories", k}
		}
		c.args = append(c.args, byCmd[k]...)
		out = append(out, c)
	}
	return out
}

// pipfileCategories parses a Pipfile once and returns the Pipfile table whose entry holds
// a byte offset of src (`packages`, `dev-packages` or a custom category), or "".
func pipfileCategories(src []byte) func(off int) string {
	doc, err := tomlspan.Parse(src)
	if err != nil {
		return func(int) string { return "" }
	}
	return func(off int) string {
		for _, k := range doc.Keys {
			if t := doc.Fields[k]; t.Kind == tomlspan.Table {
				for _, f := range t.Fields {
					if f.Start <= off && off <= f.End {
						return k
					}
				}
			}
		}
		return ""
	}
}

// topLevel reads the regular files directly in dir (README, LICENSE, … that project metadata
// may reference), skipping larger ones.
func topLevel(dir string) (map[string][]byte, error) {
	names, err := topLevelNames(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(names))
	for _, n := range names {
		if out[n], err = os.ReadFile(filepath.Join(dir, n)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// topLevelNames lists the files topLevel reads.
func topLevelNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && info.Size() <= maxCopy {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// ScopeInputs implements engine.ScopeInputLister: what decides the lockfile besides the
// manifests and lockfiles, the top-level files of dir (up to 4 MiB each; project metadata
// such as a readme) and, with uv.lock, every workspace member's pyproject.toml. lockIn
// mirrors all of dir, but the other files (sources, a local build backend, git) only let
// a backend build the project's metadata and do not change the locked versions.
func (Locker) ScopeInputs(_ context.Context, root, dir string, _ []string) (paths, digests []string, err error) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	names, err := topLevelNames(abs)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range names {
		paths = append(paths, path.Join(dir, n))
	}
	if !isFile(filepath.Join(abs, UVLock)) {
		return paths, nil, nil
	}
	pyproject, err := os.ReadFile(filepath.Join(abs, Pyproject))
	if errors.Is(err, fs.ErrNotExist) {
		return paths, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	write := map[string][]byte{Pyproject: pyproject}
	if err := members(abs, write); err != nil {
		return nil, nil, err
	}
	for p := range write {
		paths = append(paths, path.Join(dir, p))
	}
	return paths, nil, nil
}

// members adds the pyproject.toml of every `[tool.uv.workspace]` member of dir to write.
func members(dir string, write map[string][]byte) error {
	root := write[Pyproject]
	if root == nil {
		return nil
	}
	include, exclude := workspaceGlobs(root)
	fsys := os.DirFS(dir)
	for _, g := range include {
		matches, err := doublestar.Glob(fsys, g)
		if err != nil {
			return fmt.Errorf("workspace members %q: %w", g, err)
		}
		for _, m := range matches {
			p := path.Join(m, Pyproject)
			if m == "." || matchAny(exclude, m) || !isFile(filepath.Join(dir, filepath.FromSlash(p))) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
			if err != nil {
				return err
			}
			write[p] = b
		}
	}
	return nil
}

// cmdSpec is how one lockfile is regenerated: tool from PATH, else `uvx --from <pkg>`.
type cmdSpec struct {
	tool string
	args []string // after the tool
	env  []string
	uvx  bool   // run through uvx when tool is not in PATH
	from string // package uvx installs the tool from; "" = tool
	// python is the Python version the tool must run with (`uvx --python`, which installs
	// it when missing); uvx is then preferred over a tool in PATH, which runs with its own
	// interpreter. "" = any.
	python string
}

func uvCmd() cmdSpec {
	return cmdSpec{tool: "uv", args: []string{"lock", "--no-progress"}}
}

// poetryV1s caches by path whether a poetry binary is a 1.x, so that the probe runs once per
// process instead of once per lock root.
var poetryV1s sync.Map

// poetryCmd keeps the locked versions: Poetry 1 needs --no-update, Poetry 2 removed the flag
// and keeps them by default. A poetry found only through uvx is the latest, so Poetry 2.
func poetryCmd(ctx context.Context) cmdSpec {
	c := poetryTool()
	p, err := exec.LookPath("poetry")
	if err != nil {
		return c
	}
	v1, ok := poetryV1s.Load(p)
	if !ok {
		v1 = readPoetryV1(ctx, p, c.env)
		poetryV1s.Store(p, v1)
	}
	if v1.(bool) {
		c.args = append(c.args, "--no-update")
	}
	return c
}

// readPoetryV1 reports whether `bin --version` names a 1.x version; a version that cannot be
// read counts as Poetry 2.
func readPoetryV1(ctx context.Context, bin string, env []string) bool {
	cmd, cancel := toolrun.Command(ctx, bin, "--version")
	defer cancel()
	cmd.Env = toolrun.Env(env...)
	out, err := cmd.Output()
	return err == nil && poetryV1(string(out))
}

func poetryTool() cmdSpec {
	return cmdSpec{tool: "poetry", args: []string{"lock"}, env: []string{"POETRY_NO_INTERACTION=1"}, uvx: true}
}

// poetryV1 reports whether `poetry --version` output names a 1.x version
// ("Poetry (version 1.8.3)").
func poetryV1(out string) bool {
	_, v, ok := strings.Cut(out, "version ")
	return ok && strings.HasPrefix(v, "1.")
}

func pipenvCmd() cmdSpec {
	return cmdSpec{tool: "pipenv", args: []string{"lock"}, uvx: true, env: []string{
		"PIPENV_VENV_IN_PROJECT=1", "PIPENV_IGNORE_VIRTUALENVS=1", "PIPENV_NOSPIN=1", "PIPENV_YES=1", "PIPENV_VERBOSITY=-1",
	}}
}

// argv resolves the command: the tool from PATH, else (when allowed) uvx running it; with a
// python version uvx first.
func (c cmdSpec) argv() ([]string, error) {
	uvx, uvxErr := exec.LookPath("uvx")
	if c.uvx && c.python != "" && uvxErr == nil {
		return c.uvxArgv(uvx), nil
	}
	if p, err := exec.LookPath(c.tool); err == nil {
		return append([]string{p}, c.args...), nil
	}
	if !c.uvx || uvxErr != nil {
		return nil, exec.ErrNotFound
	}
	return c.uvxArgv(uvx), nil
}

func (c cmdSpec) uvxArgv(uvx string) []string {
	argv := []string{uvx}
	if c.python != "" {
		argv = append(argv, "--python", c.python)
	}
	return append(append(argv, "--from", cmp.Or(c.from, c.tool), c.tool), c.args...)
}

// toolError is a failed command: Error is the last 20 lines of its output (or the exit
// error without output), out the output up to its last 32 KiB.
type toolError struct {
	msg string
	out []byte
	err error
}

func (e *toolError) Error() string { return e.msg }

func (e *toolError) Unwrap() error { return e.err }

// run executes the command in dir; a failure is a *toolError.
func (c cmdSpec) run(ctx context.Context, dir string) error {
	argv, err := c.argv()
	if err != nil {
		return err
	}
	cmd, cancel := toolrun.Command(ctx, argv[0], argv[1:]...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env(c.env...)
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "python tool command", "argv", argv, "dir", dir, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	return &toolError{msg: cmp.Or(toolrun.Tail(out.Bytes(), 20), err.Error()), out: out.Bytes(), err: err}
}
