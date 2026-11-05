package nodetool

import (
	"os"
	"os/exec"
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
		{"a@npm%3A1.0.0#~/../a.patch&/abs.patch", ".", []string{"", ""}},
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

func TestAddLocalFilesSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.patch")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, map[string]string{"patches/ok.patch": "ok"})
	if err := os.Symlink(outside, filepath.Join(dir, "patches", "evil.patch")); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{pnpmWorkspace: []byte("patchedDependencies:\n  a@1: patches/evil.patch\n  b@1: patches/ok.patch\n")}
	w := map[string][]byte{"package.json": []byte("{}")}
	if err := addLocalFiles(dir, w, files, PnpmLock); err != nil {
		t.Fatal(err)
	}
	if _, ok := w["patches/evil.patch"]; ok || string(w["patches/ok.patch"]) != "ok" {
		t.Errorf("write = %q", w)
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
	if v, err := exec.CommandContext(t.Context(), "yarn", "--version").Output(); err != nil || strings.HasPrefix(string(v), "1.") {
		t.Skip("yarn on PATH is not yarn berry")
	}
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
