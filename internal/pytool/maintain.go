package pytool

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// Maintain implements engine.LockMaintainer (lockFileMaintenance): in the temporary copy of
// Process, the lockfile's tool moves every locked package to the newest version the manifests
// admit, with the environment and uvx fallback of Process; the manifests are unchanged:
//   - uv.lock: `uv lock --no-progress --upgrade`
//   - poetry.lock: `poetry update --lock` (Poetry 1 and 2)
//   - pdm.lock: `pdm update --no-sync --update-all` (the locked groups are kept)
//   - Pipfile.lock: `pipenv lock` (pipenv resolves every package anew)
//
// The lockfile is maintainedLock's. It returns the lockfile when it changed; without one it
// does nothing.
func (Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	name := maintainedLock(dir)
	if files[name] == nil {
		return nil, nil
	}
	var c cmdSpec
	switch name {
	case UVLock:
		c = uvCmd()
		c.args = append(c.args, "--upgrade")
	case PoetryLock:
		c = poetryTool()
		c.args = []string{"update", "--lock"}
	case PDMLock:
		c = pdmCmd()
		c.args = []string{"update", "--no-sync", "--update-all"}
	default:
		c = pipenvCmd()
	}
	return lockIn(ctx, dir, name, nil, files, []lockStep{{cmd: c}})
}

// maintainedLock is the lockfile Maintain refreshes in the absolute lock directory dir: next
// to a pyproject.toml uv.lock, poetry.lock or pdm.lock (the first present, as Process),
// else Pipfile.lock; "" without one.
func maintainedLock(dir string) string {
	names := []string{PipfileLock}
	if isFile(filepath.Join(dir, Pyproject)) {
		names = []string{UVLock, PoetryLock, PDMLock, PipfileLock}
	}
	for _, n := range names {
		if isFile(filepath.Join(dir, n)) {
			return n
		}
	}
	return ""
}

// LockRoots returns the lock roots (manager.LockMaintainer) of files whose maintainedLock is
// one of names, so managers sharing a lock directory claim disjoint lockfiles: each lock
// directory of Scope once, with its pyproject.toml (a uv workspace's root one) or Pipfile as
// the manifest, sorted by lockfile. A Pipfile.lock next to a pyproject.toml with its own
// lockfile is therefore not maintained.
func LockRoots(root string, files []string, names ...string) []manager.LockRoot {
	var out []manager.LockRoot
	ws := workspaces{}
	for _, f := range files {
		dir, ok := scope(context.Background(), root, f, ws)
		if !ok {
			continue
		}
		name := maintainedLock(filepath.Join(root, filepath.FromSlash(dir)))
		if !slices.Contains(names, name) {
			continue
		}
		manifest := Pyproject
		if name == PipfileLock {
			manifest = Pipfile
		}
		lr := manager.LockRoot{Manifest: path.Join(dir, manifest), Lockfile: path.Join(dir, name)}
		if !slices.Contains(out, lr) {
			out = append(out, lr)
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}
