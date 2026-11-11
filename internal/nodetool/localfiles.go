package nodetool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

// tree is the repository of a lock root: files are read through root, opened on the
// repository top (the nearest directory at or above the lock root holding .git, else the lock
// root), and addressed by slash paths relative to the lock root, which lies at prefix below the
// top. A lock root below the top can so reference files above it (backstage's
// workspaces/ui/.yarnrc.yml has `yarnPath: ../../.yarn/releases/yarn-4.18.1.cjs`).
type tree struct {
	root   *os.Root
	top    string
	prefix string // "." at the top
}

func openTree(dir string) (*tree, error) {
	top := dir
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			top = d
			break
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	prefix, err := filepath.Rel(top, dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(top)
	if err != nil {
		return nil, err
	}
	return &tree{root: root, top: top, prefix: filepath.ToSlash(prefix)}, nil
}

// local returns the cleaned slash path p (relative to the lock root), or "" when it is empty,
// absolute, names the lock root itself or leaves the repository top.
func (t *tree) local(p string) string {
	if p == "" || path.IsAbs(p) || filepath.IsAbs(p) {
		return ""
	}
	p = path.Clean(filepath.ToSlash(p))
	if p == "." || !filepath.IsLocal(filepath.FromSlash(path.Join(t.prefix, p))) {
		return ""
	}
	return p
}

// read reads the file at the local path p; symlinks leaving the top fail.
func (t *tree) read(p string) ([]byte, error) {
	return t.root.ReadFile(filepath.FromSlash(path.Join(t.prefix, p)))
}

// addLocalFiles adds to write (slash paths relative to the lock root -> content) the local
// files the package manager reads while it resolves the lockfile, derived from the
// configuration in write and files rather than by copying directories:
//   - yarn berry `patch:` paths (`#~/p` from the project root, `#./p` from the declaring
//     package.json's directory) in dependency maps and `resolutions`, and in yarn.lock (relative
//     to the workspace of the entry's `locator`);
//   - pnpm `patchedDependencies` of the root package.json (`pnpm.patchedDependencies`) and of
//     pnpm-workspace.yaml;
//   - the targets of `file:`, `link:` and `portal:` specs and of `./`/`../` paths in dependency
//     maps, `resolutions` and `overrides` (npm, `pnpm.overrides`, pnpm-workspace.yaml): a tarball
//     (.tgz, .tar.gz, .tar) itself, a directory's package.json only (scanned in turn);
//   - the pnpmfile pnpm loads (`.pnpmfile.cjs`/`.pnpmfile.mjs`, or the `pnpmfile` setting of
//     pnpm-workspace.yaml or .npmrc).
//
// patch-package patches need no copy: they are applied by a postinstall script, which the
// installs never run. Paths that leave the repository top (also through symlinks) and files
// that cannot be read are left out; the package manager reports what it misses.
func addLocalFiles(ctx context.Context, t *tree, write, files map[string][]byte, lockName string) {
	var queue []string // package.json paths to scan
	for p := range write {
		if p == "package.json" || strings.HasSuffix(p, "/package.json") {
			queue = append(queue, p)
		}
	}
	slices.Sort(queue)
	seen := map[string]bool{}
	add := func(p string) {
		if p = t.local(p); p == "" || seen[p] || write[p] != nil {
			return
		}
		seen[p] = true
		b, err := t.read(p)
		if err != nil {
			slog.DebugContext(ctx, "local file skipped", "top", t.top, "lockRoot", t.prefix, "path", p, "err", err)
			return
		}
		write[p] = b
		if path.Base(p) == "package.json" {
			queue = append(queue, p)
		}
	}

	if lock := write[YarnLock]; lock != nil {
		for _, p := range yarnLockPatches(lock) {
			add(p)
		}
	}
	if lockName == PnpmLock {
		addPnpmFiles(files, add)
	}

	// The manifests last: they include the package.json files of local targets added above.
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		refs, err := manifestRefs(write[p])
		if err != nil {
			continue // left to the package manager to report
		}
		base := path.Dir(p)
		for _, r := range refs.patches {
			for _, f := range yarnPatchPaths(r, base) {
				add(f)
			}
		}
		for _, r := range refs.targets {
			add(localTarget(base, r))
		}
		if p == "package.json" {
			for _, r := range refs.pnpmPatches {
				add(r)
			}
		}
	}
}

