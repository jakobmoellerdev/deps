// Package gotool runs the go command where deps cannot avoid it: go env, go list, go get and
// go mod tidy.
package gotool

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// DefaultProxy is the GOPROXY used when the go command is unavailable and GOPROXY is unset.
const DefaultProxy = "https://proxy.golang.org,direct"

// GoEnv is the module proxy configuration of the go command.
type GoEnv struct {
	Proxy    string // GOPROXY
	NoProxy  string // GONOPROXY, or GOPRIVATE if GONOPROXY is empty
	ModCache string // GOMODCACHE; "" when unknown
}

// Env reads GOPROXY, GONOPROXY, GOPRIVATE and GOMODCACHE via `go env -json`.
// If go is not in PATH it falls back to the process environment (GOMODCACHE, else
// $GOPATH/pkg/mod, else ~/go/pkg/mod, as the go command defaults).
func Env(ctx context.Context) (GoEnv, error) {
	var v struct{ GOPROXY, GONOPROXY, GOPRIVATE, GOMODCACHE string }
	out, err := run(ctx, "", nil, "env", "-json", "GOPROXY", "GONOPROXY", "GOPRIVATE", "GOMODCACHE")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		v.GOPROXY, v.GONOPROXY, v.GOPRIVATE = os.Getenv("GOPROXY"), os.Getenv("GONOPROXY"), os.Getenv("GOPRIVATE")
		v.GOMODCACHE = defaultModCache()
	case err != nil:
		return GoEnv{}, err
	default:
		if err := json.Unmarshal(out, &v); err != nil {
			return GoEnv{}, fmt.Errorf("go env: %w", err)
		}
	}
	e := GoEnv{Proxy: v.GOPROXY, NoProxy: v.GONOPROXY, ModCache: v.GOMODCACHE}
	if e.Proxy == "" {
		e.Proxy = DefaultProxy
	}
	if e.NoProxy == "" {
		e.NoProxy = v.GOPRIVATE
	}
	return e, nil
}

// defaultModCache mirrors the go command's GOMODCACHE default without running it.
func defaultModCache() string {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return v
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		gopath = filepath.Join(home, "go")
	}
	// GOPATH may be a list; the module cache lives in the first entry.
	if first, _, _ := strings.Cut(gopath, string(os.PathListSeparator)); first != "" {
		return filepath.Join(first, "pkg", "mod")
	}
	return ""
}

// ListVersions returns the known versions of module mod via `go list -m -versions -json`, run in dir.
func ListVersions(ctx context.Context, dir, mod string) ([]string, error) {
	out, err := run(ctx, dir, []string{"GOFLAGS=-mod=mod", "GOWORK=off"}, "list", "-m", "-versions", "-json", mod)
	if err != nil {
		return nil, err
	}
	var m struct{ Versions []string }
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("go list -m -versions %s: %w", mod, err)
	}
	return m.Versions, nil
}

// ModuleTime returns the publication time of module mod at version via
// `go list -m -json mod@version`, run in dir; zero when the origin reports none.
func ModuleTime(ctx context.Context, dir, mod, version string) (time.Time, error) {
	out, err := run(ctx, dir, []string{"GOFLAGS=-mod=mod", "GOWORK=off"}, "list", "-m", "-json", mod+"@"+version)
	if err != nil {
		return time.Time{}, err
	}
	var m struct{ Time *time.Time }
	if err := json.Unmarshal(out, &m); err != nil {
		return time.Time{}, fmt.Errorf("go list -m %s@%s: %w", mod, version, err)
	}
	if m.Time == nil {
		return time.Time{}, nil
	}
	return *m.Time, nil
}

// Module is the `go list -m -json` (and proxy `@latest`) view of a module version.
type Module struct {
	Version string
	Time    *time.Time
	Origin  *struct{ Hash string }
}

// Hash returns the commit hash of the module's origin, or "".
func (m Module) Hash() string {
	if m.Origin == nil {
		return ""
	}
	return m.Origin.Hash
}

