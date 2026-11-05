package gotool

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// vendorEnv serves example.com/m v1.0.0 (a.go, old.go, same.go) and v1.1.0 (a.go changed,
// b.go new, old.go gone, same.go unchanged) from a file:// proxy.
func vendorEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	proxy := t.TempDir()
	same := "package m\n\nconst S = 0\n"
	publish(t, proxy, "example.com/m", "v1.0.0", "module example.com/m\n", map[string]string{
		"a.go": "package m\n\nconst V = 1\n", "old.go": "package m\n\nconst O = 1\n", "same.go": same,
	})
	publish(t, proxy, "example.com/m", "v1.1.0", "module example.com/m\n", map[string]string{
		"a.go": "package m\n\nconst V = 2\n", "b.go": "package m\n\nconst B = 2\n", "same.go": same,
	})
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOMODCACHE", t.TempDir())
	// Tidy clears GOFLAGS, so the cache is read-only; clean it before TempDir removes it.
	t.Cleanup(func() { _ = exec.Command("go", "clean", "-modcache").Run() })
}

// goIn runs the go command in dir with GOFLAGS cleared and the given extra environment.
func goIn(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"GOFLAGS="}, env...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

const vendorMain = "package main\n\nimport \"example.com/m\"\n\nfunc main() { _ = m.V }\n"

// wantVendorUpdate checks the vendor results of moving example.com/m to v1.1.0 under prefix.
func wantVendorUpdate(t *testing.T, out map[string][]byte, prefix string) {
	t.Helper()
	var keys []string
	for k := range maps.Keys(out) {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	want := []string{prefix + "example.com/m/a.go", prefix + "example.com/m/b.go", prefix + "example.com/m/old.go", prefix + "modules.txt"}
	if !slices.Equal(keys, want) {
		t.Fatalf("vendor results = %q, want %q", keys, want)
	}
	if !strings.Contains(string(out[prefix+"modules.txt"]), "# example.com/m v1.1.0") {
		t.Errorf("modules.txt:\n%s", out[prefix+"modules.txt"])
	}
	if string(out[prefix+"example.com/m/a.go"]) != "package m\n\nconst V = 2\n" || out[prefix+"example.com/m/old.go"] != nil {
		t.Errorf("a.go %q, old.go %q", out[prefix+"example.com/m/a.go"], out[prefix+"example.com/m/old.go"])
	}
}

// TestProcessVendored updates a requirement of a module with vendor/modules.txt: the result
// holds the vendor files that changed, appeared or disappeared, and the real tree stays.
func TestProcessVendored(t *testing.T) {
	vendorEnv(t)
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod":  "module example.com/x\n\ngo 1.21\n\nrequire example.com/m v1.0.0\n",
		"main.go": vendorMain,
	})
	goIn(t, dir, []string{"GOWORK=off"}, "mod", "tidy")
	goIn(t, dir, []string{"GOWORK=off"}, "mod", "vendor")
	sum, err := os.ReadFile(filepath.Join(dir, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	tidier := Tidier{}
	if got := tidier.Trees(dir); !slices.Equal(got, []string{"vendor"}) {
		t.Errorf("Trees = %q", got)
	}
	if got := tidier.ScopeSiblings(dir); !slices.Equal(got, []string{"go.sum"}) {
		t.Errorf("ScopeSiblings = %q", got)
	}
	if paths, _, err := tidier.ScopeInputs(t.Context(), dir, ".", nil); err != nil || !slices.Contains(paths, "vendor") {
		t.Errorf("ScopeInputs = %q, %v", paths, err)
	}
	mod := []byte("module example.com/x\n\ngo 1.21\n\nrequire example.com/m v1.1.0\n")
	out, err := tidier.Process(t.Context(), dir, map[string][]byte{"go.mod": mod}, map[string][]byte{"go.sum": sum})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out["go.mod"]), "example.com/m v1.1.0") || !strings.Contains(string(out["go.sum"]), "example.com/m v1.1.0 h1:") {
		t.Errorf("go.mod %s\ngo.sum %s", out["go.mod"], out["go.sum"])
	}
	wantVendorUpdate(t, out, "vendor/")
	if b, _ := os.ReadFile(filepath.Join(dir, "vendor", "modules.txt")); !strings.Contains(string(b), "example.com/m v1.0.0") {
		t.Errorf("real modules.txt changed:\n%s", b)
	}

	// A go failure is the module's error.
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package main\n\nimport _ \"example.com/missing\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tidier.Process(t.Context(), dir, map[string][]byte{"go.mod": mod}, map[string][]byte{"go.sum": sum}); err == nil {
		t.Error("want error for an unresolvable import")
	}
}

