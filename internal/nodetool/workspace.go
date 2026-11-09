package nodetool

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v4"
)

// rootManifest holds the package.json fields of a lock root that nodetool needs.
type rootManifest struct {
	PackageManager string         `json:"packageManager"`
	Workspaces     jsontext.Value `json:"workspaces"`
}

// parseRoot decodes the root package.json; a document without the fields (checked with a
// cheap byte prefilter) is not decoded.
func parseRoot(pkg []byte) (rootManifest, error) {
	var m rootManifest
	if !bytes.Contains(pkg, []byte(`"workspaces"`)) && !bytes.Contains(pkg, []byte(`"packageManager"`)) {
		return m, nil
	}
	err := json.Unmarshal(pkg, &m, lenient)
	return m, err
}

// lenient accepts package.json files with duplicate names (the last wins), as npm's
// JSON.parse does.
var lenient = jsontext.AllowDuplicateNames(true)

// tool returns the tool and version of the packageManager field (`pnpm@9.1.0+sha512.…`).
func (m rootManifest) tool() (name, version string) {
	name, version, _ = strings.Cut(m.PackageManager, "@")
	version, _, _ = strings.Cut(version, "+")
	return name, version
}

// workspaces are the member directory globs of a lock root (slash paths relative to it).
type workspaces struct {
	include, exclude []string
}

// parseWorkspaces collects the globs of the package.json `workspaces` field (an array or
// `{"packages": [...]}`) and of the pnpm-workspace.yaml `packages` list (nil = absent);
// `!glob` excludes.
func parseWorkspaces(m rootManifest, pnpmWS []byte) (workspaces, error) {
	var globs []string
	switch raw := bytes.TrimSpace(m.Workspaces); {
	case len(raw) == 0:
	case raw[0] == '[':
		if err := json.Unmarshal(raw, &globs); err != nil {
			return workspaces{}, err
		}
	case raw[0] == '{':
		var obj struct {
			Packages []string `json:"packages"`
		}
		if err := json.Unmarshal(raw, &obj, lenient); err != nil {
			return workspaces{}, err
		}
		globs = obj.Packages
	}
	if pnpmWS != nil {
		var ws struct {
			Packages []string `yaml:"packages"`
		}
		if err := yaml.Unmarshal(pnpmWS, &ws); err != nil {
			return workspaces{}, err
		}
		globs = append(globs, ws.Packages...)
	}
	var w workspaces
	for _, g := range globs {
		neg := strings.HasPrefix(g, "!")
		g = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(g, "!"), "./"), "/")
		switch {
		case g == "" || g == ".":
		case neg:
			w.exclude = append(w.exclude, g)
		default:
			w.include = append(w.include, g)
		}
	}
	return w, nil
}

// match reports whether the member directory rel is included and not excluded.
func (w workspaces) match(rel string) bool {
	return matchAny(w.include, rel) && !matchAny(w.exclude, rel)
}

func matchAny(globs []string, rel string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, rel); ok && err == nil {
			return true
		}
	}
	return false
}

