package nodetool

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestManifestRefs(t *testing.T) {
	got, err := manifestRefs([]byte(`{
		"dependencies": {"a": "patch:a@npm%3A1.0.0#./p/a.patch", "b": "^1.0.0", "c": "file:./c.tgz", "d": 1},
		"devDependencies": {"e": "../e"},
		"overrides": {"f": {"g": "link:./g", ".": "1.0.0"}, "h": "portal:./h"},
		"pnpm": {"overrides": {"i": "file:./i"}, "patchedDependencies": {"j@1.0.0": "patches/j.patch"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a@npm%3A1.0.0#./p/a.patch"}; !slices.Equal(got.patches, want) {
		t.Errorf("patches = %v, want %v", got.patches, want)
	}
	if want := []string{"./c.tgz", "../e", "./i", "./g", "./h"}; !slices.Equal(got.targets, want) {
		t.Errorf("targets = %v, want %v", got.targets, want)
	}
	if want := []string{"patches/j.patch"}; !slices.Equal(got.pnpmPatches, want) {
		t.Errorf("pnpmPatches = %v, want %v", got.pnpmPatches, want)
	}
	if got, err := manifestRefs([]byte(`{"dependencies": {"a": "^1.0.0"}} broken`)); err != nil || got.targets != nil {
		t.Errorf("prefiltered: %v, %v", got, err)
	}
}

func TestYarnPatchPaths(t *testing.T) {
	for _, tt := range []struct {
		rng, base string
		want      []string
	}{
		{"a@npm%3A1.0.0#~/.yarn/patches/a.patch", "packages/x", []string{".yarn/patches/a.patch"}},
		{"a@npm%3A1.0.0#./a.patch&../b%2Bc.patch::locator=x", "packages/x", []string{"packages/x/a.patch", "packages/b+c.patch"}},
		{"a@npm%3A1.0.0#optional!builtin<compat/a>", ".", nil},
		{"a@npm%3A1.0.0#~/../a.patch&/abs.patch", ".", []string{"../a.patch", ""}},
		{"a@npm%3A1.0.0", ".", nil},
	} {
		if got := yarnPatchPaths(tt.rng, tt.base); !slices.Equal(got, tt.want) {
			t.Errorf("yarnPatchPaths(%q, %q) = %q, want %q", tt.rng, tt.base, got, tt.want)
		}
	}
}

func TestNpmrcValue(t *testing.T) {
	if got := npmrcValue([]byte("a=b\npnpmfile = \"x/hooks.cjs\"\n"), "pnpmfile"); got != "x/hooks.cjs" {
		t.Errorf("npmrcValue = %q", got)
	}
}

func TestTreeLocal(t *testing.T) {
	top := t.TempDir()
	write(t, top, map[string]string{".git/HEAD": "", "ws/ui/package.json": "{}"})
	tr, err := openTree(filepath.Join(top, "ws", "ui"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.root.Close()
	if tr.top != top || tr.prefix != "ws/ui" {
		t.Errorf("tree = %q %q", tr.top, tr.prefix)
	}
	for p, want := range map[string]string{
		"a/../b.patch": "b.patch", "../../.yarn/x.cjs": "../../.yarn/x.cjs", "../../../x": "", "/abs": "", ".": "", "": "",
	} {
		if got := tr.local(p); got != want {
			t.Errorf("local(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestAddLocalFilesSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.patch")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, map[string]string{".git/HEAD": "", "patches/ok.patch": "ok"})
	if err := os.Symlink(outside, filepath.Join(dir, "patches", "evil.patch")); err != nil {
		t.Fatal(err)
	}
	tr, err := openTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.root.Close()
	files := map[string][]byte{pnpmWorkspace: []byte("patchedDependencies:\n  a@1: patches/evil.patch\n  b@1: patches/ok.patch\n  c@1: ../" + filepath.Base(filepath.Dir(outside)) + "/secret.patch\n")}
	w := map[string][]byte{"package.json": []byte("{}")}
	addLocalFiles(tr, w, files, PnpmLock)
	if len(w) != 2 || string(w["patches/ok.patch"]) != "ok" {
		t.Errorf("write = %q", w)
	}
}

// TestProcessLockRootBelowTop copies the files a lock root below the repository top
// references above it at their relative paths.
func TestProcessLockRootBelowTop(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nread -r a < ../../.yarn/releases/y.cjs; read -r b < ../../.yarn/patches/p.patch; echo \"$a$b\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "yarn"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	top := t.TempDir()
	write(t, top, map[string]string{".git/HEAD": "", ".yarn/releases/y.cjs": "release", ".yarn/patches/p.patch": "patch"})
	dir := filepath.Join(top, "ws", "ui")
	pkg := []byte(`{"dependencies":{"a":"patch:a@npm%3A1.0.0#../../.yarn/patches/p.patch"}}`)
	_, err := Locker{}.Process(t.Context(), dir, pkgs("package.json", pkg),
		map[string][]byte{YarnLock: {}, yarnrc: []byte("yarnPath: ../../.yarn/releases/y.cjs\n")})
	if err == nil || err.Error() != "yarn install in "+dir+": releasepatch" {
		t.Errorf("err = %v", err)
	}
}

// tinyPatch adds a file to the tiny package.
const tinyPatch = `diff --git a/patched.txt b/patched.txt
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/patched.txt
@@ -0,0 +1 @@
+patched
`

// TestProcessRealPnpmPatch locks a pnpm patchedDependencies entry whose patch file is only on
// disk, with a pnpmfile, with the real pnpm against a local registry.
func TestProcessRealPnpmPatch(t *testing.T) {
	isolate(t)
	realTool(t, "pnpm", "pnpm@10")
	srv := registry(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"patches/tiny.patch": tinyPatch,
		".pnpmfile.cjs":      "module.exports = { hooks: { readPackage: (pkg) => pkg } }\n",
	})
	files := map[string][]byte{
		PnpmLock:      []byte("lockfileVersion: '9.0'\n"),
		npmrc:         []byte("registry=" + srv.URL + "/\n"),
		pnpmWorkspace: []byte("patchedDependencies:\n  tiny@1.0.0: patches/tiny.patch\n"),
	}
	out, err := Locker{}.Process(t.Context(), dir, pkgs("package.json", manifest("1.0.0")), files)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out[PnpmLock]); !strings.Contains(got, "patchedDependencies") || !strings.Contains(got, "tiny@1.0.0(patch_hash=") ||
		!strings.Contains(got, "pnpmfileChecksum") {
		t.Errorf("lock = %s", got)
	}
}

// TestProcessRealYarnBerryPatch locks a yarn berry `patch:` dependency whose patch file is only
// on disk with the real yarn (berry) against a local registry.
func TestProcessRealYarnBerryPatch(t *testing.T) {
	isolate(t)
	realTool(t, "yarn", "@yarnpkg/cli-dist@4")
	srv := registry(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{".yarn/patches/tiny.patch": tinyPatch})
	files := map[string][]byte{
		YarnLock: {},
		// The local registry has no publish times, which yarn's npmMinimalAgeGate rejects.
		yarnrc: []byte("npmRegistryServer: \"" + srv.URL + "\"\nunsafeHttpWhitelist: [\"127.0.0.1\"]\nenableTelemetry: false\nnpmMinimalAgeGate: 0\n"),
	}
	out, err := Locker{}.Process(t.Context(), dir, pkgs("package.json", manifest("patch:tiny@npm%3A1.0.0#~/.yarn/patches/tiny.patch")), files)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out[YarnLock]); !strings.Contains(got, `"tiny@patch:tiny@npm%3A1.0.0#~/.yarn/patches/tiny.patch":`) {
		t.Errorf("lock = %s", got)
	}
}

// TestLockerScopeInputs lists the files Process copies: the member package.json files and
// the local files the configuration references, also above the lock root, but not sources.
func TestLockerScopeInputs(t *testing.T) {
	top := t.TempDir()
	write(t, top, map[string]string{
		".git/HEAD":                     "",
		".yarn/releases/y.cjs":          "release",
		"ws/ui/.yarnrc.yml":             "yarnPath: ../../.yarn/releases/y.cjs\n",
		"ws/ui/yarn.lock":               "__metadata:\n",
		"ws/ui/package.json":            `{"workspaces": ["packages/*"], "dependencies": {"a": "patch:a@npm%3A1.0.0#~/patches/a.patch", "t": "file:./vendor/t.tgz"}}`,
		"ws/ui/patches/a.patch":         "patch",
		"ws/ui/vendor/t.tgz":            "tgz",
		"ws/ui/packages/b/package.json": `{"dependencies": {"l": "link:../../local"}}`,
		"ws/ui/local/package.json":      "{}",
		"ws/ui/local/index.js":          "",
		"ws/ui/src/index.js":            "",
	})
	paths, digests, err := Locker{}.ScopeInputs(top, "ws/ui", []string{"package.json"})
	slices.Sort(paths)
	want := []string{".yarn/releases/y.cjs", "ws/ui/.yarnrc.yml", "ws/ui/local/package.json", "ws/ui/package.json", "ws/ui/packages/b/package.json", "ws/ui/patches/a.patch", "ws/ui/vendor/t.tgz", "ws/ui/yarn.lock"}
	if err != nil || digests != nil || !slices.Equal(paths, want) {
		t.Errorf("ScopeInputs = %q, %q, %v; want %q", paths, digests, err, want)
	}
	write(t, top, map[string]string{"plain/package.json": "{}"})
	if paths, _, err := (Locker{}).ScopeInputs(top, "plain", nil); err != nil || paths != nil {
		t.Errorf("without lockfile: %q, %v", paths, err)
	}
}
