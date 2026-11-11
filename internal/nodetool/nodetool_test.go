package nodetool

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// tarball builds the npm package tarball of tiny@version.
func tarball(t *testing.T, version string) []byte {
	t.Helper()
	pj := []byte(`{"name":"tiny","version":"` + version + `"}`)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(pj))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(pj); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// registry serves the package "tiny" in versions (default 1.0.0 and 1.1.0; the last is the
// latest), metadata and tarballs.
func registry(t *testing.T, versions ...string) *httptest.Server {
	t.Helper()
	tgz := map[string][]byte{}
	var srv *httptest.Server
	var latest string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tiny" {
			meta := map[string]any{}
			for v, b := range tgz {
				s512, s1 := sha512.Sum512(b), sha1.Sum(b)
				meta[v] = map[string]any{"name": "tiny", "version": v, "dist": map[string]string{
					"tarball":   srv.URL + "/tiny/-/tiny-" + v + ".tgz",
					"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]),
					"shasum":    hex.EncodeToString(s1[:]),
				}}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.MarshalWrite(w, map[string]any{"name": "tiny", "dist-tags": map[string]string{"latest": latest}, "versions": meta})
			return
		}
		for v, b := range tgz {
			if r.URL.Path == "/tiny/-/tiny-"+v+".tgz" {
				_, _ = w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	}))
	if len(versions) == 0 {
		versions = []string{"1.0.0", "1.1.0"}
	}
	latest = versions[len(versions)-1]
	for _, v := range versions {
		tgz[v] = tarball(t, v)
	}
	t.Cleanup(srv.Close)
	return srv
}

// isolate keeps npm away from the user's config, cache and registry settings.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("npm_config_userconfig", filepath.Join(home, "npmrc"))
	t.Setenv("npm_config_cache", filepath.Join(home, "cache"))
	for _, k := range []string{"npm_config_registry", "NPM_CONFIG_REGISTRY"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

func manifest(rng string) []byte {
	return []byte(`{"name":"app","version":"1.0.0","dependencies":{"tiny":"` + rng + `"}}` + "\n")
}

// TestProcessRealNpm regenerates a lockfile with the real npm against a local registry.
func TestProcessRealNpm(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not in PATH")
	}
	isolate(t)
	srv := registry(t)
	rc := []byte("registry=" + srv.URL + "/\n")
	t.Run("update", func(t *testing.T) {
		dir := t.TempDir()
		// Create the lockfile at 1.0.0 with npm itself, so the format is npm's.
		first := `{"name":"app","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"app","version":"1.0.0"}}}`
		out, err := Locker{}.Process(t.Context(), dir, pkgs("package.json", manifest("1.0.0")), map[string][]byte{PackageLock: []byte(first), npmrc: rc})
		if err != nil {
			t.Fatal(err)
		}
		lock := out[PackageLock]
		if !strings.Contains(string(lock), "tiny-1.0.0.tgz") {
			t.Fatalf("initial lock = %s", lock)
		}

		// Unchanged manifest: nothing to write.
		out, err = Locker{}.Process(t.Context(), dir, pkgs("package.json", manifest("1.0.0")), map[string][]byte{PackageLock: lock, npmrc: rc})
		if err != nil || out != nil {
			t.Fatalf("unchanged: out = %v, err = %v", out, err)
		}

		out, err = Locker{}.Process(t.Context(), dir, pkgs("package.json", manifest("^1.1.0")), map[string][]byte{PackageLock: lock, npmrc: rc})
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 || !strings.Contains(string(out[PackageLock]), "tiny-1.1.0.tgz") || !strings.Contains(string(out[PackageLock]), `"tiny": "^1.1.0"`) {
			t.Errorf("updated lock = %s", out[PackageLock])
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("dir was written: %v", entries)
		}
	})

	t.Run("workspace", func(t *testing.T) {
		dir := t.TempDir()
		member := func(name, rng string) []byte {
			return []byte(`{"name":"` + name + `","version":"1.0.0","dependencies":{"tiny":"` + rng + `"}}` + "\n")
		}
		write(t, dir, map[string]string{
			"package.json":            `{"name":"ws","private":true,"workspaces":["packages/*"]}`,
			"packages/a/package.json": string(member("a", "1.0.0")),
			"packages/b/package.json": string(member("b", "1.0.0")),
		})
		first := []byte(`{"name":"ws","lockfileVersion":3,"requires":true,"packages":{}}`)
		out, err := Locker{}.Process(t.Context(), dir, nil, map[string][]byte{PackageLock: first, npmrc: rc})
		if err != nil {
			t.Fatal(err)
		}
		lock := out[PackageLock]
		if !strings.Contains(string(lock), "tiny-1.0.0.tgz") || !strings.Contains(string(lock), `"packages/b"`) {
			t.Fatalf("initial lock = %s", lock)
		}
		manifests := map[string][]byte{"packages/a/package.json": member("a", "^1.1.0"), "packages/b/package.json": member("b", "^1.1.0")}
		out, err = Locker{}.Process(t.Context(), dir, manifests, map[string][]byte{PackageLock: lock, npmrc: rc})
		if err != nil {
			t.Fatal(err)
		}
		got := string(out[PackageLock])
		if !strings.Contains(got, "tiny-1.1.0.tgz") || strings.Contains(got, "tiny-1.0.0.tgz") || strings.Count(got, `"tiny": "^1.1.0"`) != 2 {
			t.Errorf("updated workspace lock = %s", got)
		}
	})

	t.Run("npm failure", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Locker{}.Process(t.Context(), dir, pkgs("package.json", []byte("{")), map[string][]byte{PackageLock: []byte("{}"), npmrc: rc})
		if err == nil || !strings.HasPrefix(err.Error(), "npm install in "+dir+": ") || !strings.Contains(err.Error(), "JSON") {
			t.Errorf("err = %v", err)
		}
	})
}