// addPnpmFiles adds the files pnpm reads by its configuration: the pnpm-workspace.yaml
// `patchedDependencies` and local `overrides`, and the pnpmfile. A configuration that does
// not decode is left to pnpm to report.
func addPnpmFiles(files map[string][]byte, add func(string)) {
	var ws struct {
		PatchedDependencies map[string]string `yaml:"patchedDependencies"`
		Overrides           map[string]string `yaml:"overrides"`
		Pnpmfile            yaml.Node         `yaml:"pnpmfile"`
	}
	if b := files[pnpmWorkspace]; b != nil {
		_ = yaml.Unmarshal(b, &ws) // a type error still decodes the other fields
	}
	for _, k := range slices.Sorted(maps.Keys(ws.PatchedDependencies)) {
		add(ws.PatchedDependencies[k])
	}
	for _, k := range slices.Sorted(maps.Keys(ws.Overrides)) {
		if t, ok := localSpec(ws.Overrides[k]); ok {
			add(localTarget(".", t))
		}
	}
	pnpmfiles := []string{".pnpmfile.cjs", ".pnpmfile.mjs"}
	switch ws.Pnpmfile.Kind {
	case yaml.ScalarNode:
		pnpmfiles = []string{ws.Pnpmfile.Value}
	case yaml.SequenceNode:
		pnpmfiles = nil
		_ = ws.Pnpmfile.Decode(&pnpmfiles)
	default:
		if p := npmrcValue(files[npmrc], "pnpmfile"); p != "" {
			pnpmfiles = []string{p}
		}
	}
	for _, p := range pnpmfiles {
		add(p)
	}
}

// refsManifest holds the package.json fields that may reference local files.
type refsManifest struct {
	Dependencies         map[string]jsontext.Value `json:"dependencies"`
	DevDependencies      map[string]jsontext.Value `json:"devDependencies"`
	OptionalDependencies map[string]jsontext.Value `json:"optionalDependencies"`
	PeerDependencies     map[string]jsontext.Value `json:"peerDependencies"`
	Resolutions          map[string]jsontext.Value `json:"resolutions"`
	Overrides            jsontext.Value            `json:"overrides"`
	Pnpm                 struct {
		Overrides           map[string]jsontext.Value `json:"overrides"`
		PatchedDependencies map[string]jsontext.Value `json:"patchedDependencies"`
	} `json:"pnpm"`
}

// localRefs are the local references of one package.json: yarn `patch:` ranges (without the
// protocol), local spec paths (localSpec) and pnpm patchedDependencies paths.
type localRefs struct {
	patches, targets, pnpmPatches []string
}

// localMarkers prefilter package.json files: one without any is not decoded.
var localMarkers = [][]byte{[]byte(`patch:`), []byte(`file:`), []byte(`link:`), []byte(`portal:`), []byte(`"./`), []byte(`"../`), []byte(`patchedDependencies`)}

// manifestRefs returns the local references of the package.json pkg.
func manifestRefs(pkg []byte) (localRefs, error) {
	var r localRefs
	if !slices.ContainsFunc(localMarkers, func(m []byte) bool { return bytes.Contains(pkg, m) }) {
		return r, nil
	}
	var m refsManifest
	if err := json.Unmarshal(blankBOM(pkg), &m, lenient); err != nil {
		return r, err
	}
	spec := func(v jsontext.Value) {
		var s string
		if v.Kind() != '"' || json.Unmarshal(v, &s) != nil {
			return
		}
		if rest, ok := strings.CutPrefix(s, "patch:"); ok {
			r.patches = append(r.patches, rest)
		} else if t, ok := localSpec(s); ok {
			r.targets = append(r.targets, t)
		}
	}
	for _, deps := range []map[string]jsontext.Value{m.Dependencies, m.DevDependencies, m.OptionalDependencies, m.PeerDependencies, m.Resolutions, m.Pnpm.Overrides} {
		for _, k := range slices.Sorted(maps.Keys(deps)) {
			spec(deps[k])
		}
	}
	// npm overrides nest: {"foo": {"bar": "file:./x", ".": "1.0.0"}}.
	var walk func(v jsontext.Value)
	walk = func(v jsontext.Value) {
		if v.Kind() != '{' {
			spec(v)
			return
		}
		var o map[string]jsontext.Value
		if json.Unmarshal(v, &o, lenient) != nil {
			return
		}
		for _, k := range slices.Sorted(maps.Keys(o)) {
			walk(o[k])
		}
	}
	if len(m.Overrides) > 0 {
		walk(m.Overrides)
	}
	for _, k := range slices.Sorted(maps.Keys(m.Pnpm.PatchedDependencies)) {
		var s string
		if json.Unmarshal(m.Pnpm.PatchedDependencies[k], &s) == nil {
			r.pnpmPatches = append(r.pnpmPatches, s)
		}
	}
	return r, nil
}