// ModuleLatest resolves module mod@latest via `go list -m -json mod@latest`, run in dir.
func ModuleLatest(ctx context.Context, dir, mod string) (Module, error) {
	out, err := run(ctx, dir, []string{"GOFLAGS=-mod=mod", "GOWORK=off"}, "list", "-m", "-json", mod+"@latest")
	if err != nil {
		return Module{}, err
	}
	var m Module
	if err := json.Unmarshal(out, &m); err != nil {
		return Module{}, fmt.Errorf("go list -m %s@latest: %w", mod, err)
	}
	return m, nil
}

// Tidy runs `go mod tidy` in dir against copies of mod and sum (sum may be nil) and returns
// the tidied contents. overlay holds patched source files by slash path relative to dir
// (import paths rewritten by a major module path update); tidy reads them through
// `-overlay`. The real files in dir are never touched. A failure because a required module
// version declares another module path is a *dep.SkipError (see pathChanged).
func Tidy(ctx context.Context, dir string, mod, sum []byte, overlay map[string][]byte) (newMod, newSum []byte, err error) {
	tmp, err := os.MkdirTemp("", "deps-tidy-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	newMod, newSum, _, err = tidyIn(ctx, tmp, dir, mod, sum, overlay)
	return newMod, newSum, err
}

// tidyIn is Tidy in the temporary directory tmp, which keeps the tidied go.mod and go.sum and
// the overlay; overlayArg is the `-overlay` flag tidy used ("" without overlay).
func tidyIn(ctx context.Context, tmp, dir string, mod, sum []byte, overlay map[string][]byte) (newMod, newSum []byte, overlayArg string, err error) {
	modPath, sumPath := filepath.Join(tmp, "go.mod"), filepath.Join(tmp, "go.sum")
	if err := os.WriteFile(modPath, mod, 0o644); err != nil {
		return nil, nil, "", err
	}
	if sum != nil {
		if err := os.WriteFile(sumPath, sum, 0o644); err != nil {
			return nil, nil, "", err
		}
	}
	args := []string{"mod", "tidy", "-modfile=" + modPath}
	if len(overlay) > 0 {
		if overlayArg, err = writeOverlay(tmp, dir, overlay); err != nil {
			return nil, nil, "", err
		}
		args = append(args, overlayArg)
	}
	if _, err := run(ctx, dir, []string{"GOWORK=off", "GOFLAGS="}, args...); err != nil {
		if skip := pathChanged(err); skip != nil {
			slog.InfoContext(ctx, "go mod tidy: a required module declares another path", "dir", dir, "reason", skip.Reason)
			return nil, nil, "", skip
		}
		return nil, nil, "", inDir("go mod tidy", dir, err)
	}
	if newMod, err = os.ReadFile(modPath); err != nil {
		return nil, nil, "", err
	}
	if newSum, err = os.ReadFile(sumPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, "", err
	}
	return newMod, newSum, overlayArg, nil
}

// vendorModule runs `go mod vendor` in the module directory dir against the go.mod copy in
// tmp (tidyIn's) and adds the files that differ from dir's vendor directory to out, under
// prefix + "vendor/". overlayArg is the `-overlay` flag of tidy, or "".
func vendorModule(ctx context.Context, tmp, dir, overlayArg, prefix string, out map[string][]byte) error {
	args := []string{"mod", "vendor", "-modfile=" + filepath.Join(tmp, "go.mod"), "-o", filepath.Join(tmp, "vendor")}
	if overlayArg != "" {
		args = append(args, overlayArg)
	}
	if _, err := run(ctx, dir, []string{"GOWORK=off", "GOFLAGS="}, args...); err != nil {
		return inDir("go mod vendor", dir, err)
	}
	return diffTree(filepath.Join(dir, "vendor"), filepath.Join(tmp, "vendor"), prefix+"vendor", out)
}

// diffTree adds to out, under prefix plus their slash path below the directories, the
// regular files of next whose content differs from, or is missing in, cur, and nil for the
// files of cur missing in next. Contents are read one file at a time and kept only when
// they changed; a size difference skips reading the old file.
func diffTree(cur, next, prefix string, out map[string][]byte) error {
	seen := map[string]bool{}
	err := filepath.WalkDir(next, func(p string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(next, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		seen[rel] = true
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		old := filepath.Join(cur, filepath.FromSlash(rel))
		if st, err := os.Stat(old); err == nil && st.Mode().IsRegular() && st.Size() == int64(len(b)) {
			ob, err := os.ReadFile(old)
			if err != nil {
				return err
			}
			if bytes.Equal(ob, b) {
				return nil
			}
		}
		out[prefix+"/"+rel] = b
		return nil
	})
	if err != nil {
		return err
	}
	err = filepath.WalkDir(cur, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, err := filepath.Rel(cur, p)
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); !seen[rel] {
			out[prefix+"/"+rel] = nil
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// pathMismatch is the go command's refusal of a module version whose go.mod declares another
// module path: `<path>@<version>: parsing go.mod:` followed by `module declares its path as:
// <declared>` and `but was required as: <path>`.
var pathMismatch = regexp.MustCompile(`(\S+)@([^\s:]+): parsing go\.mod:\s+module declares its path as: (\S+)\s+but was required as: (\S+)`)

// pathChanged returns the skip of a go command failure caused by a required module version
// that declares another module path (the module was renamed upstream, or a fork kept its
// origin's path): no tidy of this go.mod can succeed until it requires the declared path, so
// it is the update's skip, not a failure. nil for any other failure.
func pathChanged(err error) *dep.SkipError {
	var ce *cmdError
	if !errors.As(err, &ce) {
		return nil
	}
	m := pathMismatch.FindStringSubmatch(ce.stderr)
	if len(m) != 5 || m[1] != m[4] {
		return nil
	}
	return &dep.SkipError{Reason: fmt.Sprintf("module path changed to %s (%s@%s)", m[3], m[1], m[2])}
}

// writeOverlay writes the overlay files and their `-overlay` JSON (absolute paths of dir's
// files to the copies) into tmp and returns the flag.
func writeOverlay(tmp, dir string, files map[string][]byte) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	replace := make(map[string]string, len(files))
	n := 0
	for rel, b := range files {
		n++
		p := filepath.Join(tmp, fmt.Sprintf("overlay-%d.go", n))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return "", err
		}
		replace[filepath.Join(abs, filepath.FromSlash(rel))] = p
	}
	b, err := json.Marshal(struct{ Replace map[string]string }{replace})
	if err != nil {
		return "", err
	}
	p := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return "-overlay=" + p, nil
}

// Tidier is the gomod post-processor (engine.PostProcessor): Tidy on copies of go.mod and
// go.sum, with patched .go files (import path rewrites) as an overlay, then `go mod vendor`
// or `go work vendor` where modules are vendored. Imports, when set, memoizes the import
// digests ScopeInputs computes for one run (nil computes them anew).
type Tidier struct{ Imports *ImportCache }

// Scope is the module directory: every go.mod is post-processed on its own, together with
// the patched .go files of its module (the nearest go.mod at or above their directory). The
// modules of a workspace that vendors (gomod.Workspace, with vendor/modules.txt next to the
// go.work) share the workspace directory as their scope, since its vendor directory derives
// from all of them. A go.work has no scope: its go and toolchain directives derive nothing.
func (Tidier) Scope(_ context.Context, root, manifest string) (string, bool) {
	dir, name, ok := strings.CutLast(manifest, "/")
	if !ok {
		dir, name = ".", manifest
	}
	switch name {
	case "go.work":
		return "", false
	case "go.mod":
		return moduleScope(root, dir), true
	}
	for {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), "go.mod")); err == nil {
			return moduleScope(root, dir), true
		}
		if dir == "." {
			return "", false
		}
		if dir, _, ok = strings.CutLast(dir, "/"); !ok {
			dir = "."
		}
	}
}

