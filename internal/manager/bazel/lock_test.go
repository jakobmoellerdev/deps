package bazel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeBazel puts a `bazel` script on PATH that logs its arguments and, for `mod deps`, writes
// MODULE.bazel.lock from the module files it sees (failing when FAIL is in MODULE.bazel).
func fakeBazel(t *testing.T, name string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	script := `#!/bin/sh
echo "$@" >> ` + log + `
case "$2" in shutdown) exit 0;; esac
if grep -q FAIL MODULE.bazel; then echo "ERROR: $PWD/MODULE.bazel: boom" >&2; exit 1; fi
if [ -f MODULE.bazel.lock ]; then kept=kept; else kept=; fi
{ cat MODULE.bazel; [ -f deps/a.MODULE.bazel ] && cat deps/a.MODULE.bazel; [ -n "$kept" ] && echo kept; } > out.tmp
rm -f MODULE.bazel.lock; mv out.tmp MODULE.bazel.lock
`
	writeFile(t, filepath.Join(bin, name), script)
	if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return log
}

func workspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ws", "MODULE.bazel"), "bazel_dep(name = \"a\", version = \"1\")\n")
	writeFile(t, filepath.Join(root, "ws", "deps", "a.MODULE.bazel"), "bazel_dep(name = \"b\", version = \"1\")\n")
	writeFile(t, filepath.Join(root, "ws", "MODULE.bazel.lock"), "old\n")
	writeFile(t, filepath.Join(root, "nolock", "MODULE.bazel"), "")
	if err := os.Symlink(filepath.Join(root, "ws"), filepath.Join(root, "ws", "bazel-ws")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLockerScopeAndRoots(t *testing.T) {
	root := workspace(t)
	l := Locker{}
	if !slices.Equal(l.Siblings(), []string{"MODULE.bazel.lock"}) {
		t.Error("Siblings")
	}
	for manifest, want := range map[string]string{"ws/MODULE.bazel": "ws", "ws/deps/a.MODULE.bazel": "ws", "nolock/MODULE.bazel": "", "stray/x.MODULE.bazel": ""} {
		dir, ok := l.Scope(t.Context(), root, manifest)
		if ok != (want != "") || ok && dir != want {
			t.Errorf("Scope(%s) = %q, %v", manifest, dir, ok)
		}
	}
	roots := New().(manager.LockMaintainer).LockRoots(root, []string{"ws/deps/a.MODULE.bazel", "nolock/MODULE.bazel", "ws/MODULE.bazel"})
	if want := []manager.LockRoot{{Manifest: "ws/MODULE.bazel", Lockfile: "ws/MODULE.bazel.lock"}}; !slices.Equal(roots, want) {
		t.Errorf("LockRoots = %v", roots)
	}
}

func TestLockerProcess(t *testing.T) {
	log := fakeBazel(t, "bazel")
	root := workspace(t)
	dir := filepath.Join(root, "ws")
	l := Locker{}
	manifests := map[string][]byte{"MODULE.bazel": []byte("bazel_dep(name = \"a\", version = \"2\")\n"), "deps/a.MODULE.bazel": []byte("bazel_dep(name = \"b\", version = \"2\")\n")}
	out, err := l.Process(t.Context(), dir, manifests, map[string][]byte{"MODULE.bazel.lock": []byte("old\n")})
	want := "bazel_dep(name = \"a\", version = \"2\")\nbazel_dep(name = \"b\", version = \"2\")\nkept\n"
	if err != nil || string(out["MODULE.bazel.lock"]) != want {
		t.Fatalf("Process = %q, %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "deps", "a.MODULE.bazel")); !strings.Contains(string(b), `"1"`) {
		t.Errorf("working tree changed: %s", b)
	}
	b, _ := os.ReadFile(log)
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 2 || !strings.HasSuffix(lines[0], " mod deps --lockfile_mode=update") ||
		!strings.HasPrefix(lines[0], "--output_base=") || !strings.HasSuffix(lines[1], " shutdown") {
		t.Errorf("bazel calls = %q", lines)
	}

	// Maintain regenerates without the old lock; an unchanged result is no change.
	out, err = l.Maintain(t.Context(), dir, map[string][]byte{"MODULE.bazel.lock": []byte("old\n")})
	if err != nil || string(out["MODULE.bazel.lock"]) != "bazel_dep(name = \"a\", version = \"1\")\nbazel_dep(name = \"b\", version = \"1\")\n" {
		t.Errorf("Maintain = %q, %v", out, err)
	}
	same := "bazel_dep(name = \"a\", version = \"1\")\nbazel_dep(name = \"b\", version = \"1\")\n"
	if out, err := l.Maintain(t.Context(), dir, map[string][]byte{"MODULE.bazel.lock": []byte(same)}); err != nil || out != nil {
		t.Errorf("unchanged Maintain = %q, %v", out, err)
	}
	for _, f := range []func() (map[string][]byte, error){
		func() (map[string][]byte, error) { return l.Process(t.Context(), dir, manifests, nil) },
		func() (map[string][]byte, error) { return l.Maintain(t.Context(), dir, nil) },
	} {
		if out, err := f(); out != nil || err != nil {
			t.Errorf("without a lockfile = %q, %v", out, err)
		}
	}

	_, err = l.Process(t.Context(), dir, map[string][]byte{"MODULE.bazel": []byte("FAIL\n")}, map[string][]byte{"MODULE.bazel.lock": []byte("old\n")})
	if err == nil || !strings.Contains(err.Error(), "bazel mod deps --lockfile_mode=update in "+dir+": ERROR: "+dir+"/MODULE.bazel: boom") {
		t.Errorf("failing bazel: %v", err)
	}
}

func TestLockerBazeliskAndMissing(t *testing.T) {
	fakeBazel(t, "bazelisk")
	root := workspace(t)
	out, err := Locker{}.Maintain(t.Context(), filepath.Join(root, "ws"), map[string][]byte{"MODULE.bazel.lock": []byte("old\n")})
	if err != nil || out == nil {
		t.Errorf("bazelisk Maintain = %q, %v", out, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Locker{}).Maintain(t.Context(), filepath.Join(root, "ws"), map[string][]byte{"MODULE.bazel.lock": []byte("old\n")}); err == nil || err.Error() != "bazel not found (needed to update MODULE.bazel.lock)" {
		t.Errorf("without bazel: %v", err)
	}
}
