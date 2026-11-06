package beam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// mixLockFile is the default lockfile of a mix project.
const mixLockFile = "mix.lock"

// lockfileRe matches the `lockfile:` option of a project (umbrella children point at the
// umbrella's: `lockfile: "../../mix.lock"`).
var lockfileRe = regexp.MustCompile(`(?m)^[ \t]*lockfile:[ \t]*"([^"\\#]+)"`)

// mixLockPath returns the slash path (relative to root) of the lockfile of the mix.exs manifest:
// its `lockfile:` option, else the mix.lock next to it; "" when that leaves root.
func mixLockPath(root, manifest string) string {
	l := path.Join(path.Dir(manifest), mixLockFile)
	if src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(manifest))); err == nil {
		if m := lockfileRe.FindSubmatch(src); m != nil {
			l = path.Join(path.Dir(manifest), string(m[1]))
		}
	}
	if l == ".." || strings.HasPrefix(l, "../") || path.IsAbs(l) {
		return ""
	}
	return l
}

// lockedHex is a Hex package entry of mix.lock.
type lockedHex struct{ app, version string }

// mixLocked returns the Hex packages a mix.lock locks, by package name (`name:organization`
// for a package of an organization's repository, as dep.Dep.Name): `"app": {:hex, :name,
// "version", <hash>, <managers>, <deps>, "repo", <hash>}`. Git and path entries are left out.
func mixLocked(src []byte) (map[string]lockedHex, error) {
	start := bytes.Index(src, []byte("%{"))
	if start < 0 {
		return nil, errors.New("not a mix.lock (no %{ map)")
	}
	end := skipCode(src, start+2, '}')
	if end > len(src) || src[end-1] != '}' {
		return nil, errors.New("unterminated map")
	}
	out := map[string]lockedHex{}
	for _, el := range elements(src, span{start + 2, end - 1}) {
		el = trim(src, el)
		key := skipQuoted(src, el.start, '"', true)
		if src[el.start] != '"' || key >= el.end || src[key] != ':' {
			return nil, fmt.Errorf("unexpected entry %.40q", src[el.start:el.end])
		}
		app := string(src[el.start+1 : key-1])
		v := trim(src, span{key + 1, el.end})
		if v.end-v.start < 2 || src[v.start] != '{' {
			return nil, fmt.Errorf("entry %s is not a tuple", app)
		}
		f := elements(src, span{v.start + 1, v.end - 1})
		if len(f) < 3 || string(src[trim(src, f[0]).start:trim(src, f[0]).end]) != ":hex" {
			continue
		}
		name := strings.TrimPrefix(string(bytes.TrimSpace(src[f[1].start:f[1].end])), ":")
		ver, ok := literal(src, f[2])
		if !ok {
			return nil, fmt.Errorf("entry %s has no version", app)
		}
		if len(f) > 6 {
			if r, ok := literal(src, f[6]); ok && r.s != "hexpm" {
				name += ":" + strings.TrimPrefix(r.s, "hexpm:")
			}
		}
		out[name] = lockedHex{app: app, version: ver.s}
	}
	return out, nil
}

// ReadLocks records the version the mix.lock of each mix.exs (mixLockPath) locks its Hex
// packages to (dep.Dep.Locked). A requirement without a lower bound (`>= 0.0.0`, `< 2.0.0`)
// takes the locked version as its Version, or is skipped without one. An unreadable lockfile is
// reported once and its deps are bumped (LockUnread).
func (mixManager) ReadLocks(root string, files [][]dep.Dep) []error {
	return readLocks(root, files, mixLockPath, func(b []byte) (map[string]string, error) {
		locked, err := mixLocked(b)
		if err != nil {
			return nil, err
		}
		out := make(map[string]string, len(locked))
		for name, l := range locked {
			out[name] = l.version
		}
		return out, nil
	})
}