// moduleScope is the scope of the module directory dir: its vendoring workspace, else dir.
func moduleScope(root, dir string) string {
	if ws, ok := gomod.Workspace(root, dir); ok && vendoredWorkspace(filepath.Join(root, filepath.FromSlash(ws))) {
		return ws
	}
	return dir
}

// vendoredWorkspace reports whether the absolute directory dir holds a go.work and the
// workspace's vendor directory.
func vendoredWorkspace(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.work"))
	return err == nil && gomod.Vendors(dir)
}

// Siblings lists go.sum.
func (Tidier) Siblings() []string { return []string{"go.sum"} }

// ScopeSiblings implements engine.ScopeSiblingLister: go.sum, or for a vendoring workspace
// the go.sum of each of its modules.
func (Tidier) ScopeSiblings(dir string) []string {
	if !vendoredWorkspace(dir) {
		return []string{"go.sum"}
	}
	mods, _ := gomod.WorkspaceModules(dir)
	sums := make([]string, len(mods))
	for i, m := range mods {
		sums[i] = path.Join(m, "go.sum")
	}
	return sums
}

// Trees implements engine.TreeRegenerator: the vendor directories below dir the go command
// uses (vendor/modules.txt), of the scope's module, or of a vendoring workspace and its
// modules.
func (Tidier) Trees(dir string) []string {
	mods := []string{"."}
	if vendoredWorkspace(dir) {
		ws, _ := gomod.WorkspaceModules(dir)
		mods = append(mods, ws...)
	}
	var trees []string
	for _, m := range mods {
		if t := path.Join(m, "vendor"); !slices.Contains(trees, t) && gomod.Vendors(filepath.Join(dir, filepath.FromSlash(m))) {
			trees = append(trees, t)
		}
	}
	return trees
}