// members returns the package.json paths (slash, relative to dir) of the workspace members
// below dir. Each glob is walked from its literal base and only as deep as it can match;
// node_modules and .git are never entered.
func (w workspaces) members(dir string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, g := range w.include {
		base, rest := doublestar.SplitPattern(g)
		if base == "/" || !filepath.IsLocal(filepath.FromSlash(base)) {
			continue // absolute and out-of-root globs are not workspace members
		}
		depth := -1 // levels below base the glob can match; -1 = any (`**`)
		if !strings.Contains(rest, "**") {
			depth = strings.Count(rest, "/") + 1
		}
		start := filepath.Join(dir, filepath.FromSlash(base))
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == start && errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if p != start && (d.Name() == "node_modules" || d.Name() == ".git") {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel != "." && !seen[rel] && w.match(rel) {
				if st, err := os.Stat(filepath.Join(p, "package.json")); err == nil && st.Mode().IsRegular() {
					seen[rel] = true
					out = append(out, path.Join(rel, "package.json"))
				}
			}
			if depth >= 0 && p != start {
				below, err := filepath.Rel(start, p)
				if err != nil {
					return err
				}
				if strings.Count(filepath.ToSlash(below), "/")+1 >= depth {
					return filepath.SkipDir
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Scope returns the lock root of the package.json manifest (slash paths relative to root): the
// nearest directory at or above the manifest's, within root, that holds a package-lock.json,
// pnpm-lock.yaml or yarn.lock. It is accepted when it is the manifest's own directory or when
// its package.json `workspaces` or pnpm-workspace.yaml `packages` globs match the manifest's
// directory; otherwise the manifest has no lockfile.
func (Locker) Scope(ctx context.Context, root, manifest string) (string, bool) {
	return lockScope(ctx, root, manifest, nil)
}

// scopeCache memoises the lockfile and workspace lookups of lockScope across the manifests of
// one tree; the directory keys are absolute.
type scopeCache struct {
	lock map[string]string
	ws   map[string]cachedWorkspaces
}

type cachedWorkspaces struct {
	ws  workspaces
	err error
}

func newScopeCache() *scopeCache {
	return &scopeCache{lock: map[string]string{}, ws: map[string]cachedWorkspaces{}}
}

// lockIn is lockIn of dir, cached when c is not nil.
func (c *scopeCache) lockIn(dir string) string {
	if c == nil {
		return lockIn(dir)
	}
	n, ok := c.lock[dir]
	if !ok {
		n = lockIn(dir)
		c.lock[dir] = n
	}
	return n
}

// readWorkspaces is readWorkspaces of dir, cached when c is not nil.
func (c *scopeCache) readWorkspaces(dir string) (workspaces, error) {
	if c == nil {
		return readWorkspaces(dir)
	}
	r, ok := c.ws[dir]
	if !ok {
		r.ws, r.err = readWorkspaces(dir)
		c.ws[dir] = r
	}
	return r.ws, r.err
}

// lockScope is Scope with the lookups cached in c (nil = no caching).
func lockScope(ctx context.Context, root, manifest string, c *scopeCache) (string, bool) {
	member := path.Dir(manifest)
	for dir := member; ; dir = path.Dir(dir) {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if c.lockIn(abs) != "" {
			if dir == member {
				return dir, true
			}
			rel := member
			if dir != "." {
				rel = strings.TrimPrefix(member, dir+"/")
			}
			ws, err := c.readWorkspaces(abs)
			if err != nil {
				slog.DebugContext(ctx, "workspace root unreadable", "dir", dir, "manifest", manifest, "err", err)
				return "", false
			}
			if ws.match(rel) {
				return dir, true
			}
			slog.DebugContext(ctx, "not a workspace member", "dir", dir, "manifest", manifest)
			return "", false
		}
		if dir == "." {
			return "", false
		}
	}
}

// lockIn returns the name of the first lockfile present in dir, or "".
func lockIn(dir string) string {
	for _, n := range lockfiles {
		if st, err := os.Stat(filepath.Join(dir, n)); err == nil && st.Mode().IsRegular() {
			return n
		}
	}
	return ""
}

// readWorkspaces reads the workspace globs of the lock root dir.
func readWorkspaces(dir string) (workspaces, error) {
	pkg, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return workspaces{}, err
	}
	m, err := parseRoot(pkg)
	if err != nil {
		return workspaces{}, err
	}
	pnpmWS, err := os.ReadFile(filepath.Join(dir, pnpmWorkspace))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return workspaces{}, err
	}
	return parseWorkspaces(m, pnpmWS)
}

// WorkspaceConfigs reads the workspace configuration (package.json `workspaces` and
// pnpm-workspace.yaml `packages`) of each directory once, so the members of one workspace share
// the root's read and parse. The zero value is ready; it is safe for concurrent use.
type WorkspaceConfigs struct {
	mu sync.Mutex
	m  map[string]*workspaceConfig // absolute directory
}

type workspaceConfig struct {
	once sync.Once
	ws   workspaces
	err  error
}

// read is readWorkspaces of the absolute directory dir, read once.
func (c *WorkspaceConfigs) read(dir string) (workspaces, error) {
	c.mu.Lock()
	e := c.m[dir]
	if e == nil {
		if c.m == nil {
			c.m = map[string]*workspaceConfig{}
		}
		e = &workspaceConfig{}
		c.m[dir] = e
	}
	c.mu.Unlock()
	e.once.Do(func() { e.ws, e.err = readWorkspaces(dir) })
	return e.ws, e.err
}

// Root returns the workspace root of the package.json directory dir (slash paths relative to
// the absolute root): the nearest directory at or above dir, within root, that declares
// workspaces (package.json `workspaces` or pnpm-workspace.yaml `packages`) and is dir itself or
// includes dir in its globs. Unlike Scope it needs no lockfile. ok is false outside any
// workspace.
func (c *WorkspaceConfigs) Root(root, dir string) (string, bool) {
	for ws := dir; ; ws = path.Dir(ws) {
		w, err := c.read(filepath.Join(root, filepath.FromSlash(ws)))
		switch {
		case err != nil:
			slog.Debug("workspace configuration unreadable", "dir", ws, "err", err)
		case len(w.include) == 0:
		case ws == dir:
			return ws, true
		case w.match(strings.TrimPrefix(dir, ws+"/")): // for ws "." dir is already relative
			return ws, true
		}
		if ws == "." {
			return "", false
		}
	}
}

// Packages returns the package names of the workspace rooted at the absolute directory dir:
// the root package and every member. Unnamed or unparsable package.json files are left out.
func (c *WorkspaceConfigs) Packages(dir string) (map[string]bool, error) {
	w, err := c.read(dir)
	if err != nil {
		return nil, err
	}
	members, err := w.members(dir)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(members)+1)
	for _, p := range append(members, "package.json") {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		var pkg struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(b, &pkg, lenient); err != nil {
			slog.Debug("workspace package unparsable", "file", p, "err", err)
			continue
		}
		if pkg.Name != "" {
			names[pkg.Name] = true
		}
	}
	return names, nil
}
