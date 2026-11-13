// Package nodetool runs the JavaScript package managers where deps cannot avoid it:
// regenerating package-lock.json, npm-shrinkwrap.json, pnpm-lock.yaml, yarn.lock and bun.lock
// for npm, pnpm, yarn (classic and berry) and bun projects and workspaces, refreshing them
// (lock file maintenance), and applying lockfile-only updates to them. It also reads the
// versions they lock (ReadLocked).
package nodetool

import (
	"bytes"
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
	"time"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// Lockfiles Locker regenerates. npm 12 no longer reads or writes npm-shrinkwrap.json: it is
// installed as package-lock.json in the temporary copy, which has the same format.
const (
	PackageLock   = "package-lock.json"
	NpmShrinkwrap = "npm-shrinkwrap.json"
	PnpmLock      = "pnpm-lock.yaml"
	YarnLock      = "yarn.lock"
	BunLock       = "bun.lock"
)

const (
	npmrc         = ".npmrc"
	pnpmWorkspace = "pnpm-workspace.yaml"
	yarnrc        = ".yarnrc.yml"
	bunfig        = "bunfig.toml"
)

// lockfiles in the order a lock root's lockfile is picked when packageManager names none of
// the present ones; npm-shrinkwrap.json comes first, as npm preferred it over package-lock.json.
var lockfiles = []string{NpmShrinkwrap, PackageLock, PnpmLock, YarnLock, BunLock}

// toolOf maps a lockfile to the tool that maintains it.
var toolOf = map[string]string{NpmShrinkwrap: "npm", PackageLock: "npm", PnpmLock: "pnpm", YarnLock: "yarn", BunLock: "bun"}

// Locker is the npm post-processor (engine.PostProcessor): it regenerates the lockfile of a
// lock root (a project or workspace root) after deps patched package.json files in it, with
// the lockfile's package manager in a temporary directory. Scopes memoizes the lock root
// lookups of Scope, which the engine repeats for every patched file of every build of a run
// (nil: none).
type Locker struct{ Scopes *Scopes }

// Siblings lists the lockfiles and the (read-only) package manager configuration of a lock
// root.
func (Locker) Siblings() []string {
	return []string{NpmShrinkwrap, PackageLock, PnpmLock, YarnLock, BunLock, npmrc, pnpmWorkspace, yarnrc, bunfig}
}

// Process copies the lock root's package.json, every workspace member's package.json (the
// patched content for manifests), the lockfile, the package manager configuration and the
// local files they reference (addLocalFiles) into a temporary directory, updates the lockfile
// there without running scripts and returns it when it changed. Without a lockfile it does
// nothing.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessLocked(ctx, dir, manifests, files, nil)
}

// ProcessLocked is Process plus the lockfile-only updates of the lock root (dep.Update.Lockfile,
// by manifest path relative to dir), which move a locked version within the manifest's
// unchanged range (lockOnly). The manifests on disk are never touched: only the lockfile is
// read back from the temporary copy.
func (Locker) ProcessLocked(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
	return install(ctx, dir, manifests, files, locks, false)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): the lock root's lockfile is
// regenerated from scratch in the temporary copy, as Renovate does: package-lock.json,
// npm-shrinkwrap.json, pnpm-lock.yaml and bun.lock are left out, yarn.lock is copied empty (so
// yarn keeps the directory as its project root), and the plain install resolves every package,
// direct and transitive, of the root and its workspace members to the newest version its range
// admits. npm installs twice: its first lockfile can be out of sync with package.json
// (renovate#37531). The manifests are unchanged. It returns the lockfile when it changed;
// without a lockfile it does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	return install(ctx, dir, nil, files, nil, true)
}