// ScopeInputs implements engine.ScopeInputLister. Tidy runs with GOWORK=off and reads, besides
// the patched manifests and go.sum: go.mod and the non-standard imports of the module's .go
// files (scanModule, importDigest), and for every local module replacement (`replace m =>
// ../m`) its go.mod and the imports of its .go files. go.mod is listed although it is the
// manifest: every scope patches it. A replacement that leaves root, or is absolute, is an
// error, since it cannot be named relative to root, and so is a missing one; one without
// go.mod is listed, so the group is always built until the base commit has it. A vendor
// directory counts whole, since only its changed files are regenerated. The scope of a
// vendoring workspace reads go.work, go.work.sum and the inputs of each of its modules.
func (t Tidier) ScopeInputs(ctx context.Context, root, dir string, _ []string) (paths, digests []string, err error) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	if !vendoredWorkspace(abs) {
		return t.moduleInputs(root, dir)
	}
	mods, err := gomod.WorkspaceModules(abs)
	if err != nil {
		return nil, nil, err
	}
	paths = []string{path.Join(dir, "go.work"), path.Join(dir, "vendor")}
	if _, err := os.Stat(filepath.Join(abs, "go.work.sum")); err == nil {
		paths = append(paths, path.Join(dir, "go.work.sum"))
	}
	for _, m := range mods {
		p, d, err := t.moduleInputs(root, path.Join(dir, m))
		if err != nil {
			return nil, nil, err
		}
		paths, digests = append(paths, p...), append(digests, d...)
	}
	return paths, digests, nil
}

// moduleInputs is ScopeInputs of the module directory dir.
func (t Tidier) moduleInputs(root, dir string) (paths, digests []string, err error) {
	file := filepath.Join(root, filepath.FromSlash(dir), "go.mod")
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	f, err := modfile.Parse(file, b, nil) // ParseLax drops replace directives
	if err != nil {
		return nil, nil, err
	}
	local := map[string]bool{}
	addLocal := func(modPath string) {
		elem, _, _ := strings.Cut(modPath, "/")
		local[elem] = true
	}
	if f.Module != nil {
		addLocal(f.Module.Mod.Path)
	}
	for _, r := range f.Require {
		addLocal(r.Mod.Path)
	}
	mods := []string{dir}
	for _, r := range f.Replace {
		addLocal(r.Old.Path)
		if r.New.Version != "" || !modfile.IsDirectoryPath(r.New.Path) {
			addLocal(r.New.Path)
			continue
		}
		target := filepath.ToSlash(filepath.Join(filepath.FromSlash(dir), filepath.FromSlash(r.New.Path)))
		if filepath.IsAbs(r.New.Path) || !filepath.IsLocal(filepath.FromSlash(target)) {
			return nil, nil, fmt.Errorf("replace %s => %s: outside the root", r.Old.Path, r.New.Path)
		}
		mods = append(mods, target)
	}
	for _, m := range mods {
		paths = append(paths, filepath.ToSlash(filepath.Join(filepath.FromSlash(m), "go.mod")))
		lines, err := t.Imports.scan(filepath.Join(root, filepath.FromSlash(m)))
		if err != nil {
			return nil, nil, err
		}
		digests = append(digests, "go imports "+m+" "+importDigest(lines, local))
	}
	if gomod.Vendors(filepath.Join(root, filepath.FromSlash(dir))) {
		paths = append(paths, path.Join(dir, "vendor"))
	}
	return paths, digests, nil
}