// localSpec returns the path of a `file:`, `link:` or `portal:` spec or of a `./`/`../` path
// spec.
func localSpec(s string) (string, bool) {
	for _, proto := range []string{"file:", "link:", "portal:"} {
		if rest, ok := strings.CutPrefix(s, proto); ok {
			return rest, true
		}
	}
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return s, true
	}
	return "", false
}

// localTarget returns the file to copy for the local spec path p of the package.json in base:
// a tarball itself, else the directory's package.json ("" for an absolute p).
func localTarget(base, p string) string {
	if strings.HasSuffix(p, ".tgz") || strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".tar") {
		return join(base, p)
	}
	if d := join(base, p); d != "" {
		return path.Join(d, "package.json")
	}
	return ""
}

// yarnPatchPaths returns the patch files of a yarn `patch:` range without the protocol
// (`<source>#<path>[&<path>…][::<params>]`): `~/` paths from the project root, others from
// base. Builtin patches (`builtin<compat/…>`) are left out; `optional!` is dropped.
func yarnPatchPaths(rng, base string) []string {
	_, sel, ok := strings.Cut(rng, "#")
	if !ok {
		return nil
	}
	sel, _, _ = strings.Cut(sel, "::")
	var out []string
	for p := range strings.SplitSeq(sel, "&") {
		p = strings.TrimPrefix(p, "optional!")
		if strings.HasPrefix(p, "builtin<") {
			continue
		}
		if u, err := url.PathUnescape(p); err == nil {
			p = u
		}
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			out = append(out, join(".", rest))
		} else {
			out = append(out, join(base, p))
		}
	}
	return out
}

// yarnLockPatches returns the patch files of the `patch:` ranges in a yarn berry lockfile;
// relative ones are resolved from the workspace named by the entry's `locator` parameter, and
// those of a non-workspace locator (a patch inside a dependency) are left out.
func yarnLockPatches(lock []byte) []string {
	var out []string
	for rest := lock; ; {
		i := bytes.Index(rest, []byte("patch:"))
		if i < 0 {
			return out
		}
		// A range starts the value or follows the name (`name@patch:`); `yaml-diff-patch:` is a
		// dependency name.
		boundary := i == 0 || strings.IndexByte(`@" `, rest[i-1]) >= 0
		rest = rest[i+len("patch:"):]
		end := bytes.IndexAny(rest, "\"\n,")
		if end < 0 {
			end = len(rest)
		}
		if !boundary || bytes.IndexByte(rest[:end], '#') < 0 {
			rest = rest[end:]
			continue
		}
		rng := string(rest[:end])
		rest = rest[end:]
		base := "."
		if _, params, ok := strings.Cut(rng, "::"); ok {
			q, _ := url.ParseQuery(params)
			if loc := q.Get("locator"); loc != "" {
				_, ws, ok := strings.CutLast(loc, "@workspace:")
				if !ok {
					continue
				}
				base = ws
			}
		}
		for _, p := range yarnPatchPaths(rng, base) {
			if p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
}

// join joins the path p to base (slash paths relative to the lock root); "" when p is empty or
// absolute. tree.local checks where the result lies.
func join(base, p string) string {
	if p == "" || path.IsAbs(p) || filepath.IsAbs(p) {
		return ""
	}
	return path.Join(base, filepath.ToSlash(p))
}

// npmrcValue returns the value of key in an .npmrc (the last assignment wins), or "".
func npmrcValue(rc []byte, key string) string {
	v := ""
	sc := bufio.NewScanner(bytes.NewReader(rc))
	for sc.Scan() {
		k, val, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.TrimSpace(k) == key {
			v = strings.Trim(strings.TrimSpace(val), `"'`)
		}
	}
	return v
}