// ScopeInputs implements engine.ScopeInputLister: the files Process copies besides the
// manifests and siblings (stage): the lock root's and its workspace members' package.json and
// the local files the configuration references (patches, the pnpmfile, `file:` and `link:`
// targets, the yarnPath and plugins), also above the lock root. They are derived from the
// unpatched manifests, since updates move versions, not local references.
func (l Locker) ScopeInputs(ctx context.Context, root, dir string, _ []string) (paths, digests []string, err error) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	files := map[string][]byte{}
	for _, n := range l.Siblings() {
		switch b, err := os.ReadFile(filepath.Join(abs, n)); {
		case err == nil:
			files[n] = b
		case !errors.Is(err, fs.ErrNotExist):
			return nil, nil, err
		}
	}
	s, err := stage(ctx, abs, nil, files, false)
	if err != nil || s == nil {
		return nil, nil, err
	}
	s.t.root.Close()
	for p := range s.write {
		paths = append(paths, path.Join(dir, p))
	}
	return paths, nil, nil
}

// staged is the temporary copy of a lock root (stage): write holds the files by slash path
// relative to the lock root, read through t, which the caller closes.
type staged struct {
	write             map[string][]byte
	lockName          string
	lock              []byte
	berry             bool
	pmTool, pmVersion string
	t                 *tree
}

// stage collects the files install copies for the lock root dir: its package.json (patched
// when in manifests), the lockfile (none for maintain, empty for a maintained yarn.lock),
// the package manager configuration, every workspace member's package.json, the yarn berry
// yarnPath and plugins, and the local files they reference (addLocalFiles); manifests
// replace their files. It is nil without a lockfile.
func stage(ctx context.Context, dir string, manifests, files map[string][]byte, maintain bool) (*staged, error) {
	rootPkg, ok := manifests["package.json"]
	if !ok {
		b, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			return nil, err
		}
		rootPkg = b
	}
	// A broken package.json is left to the package manager to report.
	m, _ := parseRoot(rootPkg)
	s := &staged{}
	s.pmTool, s.pmVersion = m.tool()
	s.lockName = pickLock(files, s.pmTool)
	if s.lockName == "" {
		return nil, nil
	}
	s.lock = files[s.lockName]

	s.write = map[string][]byte{"package.json": rootPkg, s.lockName: s.lock}
	switch {
	case maintain && s.lockName == YarnLock:
		s.write[s.lockName] = nil
	case maintain:
		delete(s.write, s.lockName)
	}
	for _, n := range []string{npmrc, pnpmWorkspace, yarnrc, bunfig} {
		if b := files[n]; b != nil {
			s.write[n] = b
		}
	}
	ws, err := parseWorkspaces(m, files[pnpmWorkspace])
	if err != nil {
		return nil, fmt.Errorf("workspaces: %w", err)
	}
	members, err := ws.members(dir)
	if err != nil {
		return nil, err
	}
	for _, p := range members {
		if s.write[p], err = os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			return nil, err
		}
	}
	maps.Copy(s.write, manifests)
	if s.t, err = openTree(dir); err != nil {
		return nil, err
	}
	s.berry = s.lockName == YarnLock && (files[yarnrc] != nil || bytes.Contains(s.lock, []byte("__metadata:")))
	if s.berry {
		paths, err := yarnFiles(files[yarnrc])
		if err != nil {
			s.t.root.Close()
			return nil, fmt.Errorf("%s: %w", yarnrc, err)
		}
		for _, p := range paths {
			if p = s.t.local(p); p == "" {
				continue
			}
			if s.write[p], err = s.t.read(p); err != nil {
				s.t.root.Close()
				return nil, err
			}
		}
	}
	addLocalFiles(ctx, s.t, s.write, files, s.lockName)
	return s, nil
}