// TestProcessRealPnpm updates a pnpm workspace lockfile with the real pnpm against a local
// registry.
func TestProcessRealPnpm(t *testing.T) {
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not in PATH")
	}
	isolate(t)
	srv := registry(t)
	dir := t.TempDir()
	member := func(rng string) []byte {
		return []byte(`{"name":"a","version":"1.0.0","dependencies":{"tiny":"` + rng + `"}}` + "\n")
	}
	write(t, dir, map[string]string{
		"package.json":            `{"name":"ws","private":true}`,
		"packages/a/package.json": string(member("1.0.0")),
	})
	files := map[string][]byte{
		PnpmLock:      []byte("lockfileVersion: '9.0'\n"),
		npmrc:         []byte("registry=" + srv.URL + "/\n"),
		pnpmWorkspace: []byte("packages:\n  - packages/*\n"),
	}
	out, err := Locker{}.Process(t.Context(), dir, nil, files)
	if err != nil {
		t.Fatal(err)
	}
	files[PnpmLock] = out[PnpmLock]
	out, err = Locker{}.Process(t.Context(), dir, pkgs("packages/a/package.json", member("^1.1.0")), files)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out[PnpmLock]); !strings.Contains(got, "tiny@1.1.0") || strings.Contains(got, "tiny@1.0.0") {
		t.Errorf("updated lock = %s", got)
	}
}

func pkgs(p string, b []byte) map[string][]byte { return map[string][]byte{p: b} }

