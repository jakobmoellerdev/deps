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
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
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

// maxCopy is the largest top-level file of a scope copied into the temporary directory.
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
		src, err := os.ReadFile(abs(dir, Pyproject))
		if err != nil {
			return "", false
		}
		rel := member
		if dir != "." {
			rel = strings.TrimPrefix(member, dir+"/")
		}
		include, exclude := workspaceGlobs(src)
		ok := matchAny(include, rel) && !matchAny(exclude, rel)
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

// Process copies the scope's top-level files (up to 4 MiB each, the patched content for
// manifests) and, for a uv workspace, every member's pyproject.toml into a temporary
// directory, runs the lock command of the lockfile there and returns the lockfile when it
// changed. Without a lockfile it does nothing.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessLocked(ctx, dir, manifests, files, nil)
}

// ProcessLocked is Process plus the lockfile-only updates of the lock root (dep.Update.Lockfile,
// by manifest path relative to dir): after the plain lock command (only when a manifest
// changed or there are no lockfile-only updates), the lockfile's tool moves them
// (lockOnlyCmds).
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
	var pipfile []byte
	if lockName == PipfileLock && len(locks) > 0 {
		var err error
		if pipfile, err = os.ReadFile(filepath.Join(dir, Pipfile)); err != nil {
			return nil, err
		}
	}
	runs := lockOnlyCmds(lockName, files[lockName], pipfile, c, locks)
	if len(locks) == 0 || toolrun.Changed(dir, manifests) {
		runs = slices.Insert(runs, 0, c)
	}
	return lockIn(ctx, dir, lockName, manifests, files, runs)
}

// lockIn copies the scope's top-level files, for uv.lock every workspace member's
// pyproject.toml, then files and manifests (replacing theirs) into a temporary directory,
// runs cmds there and returns lockName when it changed from files[lockName].
func lockIn(ctx context.Context, dir, lockName string, manifests, files map[string][]byte, cmds []cmdSpec) (map[string][]byte, error) {
	write, err := topLevel(dir)
	if err != nil {
		return nil, err
	}
	if lockName == UVLock {
		if err := members(dir, write); err != nil {
			return nil, err
		}
	}
	maps.Copy(write, files)
	maps.Copy(write, manifests)
	tmp, err := os.MkdirTemp("", "deps-python-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := toolrun.WriteFiles(tmp, write); err != nil {
		return nil, err
	}
	for _, c := range cmds {
		if err := c.run(ctx, tmp); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return nil, fmt.Errorf("%s not found (needed to update %s)", c.tool, lockName)
			}
			return nil, fmt.Errorf("%s %s in %s: %w", c.tool, strings.Join(c.args, " "), dir, err)
		}
	}
	out, err := os.ReadFile(filepath.Join(tmp, lockName))
	if err != nil {
		return nil, err
	}
	if bytes.Equal(out, files[lockName]) {
		return nil, nil
	}
	return map[string][]byte{lockName: out}, nil
}

// lockOnlyCmds returns the commands that move the lockfile-only updates in lockName (whose
// content is lock; pipfile is the unpatched Pipfile of a Pipfile.lock), with base's tool,
// environment and uvx fallback:
//   - uv.lock: `uv lock --no-progress --upgrade-package <name>==<version>…`
//   - poetry.lock: `poetry update --lock <name>…` (Poetry 1 and 2; Poetry picks the newest
//     version the constraints admit)
//   - pdm.lock: `pdm update --update-reuse --no-sync -G <group> <name>…` per lock group of
//     the names (pdm rejects several groups with names; newest admitted version, too)
//   - Pipfile.lock: `pipenv upgrade --lock-only --categories <category> <name>==<version>…`
//     per Pipfile category
func lockOnlyCmds(lockName string, lock, pipfile []byte, base cmdSpec, locks map[string][]dep.Update) []cmdSpec {
	if len(locks) == 0 {
		return nil
	}
	// Arguments by command (one per Pipfile category or pdm group, else one).
	byCmd := map[string][]string{}
	var groups map[string]string
	var category func(off int) string
	switch lockName {
	case PDMLock:
		groups = pdmGroups(lock)
	case PipfileLock:
		category = pipfileCategories(pipfile)
	}
	for _, m := range slices.Sorted(maps.Keys(locks)) {
		for _, u := range locks[m] {
			pin := u.Dep.Name + "==" + u.NewVersion
			switch lockName {
			case UVLock:
				byCmd[""] = append(byCmd[""], "--upgrade-package", pin)
			case PipfileLock:
				cat := category(u.Dep.RefSpan.Start)
				byCmd[cat] = append(byCmd[cat], pin)
			default:
				g := groups[pyreq.Normalize(u.Dep.Name)]
				if lockName == PDMLock {
					g = cmp.Or(g, "default")
				}
				if !slices.Contains(byCmd[g], u.Dep.Name) {
					byCmd[g] = append(byCmd[g], u.Dep.Name)
				}
			}
		}
	}
	var out []cmdSpec
	for _, k := range slices.Sorted(maps.Keys(byCmd)) {
		c := base
		switch lockName {
		case UVLock:
			c.args = []string{"lock", "--no-progress"}
		case PipfileLock:
			c.args = []string{"upgrade", "--lock-only", "--categories", k}
		case PDMLock:
			c.args = []string{"update", "--update-reuse", "--no-sync", "-G", k}
		default:
			c.args = []string{"update", "--lock"}
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

// pdmGroups returns the lock group of each `[[package]]` of a pdm.lock by normalized name:
// "default" when it is one of its groups, else its first.
func pdmGroups(lock []byte) map[string]string {
	doc, err := tomlspan.Parse(lock)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	if a := doc.Get("package"); a != nil && a.Kind == tomlspan.Array {
		for _, p := range a.Items {
			n, gs := p.Get("name"), p.Get("groups").Strings()
			if n == nil || n.Kind != tomlspan.String || len(gs) == 0 {
				continue
			}
			k := pyreq.Normalize(n.Str)
			if out[k] == "default" {
				continue
			}
			out[k] = gs[0].Str
			for _, g := range gs {
				if g.Str == "default" {
					out[k] = "default"
				}
			}
		}
	}
	return out
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

// ScopeInputs implements engine.ScopeInputLister: the files lockIn copies besides the
// manifests and lockfiles, the top-level files of dir (up to 4 MiB each) and, with uv.lock,
// every workspace member's pyproject.toml.
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

// poetryCmd keeps the locked versions: Poetry 1 needs --no-update, Poetry 2 removed the flag
// and keeps them by default. A poetry found only through uvx is the latest, so Poetry 2.
func poetryCmd(ctx context.Context) cmdSpec {
	c := poetryTool()
	if p, err := exec.LookPath("poetry"); err == nil {
		out, err := exec.CommandContext(ctx, p, "--version").Output()
		if err == nil && poetryV1(string(out)) {
			c.args = append(c.args, "--no-update")
		}
	}
	return c
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
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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
