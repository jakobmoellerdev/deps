package paket

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// shellTools are find, sort and cat, resolved before the test replaces PATH with the stub's
// directory.
var shellTools = func() (tools [3]string) {
	for i, name := range []string{"find", "sort", "cat"} {
		tools[i], _ = exec.LookPath(name)
	}
	return tools
}()

// The mirror paket runs in symlinks the entries of the scope whole: no directory of the
// checkout is recreated and no file of it is symlinked one by one, however deep the tree is.
// The scope's files are still there through the symlinks, and paket.lock is read back.
func TestMirrorSymlinksEntries(t *testing.T) {
	find, sort, cat := shellTools[0], shellTools[1], shellTools[2]
	if find == "" || sort == "" || cat == "" {
		t.Skip("find, sort or cat not in PATH")
	}
	dir := t.TempDir()
	for p, c := range map[string]string{
		manifestFile:                      "source https://api.nuget.org/v3/index.json\n\nnuget A 1.0\n",
		lockFile:                          "old\n",
		"src/app/App.fsproj":              "fsproj",
		"src/app/obj/project.assets.json": "{}",
		"src/app/node_modules/x/y.js":     "y",
		"packages/A/A.dll":                "dll",
	} {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\n{ echo mirror; '" + find + "' . -mindepth 1 -type d | '" + sort + "'; echo \"proj=$('" + cat + "' src/app/App.fsproj)\"; } > " + lockFile + "\n"
	if err := os.WriteFile(filepath.Join(bin, "paket"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	got, err := Locker{}.Maintain(t.Context(), dir, map[string][]byte{lockFile: []byte("old\n")})
	if err != nil {
		t.Fatal(err)
	}
	if want := "mirror\nproj=fsproj\n"; string(got[lockFile]) != want {
		t.Errorf("%s = %q, want %q", lockFile, got[lockFile], want)
	}
}