// readLocks implements ReadLocks for both managers: lockOf returns the lockfile of a manifest,
// versions the locked version of each package.
func readLocks(root string, files [][]dep.Dep, lockOf func(root, manifest string) string, versions func([]byte) (map[string]string, error)) []error {
	type lockState struct {
		versions map[string]string
		err      error
	}
	locks := map[string]*lockState{}
	lockFor := map[string]string{} // manifest → lockfile
	var errs []error
	for _, f := range files {
		for j := range f {
			d := &f[j]
			if d.Skip != "" || d.Range == "" {
				continue
			}
			file, ok := lockFor[d.File]
			if !ok {
				file = lockOf(root, d.File)
				lockFor[d.File] = file
			}
			st := locks[file]
			if st == nil {
				st = &lockState{}
				if file != "" {
					b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
					if err == nil {
						st.versions, err = versions(b)
					}
					if err != nil && !errors.Is(err, os.ErrNotExist) {
						st.err = fmt.Errorf("%s: %w", file, err)
						errs = append(errs, st.err)
					}
				}
				locks[file] = st
			}
			if st.err != nil {
				d.LockUnread = true
			} else if v := st.versions[d.Name]; hexScheme.Valid(v) {
				d.Locked = v
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

// LockRoots implements manager.LockMaintainer: the lockfile of each mix.exs with one, once.
func (mixManager) LockRoots(root string, files []string) []manager.LockRoot {
	return lockRoots(root, files, MixLocker{}.Scope, mixLockPath)
}

// lockRoots implements LockRoots for both managers: one root per lockfile, the manifest of the
// lockfile's directory (an umbrella's) or else the first manifest using it.
func lockRoots(root string, files []string, scope func(context.Context, string, string) (string, bool), lockOf func(root, manifest string) string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		if _, ok := scope(context.Background(), root, f); !ok {
			continue
		}
		l := lockOf(root, f)
		i := slices.IndexFunc(out, func(r manager.LockRoot) bool { return r.Lockfile == l })
		switch {
		case i < 0:
			out = append(out, manager.LockRoot{Manifest: f, Lockfile: l})
		case path.Dir(f) == path.Dir(l):
			out[i].Manifest = f
		}
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

// MixLocker is the mix post-processor (engine.PostProcessor, UpdatePinner and LockMaintainer):
// it updates the mix.lock of a patched mix.exs with `mix deps.update` in a temporary mirror of
// the lockfile's directory. The working tree is never touched.
type MixLocker struct{}

// Siblings lists mix.lock.
func (MixLocker) Siblings() []string { return []string{mixLockFile} }

// Scope returns the directory of the lockfile of a mix.exs (mixLockPath) when it exists: the
// umbrella root for the apps of an umbrella project.
func (MixLocker) Scope(_ context.Context, root, manifest string) (string, bool) {
	l := mixLockPath(root, manifest)
	if l == "" || path.Base(l) != mixLockFile {
		return "", false
	}
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(l)))
	return path.Dir(l), err == nil && st.Mode().IsRegular()
}

// Process brings the lockfile in line with the patched manifests: `mix deps.get`.
func (l MixLocker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates moves every updated dependency, manifest updates (ups) and lockfile-only ones
// (locks) alike: `mix deps.update <app>…` in a mirror whose mix.exs files require exactly the
// update's version of each Hex package (`== <version>`), so Mix locks that version rather than
// a newer one the requirement admits (one minimumReleaseAge held back); Mix may move the
// updated packages' own dependencies too. Git dependencies move to their new tag.
// Without updates `mix deps.get` locks what the patched requirements need. It returns the
// lockfile when it changed.
func (MixLocker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	orig, ok := files[mixLockFile]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", mixLockFile, dir)
		}
		return nil, nil
	}
	pinned := map[string][]byte{}
	var apps []string
	for _, byManifest := range []map[string][]dep.Update{ups, locks} {
		for rel, us := range byManifest {
			src, ok := pinned[rel]
			if !ok {
				if src, ok = manifests[rel]; !ok {
					var err error
					if src, err = os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
						return nil, err
					}
				}
			}
			out, names, err := pinMix(src, us)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", rel, err)
			}
			pinned[rel] = out
			for _, n := range names {
				if !slices.Contains(apps, n) {
					apps = append(apps, n)
				}
			}
		}
	}
	for rel, src := range manifests {
		if _, ok := pinned[rel]; !ok {
			pinned[rel] = src
		}
	}
	args := []string{"deps.get"}
	if len(apps) > 0 {
		slices.Sort(apps)
		args = append([]string{"deps.update"}, apps...)
	}
	return runMix(ctx, dir, pinned, orig, args)
}

// pinMix returns src with the requirement of each Hex update's dependency replaced by `==
// <version>`, and the apps of the updates. A dependency is found by its update's line (edits
// never add lines to a mix.exs) and name.
func pinMix(src []byte, us []dep.Update) ([]byte, []string, error) {
	es := mixEntries(src)
	type at struct {
		e    entry
		line int
	}
	all := make([]at, 0, len(es))
	line, pos := 1, 0
	for _, e := range es {
		line += bytes.Count(src[pos:e.at], []byte{'\n'})
		pos = e.at
		all = append(all, at{e, line})
	}
	var reps []dep.Span
	var vals []string
	var apps []string
	for _, u := range us {
		i := slices.IndexFunc(all, func(a at) bool { return a.line == u.Dep.Line && mixDep("", a.line, a.e).Name == u.Dep.Name })
		if i < 0 {
			return nil, nil, fmt.Errorf("dependency %s not found on line %d", u.Dep.Name, u.Dep.Line)
		}
		e := all[i].e
		apps = append(apps, e.app)
		if u.Dep.Datasource == Datasource && e.req != nil && hexScheme.Valid(u.NewVersion) {
			reps = append(reps, dep.Span{Start: e.req.at.start, End: e.req.at.end})
			vals = append(vals, "== "+u.NewVersion)
		}
	}
	return replace(src, reps, vals), apps, nil
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `mix deps.update --all` in
// a mirror of dir moves every locked dependency to the newest version the requirements admit;
// the manifests are unchanged. It returns the lockfile when it changed; without one it does
// nothing.
func (MixLocker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[mixLockFile]
	if !ok {
		return nil, nil
	}
	return runMix(ctx, dir, nil, lock, []string{"deps.update", "--all"})
}

// mixSkipDirs are left out of a mix mirror: Mix fetches and builds into fresh ones.
var mixSkipDirs = []string{".git", "deps", "_build", "node_modules", ".elixir_ls"}

// runMix runs `mix <args>` in a mirror of dir (mirror) whose manifests (slash paths relative
// to dir) and mix.lock are copies, and returns mix.lock when it changed.
func runMix(ctx context.Context, dir string, manifests map[string][]byte, lock []byte, args []string) (map[string][]byte, error) {
	files := map[string][]byte{mixLockFile: lock}
	maps.Copy(files, manifests)
	tmp, base, err := mirror(dir, "deps-mix-*", files, mixSkipDirs)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := tool(ctx, "mix", mixLockFile, base, dir, args, "MIX_ENV=dev"); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(base, mixLockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{mixLockFile: out}, nil
}