// Process tidies the patched go.mod and returns the tidied go.mod and go.sum (nil when tidy
// wrote none). Other manifests are patched .go files of the module, passed as the overlay.
// A module with vendor/modules.txt is vendored again (`go mod vendor -modfile=<tmp>/go.mod
// -o <tmp>/vendor`); the result holds the vendor files that changed. In the scope of a
// vendoring workspace, every patched go.mod is tidied in its module directory, then
// `go work vendor -o <tmp>/vendor` runs in the workspace with the tidied files as overlay.
func (Tidier) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	if vendoredWorkspace(dir) {
		return processWorkspace(ctx, dir, manifests, files)
	}
	mod, ok := manifests["go.mod"]
	if !ok {
		return nil, errors.New("go.mod of the module was not patched")
	}
	var overlay map[string][]byte
	if len(manifests) > 1 {
		overlay = make(map[string][]byte, len(manifests)-1)
		for rel, b := range manifests {
			if rel != "go.mod" {
				overlay[rel] = b
			}
		}
	}
	out := map[string][]byte{}
	if err := processModule(ctx, dir, "", mod, files["go.sum"], overlay, gomod.Vendors(dir), out); err != nil {
		return nil, err
	}
	return out, nil
}

// processModule tidies the module in dir and, when vendor is set, vendors it, adding go.mod,
// go.sum and the changed vendor files to out under prefix (the module directory relative to
// the scope, "" or ending in "/").
func processModule(ctx context.Context, dir, prefix string, mod, sum []byte, overlay map[string][]byte, vendor bool, out map[string][]byte) error {
	tmp, err := os.MkdirTemp("", "deps-tidy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	newMod, newSum, arg, err := tidyIn(ctx, tmp, dir, mod, sum, overlay)
	if err != nil {
		return err
	}
	out[prefix+"go.mod"], out[prefix+"go.sum"] = newMod, newSum
	if !vendor {
		return nil
	}
	return vendorModule(ctx, tmp, dir, arg, prefix, out)
}

// processWorkspace is Process in the vendoring workspace dir: manifests are the patched
// go.mod files of its modules and the .go files of those, by slash path relative to dir;
// files holds the modules' go.sum files. Each patched module is tidied (and vendored when it
// has its own vendor/modules.txt), then `go work vendor` regenerates the workspace's vendor
// directory with GOWORK set to the go.work and the results as overlay.
func processWorkspace(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	var mods []string
	for rel := range manifests {
		if path.Base(rel) == "go.mod" {
			mods = append(mods, path.Dir(rel))
		}
	}
	if len(mods) == 0 {
		return nil, errors.New("no go.mod of the workspace was patched")
	}
	// Longest first, so that a source file belongs to its innermost patched module.
	slices.SortFunc(mods, func(a, b string) int { return len(b) - len(a) })
	overlays := make(map[string]map[string][]byte, len(mods))
	for rel, b := range manifests {
		if path.Base(rel) == "go.mod" {
			continue
		}
		i := slices.IndexFunc(mods, func(m string) bool { return m == "." || strings.HasPrefix(rel, m+"/") })
		if i < 0 {
			return nil, fmt.Errorf("%s is in no patched module", rel)
		}
		if overlays[mods[i]] == nil {
			overlays[mods[i]] = map[string][]byte{}
		}
		overlays[mods[i]][relTo(mods[i], rel)] = b
	}
	out := map[string][]byte{}
	wsOverlay := map[string][]byte{}
	for _, m := range mods {
		prefix := ""
		if m != "." {
			prefix = m + "/"
		}
		mdir := filepath.Join(dir, filepath.FromSlash(m))
		// The workspace's vendor directory is not the root module's own.
		vendor := m != "." && gomod.Vendors(mdir)
		if err := processModule(ctx, mdir, prefix, manifests[prefix+"go.mod"], files[prefix+"go.sum"], overlays[m], vendor, out); err != nil {
			return nil, err
		}
		wsOverlay[prefix+"go.mod"], wsOverlay[prefix+"go.sum"] = out[prefix+"go.mod"], out[prefix+"go.sum"]
		for rel, b := range overlays[m] {
			wsOverlay[prefix+rel] = b
		}
	}
	tmp, err := os.MkdirTemp("", "deps-vendor-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	arg, err := writeOverlay(tmp, dir, wsOverlay)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	env := []string{"GOWORK=" + filepath.Join(abs, "go.work"), "GOFLAGS="}
	if _, err := run(ctx, dir, env, "work", "vendor", "-o", filepath.Join(tmp, "vendor"), arg); err != nil {
		return nil, inDir("go work vendor", dir, err)
	}
	if err := diffTree(filepath.Join(dir, "vendor"), filepath.Join(tmp, "vendor"), "vendor", out); err != nil {
		return nil, err
	}
	return out, nil
}

// relTo returns the slash path p relative to its ancestor directory base.
func relTo(base, p string) string {
	if base == "." {
		return p
	}
	return strings.TrimPrefix(p, base+"/")
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): every `// indirect`
// requirement of dir's go.mod without a replace directive moves to its newest release
// (`go get -modfile=<tmp>/go.mod <module>@upgrade ...`, then Tidy), on temporary copies of
// go.mod and go.sum with the environment of Tidy. The go and toolchain directives and the
// direct requirements stay: an attempt that moves them, or that go rejects as a version
// conflict, is split in halves and retried on the result of the first half, so only a module
// whose upgrade conflicts on its own keeps its version. It returns go.mod and go.sum when
// they changed; without go.sum or indirect requirements it does nothing.
func (Tidier) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	sum, ok := files["go.sum"]
	if !ok {
		return nil, nil
	}
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	f, err := modfile.Parse(filepath.Join(dir, "go.mod"), mod, nil)
	if err != nil {
		return nil, err
	}
	var mods []string
	for _, r := range f.Require {
		if r.Indirect && !slices.ContainsFunc(f.Replace, func(rp *modfile.Replace) bool { return rp.Old.Path == r.Mod.Path }) {
			mods = append(mods, r.Mod.Path)
		}
	}
	if len(mods) == 0 {
		return nil, nil
	}
	tmp, err := os.MkdirTemp("", "deps-maintain-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	u := upgrader{dir: dir, mod: filepath.Join(tmp, "go.mod"), sum: filepath.Join(tmp, "go.sum"), fixed: fixed(f)}
	if err := u.write(mod, sum); err != nil {
		return nil, err
	}
	if err := u.upgrade(ctx, mods); err != nil {
		return nil, err
	}
	upMod, upSum, err := u.read()
	if err != nil || bytes.Equal(upMod, mod) {
		return nil, err
	}
	out := map[string][]byte{}
	if err := processModule(ctx, dir, "", upMod, upSum, nil, gomod.Vendors(dir), out); err != nil {
		return nil, err
	}
	if bytes.Equal(out["go.mod"], mod) {
		delete(out, "go.mod")
	}
	if bytes.Equal(out["go.sum"], sum) {
		delete(out, "go.sum")
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// conflictRe matches the go get errors of a version conflict between requested modules
// (`go: a@upgrade (v1.1.0) requires b@v1.5.0, not b@upgrade (v1.4.0)`) and of a requested
// version needing a newer go than the running one (`go: a@upgrade: a@v1.1.0 requires go >= 1.30`).
var conflictRe = regexp.MustCompile(`(?m)^go: \S+@.* requires (\S+, not |go >= )`)

// upgrader runs the go get attempts of Maintain on the go.mod and go.sum copies mod and sum.
type upgrader struct {
	dir, mod, sum string
	fixed         string // fixed of the original go.mod
}

// fixed renders what lock file maintenance must not move: the go and toolchain directives
// and the direct requirements.
func fixed(f *modfile.File) string {
	var lines []string
	if f.Go != nil {
		lines = append(lines, "go "+f.Go.Version)
	}
	if f.Toolchain != nil {
		lines = append(lines, "toolchain "+f.Toolchain.Name)
	}
	for _, r := range f.Require {
		if !r.Indirect {
			lines = append(lines, r.Mod.String())
		}
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// upgrade moves mods to @upgrade: all at once or, on a conflict, each half in turn. A single
// conflicting module keeps its version.
func (u *upgrader) upgrade(ctx context.Context, mods []string) error {
	ok, err := u.try(ctx, mods)
	if err != nil || ok {
		return err
	}
	if len(mods) == 1 {
		slog.DebugContext(ctx, "lock file maintenance keeps a conflicting module", "dir", u.dir, "module", mods[0])
		return nil
	}
	h := len(mods) / 2
	if err := u.upgrade(ctx, mods[:h]); err != nil {
		return err
	}
	return u.upgrade(ctx, mods[h:])
}

// try runs `go get <mods>@upgrade` on the copies and reports whether it kept the fixed part of
// go.mod; a conflict restores the copies. Other go failures are errors.
func (u *upgrader) try(ctx context.Context, mods []string) (bool, error) {
	mod, sum, err := u.read()
	if err != nil {
		return false, err
	}
	args := make([]string, 0, len(mods)+2)
	args = append(args, "get", "-modfile="+u.mod)
	for _, m := range mods {
		args = append(args, m+"@upgrade")
	}
	var ok bool
	if _, err := run(ctx, u.dir, []string{"GOWORK=off", "GOFLAGS="}, args...); err != nil {
		var ce *cmdError
		if !errors.As(err, &ce) || !conflictRe.MatchString(ce.stderr) {
			return false, inDir("go get", u.dir, err)
		}
	} else {
		b, err := os.ReadFile(u.mod)
		if err != nil {
			return false, err
		}
		f, err := modfile.Parse(u.mod, b, nil)
		if err != nil {
			return false, err
		}
		ok = fixed(f) == u.fixed
	}
	if !ok {
		return false, u.write(mod, sum)
	}
	return true, nil
}

func (u *upgrader) read() (mod, sum []byte, err error) {
	if mod, err = os.ReadFile(u.mod); err != nil {
		return nil, nil, err
	}
	if sum, err = os.ReadFile(u.sum); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return mod, sum, nil
}

func (u *upgrader) write(mod, sum []byte) error {
	if err := os.WriteFile(u.mod, mod, 0o644); err != nil {
		return err
	}
	return os.WriteFile(u.sum, sum, 0o644)
}

// cmdError is a failed go command with the tail of its stderr.
type cmdError struct {
	args   string
	stderr string
	err    error
}

func (e *cmdError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("go %s: %v", e.args, e.err)
	}
	return fmt.Sprintf("go %s: %s", e.args, e.stderr)
}

func (e *cmdError) Unwrap() error { return e.err }

// inDir wraps err of the go command what run in dir, with go's stderr when it wrote any.
func inDir(what, dir string, err error) error {
	var ce *cmdError
	if errors.As(err, &ce) && ce.stderr != "" {
		return fmt.Errorf("%s in %s: %s", what, dir, ce.stderr)
	}
	return fmt.Errorf("%s in %s: %w", what, dir, err)
}

// run executes go with args in dir, with env appended to the process environment, and returns stdout.
func run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout bytes.Buffer
	stderr := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "go command", "args", args, "dir", dir, "duration", time.Since(start), "err", err)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, err
		}
		return nil, &cmdError{args: strings.Join(args, " "), stderr: toolrun.Tail(stderr.Bytes(), 20), err: err}
	}
	return stdout.Bytes(), nil
}