// TestProcessWorkspaceVendored updates a module of a workspace with vendor/modules.txt next
// to go.work: the workspace is the scope, and `go work vendor` regenerates its vendor tree.
func TestProcessWorkspaceVendored(t *testing.T) {
	vendorEnv(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.work":   "go 1.21\n\nuse (\n\t./a\n\t./b\n)\n",
		"a/go.mod":  "module example.com/a\n\ngo 1.21\n\nrequire example.com/m v1.0.0\n",
		"a/main.go": vendorMain,
		"b/go.mod":  "module example.com/b\n\ngo 1.21\n",
		"b/b.go":    "package b\n",
	})
	goIn(t, filepath.Join(root, "a"), []string{"GOWORK=off"}, "mod", "tidy")
	goIn(t, root, nil, "work", "vendor")
	sum, err := os.ReadFile(filepath.Join(root, "a", "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	tidier := Tidier{}
	for manifest, want := range map[string]string{"a/go.mod": ".", "a/main.go": ".", "b/go.mod": "."} {
		if got, ok := tidier.Scope(t.Context(), root, manifest); got != want || !ok {
			t.Errorf("Scope(%s) = %q, %v", manifest, got, ok)
		}
	}
	if _, ok := tidier.Scope(t.Context(), root, "go.work"); ok {
		t.Error("go.work has a scope")
	}
	if got := tidier.ScopeSiblings(root); !slices.Equal(got, []string{"a/go.sum", "b/go.sum"}) {
		t.Errorf("ScopeSiblings = %q", got)
	}
	if got := tidier.Trees(root); !slices.Equal(got, []string{"vendor"}) {
		t.Errorf("Trees = %q", got)
	}
	paths, digests, err := tidier.ScopeInputs(t.Context(), root, ".", nil)
	if err != nil || !slices.Equal(paths, []string{"go.work", "vendor", "a/go.mod", "b/go.mod"}) || len(digests) != 2 {
		t.Errorf("ScopeInputs = %q, %q, %v", paths, digests, err)
	}
	mod := []byte("module example.com/a\n\ngo 1.21\n\nrequire example.com/m v1.1.0\n")
	out, err := tidier.Process(t.Context(), root, map[string][]byte{"a/go.mod": mod, "a/main.go": []byte(vendorMain)}, map[string][]byte{"a/go.sum": sum})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out["a/go.mod"]), "example.com/m v1.1.0") || !strings.Contains(string(out["a/go.sum"]), "example.com/m v1.1.0 h1:") {
		t.Errorf("a/go.mod %s\na/go.sum %s", out["a/go.mod"], out["a/go.sum"])
	}
	wantVendorUpdate(t, out, "vendor/")

	if _, err := tidier.Process(t.Context(), root, map[string][]byte{"a/main.go": nil}, nil); err == nil {
		t.Error("want error without a patched go.mod")
	}
	if _, err := tidier.Process(t.Context(), root, map[string][]byte{"a/go.mod": mod, "b/b.go": nil}, nil); err == nil {
		t.Error("want error for a source file of an unpatched module")
	}
}

func TestDiffTree(t *testing.T) {
	next := t.TempDir()
	writeTree(t, next, map[string]string{"a/x.go": "x"})
	out := map[string][]byte{}
	if err := diffTree(filepath.Join(t.TempDir(), "missing"), next, "vendor", out); err != nil || string(out["vendor/a/x.go"]) != "x" || len(out) != 1 {
		t.Errorf("diffTree into a missing tree = %q, %v", out, err)
	}
}