// install runs ProcessLocked, or Maintain when maintain is set.
func install(ctx context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update, maintain bool) (map[string][]byte, error) {
	s, err := stage(ctx, dir, manifests, files, maintain)
	if err != nil || s == nil {
		return nil, err
	}
	defer s.t.root.Close()
	lockName, lock, t := s.lockName, s.lock, s.t
	// npm 12 ignores npm-shrinkwrap.json; the copy holds it as package-lock.json.
	tmpLock := lockName
	if lockName == NpmShrinkwrap {
		tmpLock = PackageLock
		if b, ok := s.write[NpmShrinkwrap]; ok {
			delete(s.write, NpmShrinkwrap)
			s.write[PackageLock] = b
		}
	}

	// npm names an unnamed root package after its directory (package-lock.json "name"), so the
	// copy lives in a directory named like the lock root, at its path below the repository top
	// (files above the lock root keep their relative paths).
	parent, err := os.MkdirTemp("", "deps-node-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(parent)
	tmp := filepath.Join(parent, filepath.Base(t.top), filepath.FromSlash(t.prefix))
	if err := toolrun.WriteFiles(tmp, s.write); err != nil {
		return nil, err
	}

	r := runner{ctx: ctx, dir: dir, tmp: tmp, lockName: lockName, c: command(lockName, s.berry)}
	if s.pmTool == r.c.tool {
		r.version = s.pmVersion
	}
	switch {
	case maintain && r.c.tool == "npm":
		err = r.runAll(r.c, r.c)
	case len(locks) == 0:
		err = r.runAll(r.c)
	default:
		err = r.lockOnly(s, locks, toolrun.Changed(dir, manifests))
	}
	if err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(tmp, tmpLock))
	if err != nil {
		return nil, err
	}
	if len(locks) > 0 && (toolOf[lockName] == "npm" || lockName == BunLock) {
		if out, err = restoreRanges(out, locks, lockName); err != nil {
			return nil, err
		}
	}
	if bytes.Equal(out, lock) {
		return nil, nil
	}
	return map[string][]byte{lockName: out}, nil
}

// runner runs package manager commands in the temporary copy tmp of the lock root dir.
type runner struct {
	ctx      context.Context
	dir, tmp string
	lockName string
	c        cmdSpec // the lockfile's install command
	version  string  // the packageManager version of c's tool
}

// runAll runs the commands in order; a failure names the tool and its subcommand.
func (r runner) runAll(cmds ...cmdSpec) error {
	for _, c := range cmds {
		if err := c.run(r.ctx, r.tmp, r.version); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return fmt.Errorf("%s not found (needed to update %s)", c.tool, r.lockName)
			}
			return fmt.Errorf("%s %s in %s: %w", c.tool, c.args[0], r.dir, err)
		}
	}
	return nil
}

// lockOnly applies the lockfile-only updates: npm and pnpm run the plain install first only
// when a manifest changed, then lockCommands. yarn and bun cannot install one version within a
// range, so they install from manifests whose updated ranges are pinned to the new versions
// (pinManifests): bun once, after which restoreRanges puts the ranges back into bun.lock; yarn
// moves each `<name>@<range>` descriptor to the entry of the pinned `<name>@<version>`
// (moveDescriptors) and installs again from the unpinned manifests, which keeps the moved
// descriptors and drops the pinned ones nothing else needs. Two installs do that for any
// number of updates, where berry's `yarn set resolution` would load the project once per
// descriptor.
func (r runner) lockOnly(s *staged, locks map[string][]dep.Update, changed bool) error {
	switch s.lockName {
	case BunLock, YarnLock:
		pinned, err := pinManifests(s.write, locks)
		if err != nil {
			return err
		}
		if err := toolrun.WriteFiles(r.tmp, pinned); err != nil {
			return err
		}
		if err := r.runAll(r.c); err != nil || s.lockName == BunLock {
			return err
		}
		restore := make(map[string][]byte, len(locks))
		for m := range locks {
			restore[m] = s.write[m]
		}
		if err := toolrun.WriteFiles(r.tmp, restore); err != nil {
			return err
		}
		p := filepath.Join(r.tmp, YarnLock)
		lock, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if lock, err = moveDescriptors(lock, locks, s.berry); err != nil {
			return err
		}
		if err := os.WriteFile(p, lock, 0o644); err != nil {
			return err
		}
		return r.runAll(r.c)
	}
	cmds := lockCommands(r.c, s.lockName, locks)
	if changed {
		cmds = slices.Insert(cmds, 0, r.c) // the plain install first
	}
	return r.runAll(cmds...)
}