// write creates files (slash paths relative to dir).
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScope(t *testing.T) {
	for _, tt := range []struct {
		name     string
		files    map[string]string
		manifest string
		want     string // "" = no lock root
	}{
		{"own lock", map[string]string{"web/package-lock.json": "{}"}, "web/package.json", "web"},
		{"root manifest", map[string]string{"package-lock.json": "{}"}, "package.json", "."},
		{"npm workspaces array", map[string]string{"package.json": `{"workspaces": ["packages/*"]}`, "package-lock.json": "{}"}, "packages/a/package.json", "."},
		{"npm workspaces dot slash", map[string]string{"package.json": `{"workspaces": ["./tools/cli/"]}`, "package-lock.json": "{}"}, "tools/cli/package.json", "."},
		{"workspaces object", map[string]string{"package.json": `{"workspaces": {"packages": ["apps/**"]}}`, "yarn.lock": ""}, "apps/web/ui/package.json", "."},
		{"pnpm-workspace.yaml", map[string]string{"pnpm-workspace.yaml": "packages:\n  - libs/*\n  - '!libs/skip'\n", "pnpm-lock.yaml": ""}, "libs/a/package.json", "."},
		{"pnpm exclude", map[string]string{"pnpm-workspace.yaml": "packages:\n  - libs/*\n  - '!libs/skip'\n", "pnpm-lock.yaml": ""}, "libs/skip/package.json", ""},
		{"not a member", map[string]string{"package.json": `{"workspaces": ["packages/*"]}`, "package-lock.json": "{}"}, "examples/x/package.json", ""},
		{"no workspaces", map[string]string{"package.json": `{"name": "x"}`, "package-lock.json": "{}"}, "packages/a/package.json", ""},
		{"no lock", map[string]string{"package.json": `{"workspaces": ["packages/*"]}`}, "packages/a/package.json", ""},
		{"nested lock wins", map[string]string{"package.json": `{"workspaces": ["packages/*"]}`, "package-lock.json": "{}", "packages/c/package-lock.json": "{}"}, "packages/c/package.json", "packages/c"},
		{"nearest lock only", map[string]string{"package.json": `{"workspaces": ["packages/**"]}`, "package-lock.json": "{}", "packages/c/package-lock.json": "{}"}, "packages/c/sub/package.json", ""},
		{"nested workspace", map[string]string{"sub/ws/package.json": `{"workspaces": ["m"]}`, "sub/ws/package-lock.json": "{}"}, "sub/ws/m/package.json", "sub/ws"},
		{"broken root package.json", map[string]string{"package.json": `{"workspaces": [`, "package-lock.json": "{}"}, "packages/a/package.json", ""},
		{"unreadable pnpm-workspace.yaml", map[string]string{"pnpm-workspace.yaml/x": "", "pnpm-lock.yaml": ""}, "libs/a/package.json", ""},
		{"lockfile is a directory", map[string]string{"package-lock.json/x": "", "package.json": `{"workspaces": ["*"]}`}, "a/package.json", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tt.files)
			got, ok := Locker{}.Scope(t.Context(), root, tt.manifest)
			if ok != (tt.want != "") || got != tt.want {
				t.Errorf("Scope(%s) = %q, %v, want %q", tt.manifest, got, ok, tt.want)
			}
		})
	}
}

func TestWorkspaceConfigs(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"package.json":                 `{"name": "ws", "workspaces": ["packages/*"]}`,
		"packages/a/package.json":      `{"name": "a"}`,
		"packages/a/deep/package.json": `{"name": "deep"}`,
		"sub/package.json":             `{"name": "sub"}`,
		"sub/pnpm-workspace.yaml":      "packages: [libs/*]\n",
		"sub/libs/l/package.json":      `{"name": "l"}`,
		"broken/package.json":          `{"workspaces": [`,
		"broken/x/package.json":        "{}",
	})
	var confs WorkspaceConfigs
	for _, c := range []struct{ dir, want string }{
		{".", "."},
		{"packages/a", "."},
		{"packages/a/deep", ""},
		{"sub", "sub"},
		{"sub/libs/l", "sub"},
		{"broken/x", ""},
		{"elsewhere", ""},
	} {
		got, ok := confs.Root(root, c.dir)
		if ok != (c.want != "") || got != c.want {
			t.Errorf("Root(%s) = %q, %v, want %q", c.dir, got, ok, c.want)
		}
	}

	// Unnamed and unparsable members are left out.
	write(t, root, map[string]string{"packages/nameless/package.json": "{}", "packages/bad/package.json": "{"})
	if names, err := (&WorkspaceConfigs{}).Packages(root); err != nil || len(names) != 2 || !names["ws"] || !names["a"] {
		t.Errorf("Packages = %v, %v; want ws and a", names, err)
	}
	if _, err := (&WorkspaceConfigs{}).Packages(filepath.Join(root, "broken")); err == nil {
		t.Error("broken workspace: no error")
	}
	write(t, root, map[string]string{"dirpkg/package.json/x": "", "dirpkg/pnpm-workspace.yaml": "packages: [m]\n"})
	if _, err := (&WorkspaceConfigs{}).Packages(filepath.Join(root, "dirpkg")); err == nil {
		t.Error("unreadable root package.json: no error")
	}
	if names, err := (&WorkspaceConfigs{}).Packages(filepath.Join(root, "none")); err != nil || len(names) != 0 {
		t.Errorf("no workspace = %v, %v", names, err)
	}
}

