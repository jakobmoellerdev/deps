package beam

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Gleam dep types: the gleam.toml table naming the package (`[dev_dependencies]`, Gleam's
// canonical name, is read as dev-dependencies too).
const (
	DepTypeDependencies    = "dependencies"
	DepTypeDevDependencies = "dev-dependencies"
)

const (
	gleamManifest = "gleam.toml"
	gleamLockFile = "manifest.toml"
)

// gleamTables are the dependency tables of gleam.toml and their dep types.
var gleamTables = []struct{ table, depType string }{
	{"dependencies", DepTypeDependencies},
	{"dev-dependencies", DepTypeDevDependencies},
	{"dev_dependencies", DepTypeDevDependencies},
}

type gleamManager struct{}

// NewGleam returns the gleam manager.
func NewGleam() manager.Manager { return gleamManager{} }

func (gleamManager) Name() string { return NameGleam }

// Match reports whether the slash path is a gleam.toml.
func (gleamManager) Match(p string) bool {
	return p == gleamManifest || strings.HasSuffix(p, "/"+gleamManifest)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (gleamManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{gleamManifest}}
}

// Extract returns one Dep per entry of `[dependencies]` and `[dev-dependencies]`
// (`[dev_dependencies]`): `name = "<requirement>"` or `name = { version = "<requirement>" }`,
// with Range the requirement (RefSpan its bytes) and Version its anchor (as for mix). Path and
// git dependencies (`{ path = … }`, `{ git = …, ref = … }`) are skipped.
func (gleamManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	root, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	var deps []dep.Dep
	for _, t := range gleamTables {
		tbl := root.Get(t.table)
		if tbl == nil || tbl.Kind != tomlspan.Table {
			continue
		}
		for _, name := range tbl.Keys {
			deps = append(deps, gleamDep(file, t.depType, name, tbl.Fields[name]))
		}
	}
	return deps, nil
}

// gleamRequirement returns the requirement string of a dependency value v, or nil.
func gleamRequirement(v *tomlspan.Value) *tomlspan.Value {
	if v.Kind == tomlspan.Table {
		v = v.Fields["version"]
	}
	if v == nil || v.Kind != tomlspan.String {
		return nil
	}
	return v
}

// gleamDep is the dep of the dependency name = v of a table of depType.
func gleamDep(file, depType, name string, v *tomlspan.Value) dep.Dep {
	d := dep.Dep{Manager: NameGleam, Datasource: Datasource, Versioning: Versioning, DepType: depType, File: file, Line: v.Line, Name: name}
	req := gleamRequirement(v)
	switch {
	case v.Kind == tomlspan.Table && v.Fields["path"] != nil:
		d.Skip = skipPath
	case v.Kind == tomlspan.Table && v.Fields["git"] != nil:
		d.Skip = skipGit
	case req == nil:
		d.Skip = skipNoReq
	case !req.Exact:
		d.Version, d.Skip = req.Str, skipEscaped
	default:
		d.Line = req.Line
		a, ok := anchor(req.Str)
		if !ok {
			d.Version, d.Skip = req.Str, skipUnsupported
			break
		}
		d.Version, d.Range, d.RefSpan = a, req.Str, dep.Span{Start: req.Start, End: req.End}
	}
	return d
}

// Edits rewrites the requirement for the new version under u.RangeStrategy (newRange).
func (gleamManager) Edits(u dep.Update) ([]patch.Edit, error) { return edits(u) }

// NewRange implements manager.RangeRewriter (newRange).
func (gleamManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return newRange(d.Range, v, strategy)
}

// gleamLockPath returns the slash path of the manifest.toml next to a gleam.toml.
func gleamLockPath(_, manifest string) string { return path.Join(path.Dir(manifest), gleamLockFile) }

// gleamLocked returns the versions a manifest.toml locks its Hex packages to, by name
// (`packages = [{ name = "…", version = "…", source = "hex", … }]`).
func gleamLocked(src []byte) (map[string]string, error) {
	root, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	pkgs := root.Get("packages")
	if pkgs == nil || pkgs.Kind != tomlspan.Array {
		return nil, errors.New("not a Gleam manifest.toml (no packages)")
	}
	out := map[string]string{}
	for _, p := range pkgs.Items {
		name, ver, src := p.Get("name"), p.Get("version"), p.Get("source")
		if name == nil || ver == nil || src == nil || src.Str != "hex" {
			continue
		}
		out[name.Str] = ver.Str
	}
	return out, nil
}

// ReadLocks records the version the manifest.toml next to each gleam.toml locks its packages to
// (dep.Dep.Locked), as the mix manager does.
func (gleamManager) ReadLocks(root string, files [][]dep.Dep) []error {
	return readLocks(root, files, gleamLockPath, gleamLocked)
}

// LockRoots implements manager.LockMaintainer: the manifest.toml of each gleam.toml with one.
func (gleamManager) LockRoots(root string, files []string) []manager.LockRoot {
	return lockRoots(root, files, GleamLocker{}.Scope, gleamLockPath)
}

// GleamLocker is the gleam post-processor (engine.PostProcessor, UpdatePinner and
// LockMaintainer): it updates the manifest.toml of a patched gleam.toml with `gleam deps
// update` in a temporary mirror of the project. The working tree is never touched.
type GleamLocker struct{}

// Siblings lists manifest.toml.
func (GleamLocker) Siblings() []string { return []string{gleamLockFile} }

// Scope returns the directory of a gleam.toml that has a manifest.toml next to it.
func (GleamLocker) Scope(_ context.Context, root, manifest string) (string, bool) {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(gleamLockPath(root, manifest))))
	return path.Dir(manifest), err == nil && st.Mode().IsRegular()
}