// pickLock returns the lockfile of tool when present, else the first present lockfile.
func pickLock(files map[string][]byte, tool string) string {
	first := ""
	for _, n := range lockfiles {
		if files[n] == nil {
			continue
		}
		if toolOf[n] == tool {
			return n
		}
		if first == "" {
			first = n
		}
	}
	return first
}

// yarnFiles returns the local files .yarnrc.yml references (yarnPath, plugins[].path), which
// yarn berry loads before it touches the lockfile, as written (tree.local checks them).
func yarnFiles(rc []byte) ([]string, error) {
	var cfg struct {
		YarnPath string      `yaml:"yarnPath"`
		Plugins  []yaml.Node `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(rc, &cfg); err != nil {
		return nil, err
	}
	paths := []string{cfg.YarnPath}
	for _, n := range cfg.Plugins {
		switch n.Kind {
		case yaml.ScalarNode:
			paths = append(paths, n.Value)
		case yaml.MappingNode:
			var p struct {
				Path string `yaml:"path"`
			}
			if err := n.Decode(&p); err != nil {
				return nil, err
			}
			paths = append(paths, p.Path)
		}
	}
	return slices.DeleteFunc(paths, func(p string) bool { return p == "" }), nil
}

// cmdSpec is how one lockfile type is updated.
type cmdSpec struct {
	tool string
	// npxPackage is the npm package providing tool for npx; "" = tool.
	npxPackage string
	args, env  []string
}

func command(lockName string, berry bool) cmdSpec {
	switch {
	case lockName == PnpmLock:
		// CI=true makes pnpm default to --frozen-lockfile, which rejects the patched manifest.
		return cmdSpec{tool: "pnpm", args: []string{"install", "--lockfile-only", "--ignore-scripts", "--no-frozen-lockfile"}}
	case lockName == YarnLock && berry:
		return cmdSpec{tool: "yarn", npxPackage: "@yarnpkg/cli-dist", args: []string{"install", "--mode=update-lockfile"},
			env: []string{"YARN_ENABLE_SCRIPTS=0", "YARN_ENABLE_IMMUTABLE_INSTALLS=0"}}
	case lockName == YarnLock:
		return cmdSpec{tool: "yarn", args: []string{"install", "--ignore-scripts", "--ignore-engines", "--non-interactive", "--no-progress"}}
	case lockName == BunLock:
		return cmdSpec{tool: "bun", args: []string{"install", "--lockfile-only", "--ignore-scripts"}}
	}
	return cmdSpec{tool: "npm", args: []string{"install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund"}}
}

// argv resolves the command: the tool from PATH, else npx running the tool (at version when
// packageManager pins it), else exec.ErrNotFound.
func (c cmdSpec) argv(version string) ([]string, error) {
	if p, err := exec.LookPath(c.tool); err == nil {
		return append([]string{p}, c.args...), nil
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		return nil, exec.ErrNotFound
	}
	pkg := c.tool
	if c.npxPackage != "" {
		pkg = c.npxPackage
	}
	if version != "" {
		pkg += "@" + version
	}
	argv := []string{npx, "--yes"}
	if c.npxPackage != "" {
		argv = append(argv, "--package="+pkg, "--", c.tool)
	} else {
		argv = append(argv, pkg)
	}
	return append(argv, c.args...), nil
}

// run executes the command in dir; a failure carries the tail of its output (package managers
// report errors on stdout as well as stderr).
func (c cmdSpec) run(ctx context.Context, dir, version string) error {
	argv, err := c.argv(version)
	if err != nil {
		return err
	}
	cmd, cancel := toolrun.Command(ctx, argv[0], argv[1:]...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env("npm_config_update_notifier=false", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0")
	cmd.Env = append(cmd.Env, c.env...)
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "package manager command", "argv", argv, "dir", dir, "duration", time.Since(start), "err", err)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, exec.ErrNotFound):
		return err
	}
	if t := toolrun.Tail(out.Bytes(), 20); t != "" {
		return errors.New(t)
	}
	return err
}