// TestManifestBOM covers package.json files written with a UTF-8 byte order mark: they are
// read like the same file without one, and the edits pinning their ranges keep the mark.
func TestManifestBOM(t *testing.T) {
	m, err := parseRoot([]byte("\ufeff" + `{"packageManager": "pnpm@9.1.0", "workspaces": ["packages/*"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if name, version := m.tool(); name != "pnpm" || version != "9.1.0" || string(m.Workspaces) != `["packages/*"]` {
		t.Errorf("parseRoot = %q, %q, %q", name, version, m.Workspaces)
	}
	refs, err := manifestRefs([]byte("\ufeff" + `{"dependencies": {"a": "file:./a.tgz"}}`))
	if err != nil || !slices.Equal(refs.targets, []string{"./a.tgz"}) {
		t.Errorf("manifestRefs = %v, %v", refs, err)
	}

	in := "\ufeff" + `{"dependencies": {"a": "^1.0.0"}}`
	got, err := pinManifests(map[string][]byte{"package.json": []byte(in)},
		map[string][]dep.Update{"package.json": {lockUp("dependencies", "a", "^1.0.0", "1.3.0")}})
	if want := "\ufeff" + `{"dependencies": {"a": "1.3.0"}}`; err != nil || string(got["package.json"]) != want {
		t.Errorf("pinManifests = %q, %v, want %q", got["package.json"], err, want)
	}
}

func TestMembers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"packages/a/package.json":                  "{}",
		"packages/b/package.json":                  "{}",
		"packages/b/deep/package.json":             "{}", // below packages/*
		"packages/nopkg/README":                    "",
		"packages/skip/package.json":               "{}",
		"tools/x/y/package.json":                   "{}",
		"tools/node_modules/z/package.json":        "{}",
		"tools/.git/package.json":                  "{}",
		"exact/package.json":                       "{}",
		"packages/a/node_modules/dep/package.json": "{}",
		"other/package.json":                       "{}",
		"packages/file.txt":                        "",
		"tools/w/package.json":                     "{}",
	})
	ws, err := parseWorkspaces(rootManifest{Workspaces: []byte(`["packages/*", "tools/**", "./exact/", "missing/*", "/abs/*", "packages/*", "!packages/skip"]`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ws.members(dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"exact/package.json", "packages/a/package.json", "packages/b/package.json", "tools/w/package.json", "tools/x/y/package.json"}
	if !slices.Equal(got, want) {
		t.Errorf("members = %v, want %v", got, want)
	}

	if _, err := parseWorkspaces(rootManifest{Workspaces: []byte(`{"packages": 1}`)}, nil); err == nil {
		t.Error("bad workspaces object: no error")
	}
	if _, err := parseWorkspaces(rootManifest{Workspaces: []byte(`[1]`)}, nil); err == nil {
		t.Error("bad workspaces array: no error")
	}
	if _, err := parseWorkspaces(rootManifest{}, []byte("packages: {")); err == nil {
		t.Error("bad pnpm-workspace.yaml: no error")
	}
	if ws, err := parseWorkspaces(rootManifest{Workspaces: []byte(`"x"`)}, nil); err != nil || ws.include != nil {
		t.Errorf("scalar workspaces = %+v, %v; want ignored", ws, err)
	}
}

// stubs creates a PATH directory with shell scripts named tools; each records its name, argv,
// the YARN_ env and the files of its working directory in log, creates the lockfile STUB_CREATE
// (with "created") when it is missing and appends "modified" to the lockfile of its working
// directory unless STUB_KEEP is set; STUB_FAIL makes it fail.
func stubs(t *testing.T, tools ...string) (log string) {
	t.Helper()
	find, err := exec.LookPath("find")
	if err != nil {
		t.Skip("find not in PATH")
	}
	sort, err := exec.LookPath("sort")
	if err != nil {
		t.Skip("sort not in PATH")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
{
  printf 'argv:'
  printf ' %s' "${0##*/}" "$@"
  printf '\nenv: scripts=%s immutable=%s\n' "$YARN_ENABLE_SCRIPTS" "$YARN_ENABLE_IMMUTABLE_INSTALLS"
  '` + find + `' . -type f | '` + sort + `' | while read -r f; do printf 'file: %s\n' "$f"; done
} >> '` + log + `'
if [ -n "$STUB_FAIL" ]; then echo "progress" >&2; echo "ERR something broke"; exit 3; fi
[ -n "$STUB_CREATE" ] && [ ! -f "$STUB_CREATE" ] && echo created > "$STUB_CREATE"
[ -n "$STUB_KEEP" ] && exit 0
for f in package-lock.json pnpm-lock.yaml yarn.lock bun.lock; do
  [ -f "$f" ] && echo modified >> "$f"
done
exit 0
`
	for _, tool := range tools {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_KEEP", "")
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_CREATE", "")
	return log
}

func readLog(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProcessCommands(t *testing.T) {
	const rootPkg = `{"name":"ws","private":true,"workspaces":["packages/*"]}`
	for _, tt := range []struct {
		name      string
		tools     []string
		files     map[string][]byte // siblings
		disk      map[string]string // working tree besides the root package.json and members
		rootPkg   string
		lock      string
		wantArgv  string
		wantEnv   string
		wantFiles []string // besides package.json and the members
	}{
		{
			name: "npm", tools: []string{"npm", "pnpm", "yarn"},
			files:     map[string][]byte{PackageLock: []byte("{}\n"), npmrc: []byte("x=y\n")},
			lock:      PackageLock,
			wantArgv:  "npm install --package-lock-only --ignore-scripts --no-audit --no-fund",
			wantEnv:   "scripts= immutable=",
			wantFiles: []string{".npmrc", PackageLock},
		},
		{
			name: "pnpm", tools: []string{"npm", "pnpm", "yarn"},
			files:     map[string][]byte{PnpmLock: []byte("lockfileVersion: '9.0'\n"), pnpmWorkspace: []byte("packages: [packages/*]\n")},
			rootPkg:   `{"name":"ws","private":true}`,
			lock:      PnpmLock,
			wantArgv:  "pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile",
			wantEnv:   "scripts= immutable=",
			wantFiles: []string{PnpmLock, pnpmWorkspace},
		},
		{
			name: "packageManager picks among lockfiles", tools: []string{"npm", "pnpm"},
			files:     map[string][]byte{PackageLock: []byte("{}\n"), PnpmLock: []byte("lockfileVersion: '9.0'\n")},
			rootPkg:   `{"name":"ws","packageManager":"pnpm@9.1.0","workspaces":["packages/*"]}`,
			lock:      PnpmLock,
			wantArgv:  "pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile",
			wantEnv:   "scripts= immutable=",
			wantFiles: []string{PnpmLock},
		},
		{
			name: "yarn berry (.yarnrc.yml)", tools: []string{"yarn"},
			files: map[string][]byte{YarnLock: []byte("# yarn\n"), yarnrc: []byte(
				"yarnPath: .yarn/releases/yarn-4.5.0.cjs\nplugins:\n  - path: .yarn/plugins/a.cjs\n    spec: a\n  - .yarn/plugins/b.cjs\n  - /abs/c.cjs\n")},
			disk: map[string]string{
				".yarn/releases/yarn-4.5.0.cjs": "", ".yarn/plugins/a.cjs": "", ".yarn/plugins/b.cjs": "", ".yarn/cache/x.zip": "",
			},
			lock:      YarnLock,
			wantArgv:  "yarn install --mode=update-lockfile",
			wantEnv:   "scripts=0 immutable=0",
			wantFiles: []string{".yarn/plugins/a.cjs", ".yarn/plugins/b.cjs", ".yarn/releases/yarn-4.5.0.cjs", yarnrc, YarnLock},
		},
		{
			name: "yarn berry (__metadata)", tools: []string{"yarn"},
			files:     map[string][]byte{YarnLock: []byte("__metadata:\n  version: 8\n")},
			lock:      YarnLock,
			wantArgv:  "yarn install --mode=update-lockfile",
			wantEnv:   "scripts=0 immutable=0",
			wantFiles: []string{YarnLock},
		},
		{
			name: "yarn classic", tools: []string{"yarn"},
			files:     map[string][]byte{YarnLock: []byte("# yarn lockfile v1\n")},
			lock:      YarnLock,
			wantArgv:  "yarn install --ignore-scripts --ignore-engines --non-interactive --no-progress",
			wantEnv:   "scripts= immutable=",
			wantFiles: []string{YarnLock},
		},
		{
			name: "yarn berry local files", tools: []string{"yarn"},
			files: map[string][]byte{yarnrc: []byte("nodeLinker: node-modules\n"), YarnLock: []byte(`__metadata:
  version: 8

"tiny@patch:tiny@npm%3A1.0.0#./patches/b.patch::locator=b%40workspace%3Apackages%2Fb":
  resolution: "tiny@patch:tiny@npm%3A1.0.0#./patches/b.patch::version=1.0.0&hash=abc&locator=b%40workspace%3Apackages%2Fb"
"dep@patch:dep@npm%3A1.0.0#./x.patch::locator=other%40npm%3A1.0.0":
"ts@patch:typescript@npm%3A5.0.0#optional!builtin<compat/typescript>":
  dependencies:
    yaml-diff-patch: "npm:^2.0.0#x.patch"
`)},
			rootPkg: `{"name":"ws","private":true,"workspaces":["packages/*"],` +
				`"resolutions":{"a@npm:^1":"patch:a@npm%3A1.0.0#~/.yarn/patches/a.patch&optional!./.yarn/patches/a%2B2.patch","e@1":"patch:e@npm%3A1#~/../escape.patch"},` +
				`"devDependencies":{"l":"portal:./tools/l","m":"link:./missing"}}`,
			disk: map[string]string{
				".yarn/patches/a.patch": "", ".yarn/patches/a+2.patch": "", ".yarn/patches/unused.patch": "",
				"packages/b/patches/b.patch": "", "x.patch": "",
				"tools/l/package.json": `{"dependencies":{"t":"file:./t.tgz"}}`, "tools/l/t.tgz": "", "tools/l/index.js": "",
			},
			lock:     YarnLock,
			wantArgv: "yarn install --mode=update-lockfile",
			wantEnv:  "scripts=0 immutable=0",
			wantFiles: []string{".yarn/patches/a.patch", ".yarn/patches/a+2.patch", "packages/b/patches/b.patch",
				"tools/l/package.json", "tools/l/t.tgz", yarnrc, YarnLock},
		},
		{
			name: "pnpm local files", tools: []string{"pnpm"},
			files: map[string][]byte{PnpmLock: []byte("lockfileVersion: '9.0'\n"), pnpmWorkspace: []byte(
				"packages: [packages/*]\npatchedDependencies:\n  a@1.0.0: patches/a.patch\noverrides:\n  o: file:./vendor/o.tgz\n  p: 1.0.0\n")},
			rootPkg: `{"name":"ws","private":true,"dependencies":{"r":"file:../outside"},` +
				`"pnpm":{"patchedDependencies":{"b@1.0.0":"patches/b.patch"},"overrides":{"q":"link:./vendor/q"}}}`,
			disk: map[string]string{
				"patches/a.patch": "", "patches/b.patch": "", "patches/unused.patch": "", "vendor/o.tgz": "",
				"vendor/q/package.json": "{}", "vendor/q/index.js": "", ".pnpmfile.cjs": "",
			},
			lock:     PnpmLock,
			wantArgv: "pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile",
			wantEnv:  "scripts= immutable=",
			wantFiles: []string{".pnpmfile.cjs", "patches/a.patch", "patches/b.patch", "vendor/o.tgz", "vendor/q/package.json",
				PnpmLock, pnpmWorkspace},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			log := stubs(t, tt.tools...)
			dir := t.TempDir()
			disk := map[string]string{
				"packages/a/package.json":              `{"name":"a"}`,
				"packages/b/package.json":              `{"name":"b"}`,
				"packages/a/node_modules/package.json": "{}",
			}
			maps.Copy(disk, tt.disk)
			root := rootPkg
			if tt.rootPkg != "" {
				root = tt.rootPkg
			}
			disk["package.json"] = root
			write(t, dir, disk)

			patched := []byte(`{"name":"a","patched":true}`)
			out, err := Locker{}.Process(t.Context(), dir, pkgs("packages/a/package.json", patched), tt.files)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 || string(out[tt.lock]) != string(tt.files[tt.lock])+"modified\n" {
				t.Errorf("out = %q, want only %s modified", out, tt.lock)
			}
			got := readLog(t, log)
			lines := strings.Split(strings.TrimSpace(got), "\n")
			if lines[0] != "argv: "+tt.wantArgv || lines[1] != "env: "+tt.wantEnv {
				t.Errorf("command:\n%s\nwant argv: %s\nenv: %s", got, tt.wantArgv, tt.wantEnv)
			}
			want := append([]string{"package.json", "packages/a/package.json", "packages/b/package.json"}, tt.wantFiles...)
			slices.Sort(want)
			var files []string
			for _, l := range lines[2:] {
				files = append(files, strings.TrimPrefix(l, "file: ./"))
			}
			slices.Sort(files)
			if !slices.Equal(files, want) {
				t.Errorf("copied files = %v, want %v", files, want)
			}
			if entries, _ := os.ReadDir(filepath.Join(dir, "packages", "a")); len(entries) != 2 {
				t.Errorf("working tree touched: %v", entries)
			}
		})
	}
}

func TestProcessCopiesPatchedManifest(t *testing.T) {
	// The patched manifests replace the files on disk in the temporary copy: the stub fails
	// with their content.
	bin := t.TempDir()
	script := "#!/bin/sh\nread -r l < packages/a/package.json; echo \"$l\"\nread -r l < package.json; echo \"$l\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir := t.TempDir()
	write(t, dir, map[string]string{"package.json": "on disk", "packages/a/package.json": "old"})
	_, err := Locker{}.Process(t.Context(), dir, map[string][]byte{"package.json": []byte(`{"workspaces":["packages/*"]}`), "packages/a/package.json": []byte("patched")},
		map[string][]byte{PackageLock: []byte("{}")})
	if err == nil || err.Error() != "npm install in "+dir+": patched\n{\"workspaces\":[\"packages/*\"]}" {
		t.Errorf("err = %v", err)
	}
}

func TestToolResolution(t *testing.T) {
	berryFiles := map[string][]byte{YarnLock: []byte("__metadata:\n")}
	pnpmFiles := map[string][]byte{PnpmLock: []byte("lockfileVersion: '9.0'\n")}
	for _, tt := range []struct {
		name     string
		tools    []string
		pm       string
		files    map[string][]byte
		wantArgv string
		wantErr  string
	}{
		{"PATH first", []string{"pnpm", "npx"}, "pnpm@9.1.0", pnpmFiles, "pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile", ""},
		{"npx with packageManager version", []string{"npx"}, "pnpm@9.1.0+sha512.abc", pnpmFiles, "npx --yes pnpm@9.1.0 install --lockfile-only --ignore-scripts --no-frozen-lockfile", ""},
		{"npx without version", []string{"npx"}, "yarn@4.5.0", pnpmFiles, "npx --yes pnpm install --lockfile-only --ignore-scripts --no-frozen-lockfile", ""},
		{"npx yarn berry", []string{"npx"}, "yarn@4.5.0", berryFiles, "npx --yes --package=@yarnpkg/cli-dist@4.5.0 -- yarn install --mode=update-lockfile", ""},
		{"npx yarn berry latest", []string{"npx"}, "", berryFiles, "npx --yes --package=@yarnpkg/cli-dist -- yarn install --mode=update-lockfile", ""},
		{"not found", nil, "pnpm@9.1.0", pnpmFiles, "", "pnpm not found (needed to update pnpm-lock.yaml)"},
		{"npm not found", nil, "", map[string][]byte{PackageLock: []byte("{}")}, "", "npm not found (needed to update package-lock.json)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			log := stubs(t, tt.tools...)
			dir := t.TempDir()
			pkg := []byte(`{"name":"x","packageManager":"` + tt.pm + `"}`)
			_, err := Locker{}.Process(t.Context(), dir, pkgs("package.json", pkg), tt.files)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("err = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, _, _ := strings.Cut(readLog(t, log), "\n"); got != "argv: "+tt.wantArgv {
				t.Errorf("%s, want argv: %s", got, tt.wantArgv)
			}
		})
	}
}

func TestProcessEdgeCases(t *testing.T) {
	stubs(t, "npm")
	dir := t.TempDir()
	lockOnly := map[string][]byte{PackageLock: []byte("{}\n")}

	// No lockfile: nothing to do, the tool is not run.
	out, err := Locker{}.Process(t.Context(), dir, pkgs("package.json", manifest("1.0.0")), map[string][]byte{npmrc: []byte("x=y\n")})
	if err != nil || out != nil {
		t.Errorf("no lockfile: out = %v, err = %v", out, err)
	}
	// A member manifest without the root package.json on disk.
	if _, err := (Locker{}).Process(t.Context(), dir, pkgs("a/package.json", manifest("1.0.0")), lockOnly); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing root package.json: err = %v", err)
	}
	// Unchanged lockfile.
	t.Setenv("STUB_KEEP", "1")
	if out, err := (Locker{}).Process(t.Context(), dir, pkgs("package.json", manifest("1.0.0")), lockOnly); err != nil || out != nil {
		t.Errorf("unchanged: out = %v, err = %v", out, err)
	}
	// Tool failure: the tail of stdout and stderr.
	t.Setenv("STUB_FAIL", "1")
	_, err = Locker{}.Process(t.Context(), dir, pkgs("package.json", manifest("1.0.0")), lockOnly)
	if err == nil || err.Error() != "npm install in "+dir+": progress\nERR something broke" {
		t.Errorf("failure: err = %v", err)
	}
	t.Setenv("STUB_FAIL", "")
	// Broken workspace configuration.
	if _, err := (Locker{}).Process(t.Context(), dir, pkgs("package.json", []byte(`{"workspaces":{"packages":1}}`)), lockOnly); err == nil || !strings.HasPrefix(err.Error(), "workspaces: ") {
		t.Errorf("bad workspaces: err = %v", err)
	}
	if _, err := (Locker{}).Process(t.Context(), dir, pkgs("package.json", manifest("1.0.0")), map[string][]byte{YarnLock: {}, yarnrc: []byte("plugins: {")}); err == nil || !strings.HasPrefix(err.Error(), ".yarnrc.yml: ") {
		t.Errorf("bad .yarnrc.yml: err = %v", err)
	}
	if _, err := (Locker{}).Process(t.Context(), dir, pkgs("package.json", manifest("1.0.0")), map[string][]byte{YarnLock: {}, yarnrc: []byte("yarnPath: .yarn/missing.cjs\n")}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing yarnPath: err = %v", err)
	}
	// A member listed by the workspaces glob that cannot be read.
	write(t, dir, map[string]string{"m/package.json/x": ""})
	if _, err := (Locker{}).Process(t.Context(), dir, pkgs("package.json", []byte(`{"workspaces":["m"]}`)), lockOnly); err != nil {
		t.Errorf("directory named package.json: err = %v, want it ignored", err)
	}
	// Canceled context.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Locker{}).Process(ctx, dir, pkgs("package.json", manifest("1.0.0")), lockOnly); err == nil {
		t.Error("canceled: no error")
	}
	if got := (Locker{}).Siblings(); strings.Join(got, ",") != "npm-shrinkwrap.json,package-lock.json,pnpm-lock.yaml,yarn.lock,bun.lock,.npmrc,pnpm-workspace.yaml,.yarnrc.yml,bunfig.toml" {
		t.Errorf("Siblings = %v", got)
	}
}