// Process brings manifest.toml in line with the patched gleam.toml: `gleam deps download`.
func (l GleamLocker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates moves every updated package, manifest updates (ups) and lockfile-only ones
// (locks) alike, to exactly the update's version: `gleam deps update <name>…` in a mirror whose
// gleam.toml requires `== <version>` of each, so Gleam locks that version rather than a newer
// one the requirement admits, and moves other packages only where the new versions need it.
// The `[requirements]` manifest.toml records are then set back to the patched gleam.toml's.
// Without updates `gleam deps download` locks what the patched requirements need. It returns
// manifest.toml when it changed.
func (GleamLocker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	orig, ok := files[gleamLockFile]
	if !ok {
		if len(locks) > 0 {
			return nil, fmt.Errorf("lockfile-only updates need %s in %s", gleamLockFile, dir)
		}
		return nil, nil
	}
	src, ok := manifests[gleamManifest]
	if !ok {
		var err error
		if src, err = os.ReadFile(filepath.Join(dir, gleamManifest)); err != nil {
			return nil, err
		}
	}
	reqs := map[string]string{} // the patched requirement of each pinned package
	var names []string
	var spans []dep.Span
	var vals []string
	root, err := tomlspan.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", gleamManifest, err)
	}
	for _, u := range slices.Concat(ups[gleamManifest], locks[gleamManifest]) {
		if slices.Contains(names, u.Dep.Name) {
			continue
		}
		var req *tomlspan.Value
		for _, t := range gleamTables {
			if v := root.Get(t.table, u.Dep.Name); v != nil {
				req = gleamRequirement(v)
			}
		}
		if req == nil || !req.Exact || !hexScheme.Valid(u.NewVersion) {
			return nil, fmt.Errorf("%s: no requirement of %s to pin", gleamManifest, u.Dep.Name)
		}
		names = append(names, u.Dep.Name)
		reqs[u.Dep.Name] = req.Str
		spans = append(spans, dep.Span{Start: req.Start, End: req.End})
		vals = append(vals, "== "+u.NewVersion)
	}
	args := []string{"deps", "download"}
	if len(names) > 0 {
		args = append([]string{"deps", "update"}, names...)
	}
	out, err := runGleam(ctx, dir, replace(src, spans, vals), orig, args)
	if err != nil || out == nil || len(reqs) == 0 {
		return out, err
	}
	fixed, err := restoreRequirements(out[gleamLockFile], reqs)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(fixed, orig) {
		return nil, nil
	}
	return map[string][]byte{gleamLockFile: fixed}, nil
}

// restoreRequirements sets the `[requirements]` records of the pinned packages of a
// manifest.toml (`name = { version = "== 1.2.3" }`) to their requirements reqs.
func restoreRequirements(lock []byte, reqs map[string]string) ([]byte, error) {
	root, err := tomlspan.Parse(lock)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", gleamLockFile, err)
	}
	var spans []dep.Span
	var vals []string
	for name, req := range reqs {
		v := root.Get("requirements", name, "version")
		if v == nil || v.Kind != tomlspan.String || !v.Exact {
			continue
		}
		spans = append(spans, dep.Span{Start: v.Start, End: v.End})
		vals = append(vals, req)
	}
	return replace(lock, spans, vals), nil
}

// replace returns src with each span replaced by its value; spans do not overlap.
func replace(src []byte, spans []dep.Span, vals []string) []byte {
	order := make([]int, len(spans))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return spans[b].Start - spans[a].Start })
	out := slices.Clone(src)
	for _, i := range order {
		out = slices.Concat(out[:spans[i].Start], []byte(vals[i]), out[spans[i].End:])
	}
	return out
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `gleam deps update` in a
// mirror of dir moves every locked package to the newest version the requirements admit;
// gleam.toml is unchanged. It returns manifest.toml when it changed; without one it does
// nothing.
func (GleamLocker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[gleamLockFile]
	if !ok {
		return nil, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, gleamManifest))
	if err != nil {
		return nil, err
	}
	return runGleam(ctx, dir, src, lock, []string{"deps", "update"})
}

// gleamSkipDirs are left out of a gleam mirror: Gleam downloads and builds into a fresh build
// directory.
var gleamSkipDirs = []string{".git", "build", "node_modules"}

// runGleam runs `gleam <args>` in a mirror of dir holding src as gleam.toml and lock as
// manifest.toml, and returns manifest.toml when it changed.
func runGleam(ctx context.Context, dir string, src, lock []byte, args []string) (map[string][]byte, error) {
	tmp, base, err := mirror(dir, "deps-gleam-*", map[string][]byte{gleamManifest: src, gleamLockFile: lock}, gleamSkipDirs)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := tool(ctx, "gleam", gleamLockFile, base, dir, args); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(base, gleamLockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{gleamLockFile: out}, nil
}