func TestYarnFiles(t *testing.T) {
	got, err := yarnFiles([]byte("yarnPath: ./.yarn/releases/y.cjs\nplugins:\n  - path: .yarn/plugins/a.cjs\n  - ../outside.cjs\n  - /abs.cjs\n  - [nested]\n"))
	if want := []string{"./.yarn/releases/y.cjs", ".yarn/plugins/a.cjs", "../outside.cjs", "/abs.cjs"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("yarnFiles = %v, %v", got, err)
	}
	if _, err := yarnFiles([]byte("plugins:\n  - path: [x]\n")); err == nil {
		t.Error("bad plugin path: no error")
	}
	if got, err := yarnFiles(nil); err != nil || len(got) != 0 {
		t.Errorf("no .yarnrc.yml: %v, %v", got, err)
	}
}

// TestWorkspaceMembersOutsideRoot drops workspace globs that leave the root: staging them
// would write their package.json outside the temp tree.
func TestWorkspaceMembersOutsideRoot(t *testing.T) {
	top := t.TempDir()
	write(t, top, map[string]string{
		"root/package.json":    `{"workspaces": ["../sib", "../*", "in"]}`,
		"root/in/package.json": "{}",
		"sib/package.json":     "{}",
	})
	dir := filepath.Join(top, "root")
	ws, err := parseWorkspaces(rootManifest{Workspaces: []byte(`["../sib", "../*"]`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ws.members(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("members = %v, want none", got)
	}

	s, err := stage(t.Context(), dir, nil, map[string][]byte{"package-lock.json": []byte("{}")}, false)
	if err != nil {
		t.Fatal(err)
	}
	for p := range s.write {
		if !filepath.IsLocal(filepath.FromSlash(p)) {
			t.Errorf("stage writes %q outside the temp tree", p)
		}
	}
	if _, ok := s.write["in/package.json"]; !ok {
		t.Errorf("stage write = %v, want in/package.json", slices.Collect(maps.Keys(s.write)))
	}
}
