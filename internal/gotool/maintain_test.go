package gotool

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// TestMaintain serves modules from a file:// GOPROXY, which needs no network:
//   - example.com/i (indirect) moves v1.0.0 → v1.1.0;
//   - example.com/a@v1.1.0 requires the prerelease example.com/b@v1.5.0-pre, above b@upgrade
//     (v1.4.0): go rejects a@upgrade b@upgrade together, one after the other both move;
//   - example.com/c@v1.1.0 would move the direct example.com/d, example.com/g@v1.1.0 the go
//     directive: both stay;
//   - example.com/r has a replace directive and stays;
//   - the direct example.com/d, go and toolchain stay.
func TestMaintain(t *testing.T) {
	needGo(t)
	proxy := t.TempDir()
	pkg := func(name string, imports ...string) map[string]string {
		src := "package " + name + "\n"
		for _, p := range imports {
			src += "\nimport _ \"" + p + "\"\n"
		}
		return map[string]string{"a.go": src}
	}
	gomod := func(path, goVersion string, requires ...string) string {
		mod := "module " + path + "\n\ngo " + goVersion + "\n"
		for _, r := range requires {
			mod += "\nrequire " + r + "\n"
		}
		return mod
	}
	for _, v := range []string{"v1.0.0", "v1.1.0"} {
		publish(t, proxy, "example.com/i", v, gomod("example.com/i", "1.21"), pkg("i"))
		publish(t, proxy, "example.com/r", v, gomod("example.com/r", "1.21"), pkg("r"))
		publish(t, proxy, "example.com/d", v, gomod("example.com/d", "1.21",
			"example.com/a v1.0.0", "example.com/c v1.0.0", "example.com/g v1.0.0", "example.com/i v1.0.0", "example.com/r v1.0.0"),
			pkg("d", "example.com/a", "example.com/c", "example.com/g", "example.com/i", "example.com/r"))
	}
	for _, v := range []string{"v1.0.0", "v1.4.0", "v1.5.0-pre"} {
		publish(t, proxy, "example.com/b", v, gomod("example.com/b", "1.21"), pkg("b"))
	}
	publish(t, proxy, "example.com/a", "v1.0.0", gomod("example.com/a", "1.21", "example.com/b v1.0.0"), pkg("a", "example.com/b"))
	publish(t, proxy, "example.com/a", "v1.1.0", gomod("example.com/a", "1.21", "example.com/b v1.5.0-pre"), pkg("a", "example.com/b"))
	publish(t, proxy, "example.com/c", "v1.0.0", gomod("example.com/c", "1.21"), pkg("c"))
	publish(t, proxy, "example.com/c", "v1.1.0", gomod("example.com/c", "1.21", "example.com/d v1.1.0"), pkg("c"))
	publish(t, proxy, "example.com/g", "v1.0.0", gomod("example.com/g", "1.21"), pkg("g"))
	publish(t, proxy, "example.com/g", "v1.1.0", gomod("example.com/g", "1.25"), pkg("g"))
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOFLAGS", "-modcacherw")
	// Maintain clears GOFLAGS, so the cache is read-only; clean it before TempDir removes it.
	t.Cleanup(func() { _ = exec.Command("go", "clean", "-modcache").Run() })

	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":   "module example.com/x\n\ngo 1.21\n\ntoolchain go1.22.1\n\nrequire example.com/d v1.0.0\n\nreplace example.com/r => ./r\n",
		"main.go":  "package main\n\nimport _ \"example.com/d\"\n\nfunc main() {}\n",
		"r/go.mod": "module example.com/r\n\ngo 1.21\n",
		"r/a.go":   "package r\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	sum, _ := os.ReadFile(filepath.Join(dir, "go.sum"))
	if !strings.Contains(string(mod), "example.com/i v1.0.0 // indirect") {
		t.Fatalf("setup go.mod:\n%s", mod)
	}

	out, err := Tidier{}.Maintain(context.Background(), dir, map[string][]byte{"go.sum": sum})
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.Parse("go.mod", out["go.mod"], nil)
	if err != nil {
		t.Fatalf("%v:\n%s", err, out["go.mod"])
	}
	got := map[string]string{"go": f.Go.Version, "toolchain": f.Toolchain.Name}
	for _, r := range f.Require {
		got[r.Mod.Path] = r.Mod.Version
		if r.Indirect == (r.Mod.Path == "example.com/d") {
			t.Errorf("%s indirect = %v", r.Mod.Path, r.Indirect)
		}
	}
	want := map[string]string{
		"go": "1.21", "toolchain": "go1.22.1", "example.com/d": "v1.0.0",
		"example.com/i": "v1.1.0", "example.com/a": "v1.1.0", "example.com/b": "v1.5.0-pre",
		"example.com/c": "v1.0.0", "example.com/g": "v1.0.0", "example.com/r": "v1.0.0",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("go.mod:\n%s", out["go.mod"])
	}
	for _, line := range []string{"example.com/i v1.1.0 h1:", "example.com/a v1.1.0 h1:", "example.com/b v1.5.0-pre h1:"} {
		if !strings.Contains(string(out["go.sum"]), line) {
			t.Errorf("go.sum misses %q:\n%s", line, out["go.sum"])
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "go.mod")); !bytes.Equal(b, mod) {
		t.Errorf("real go.mod changed:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "go.sum")); !bytes.Equal(b, sum) {
		t.Errorf("real go.sum changed:\n%s", b)
	}

	// Maintained: only conflicting modules are left, nothing changes.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), out["go.mod"], 0o644); err != nil {
		t.Fatal(err)
	}
	if again, err := (Tidier{}).Maintain(context.Background(), dir, map[string][]byte{"go.sum": out["go.sum"]}); err != nil || again != nil {
		t.Errorf("second run = %q, %v", again, err)
	}
	if none, err := (Tidier{}).Maintain(context.Background(), dir, map[string][]byte{}); err != nil || none != nil {
		t.Errorf("without go.sum = %q, %v", none, err)
	}
}

// TestMaintainStub checks the go get calls with a fake go: more than one module conflicts,
// example.com/bad fails otherwise. Nothing changes, so tidy does not run.
func TestMaintainStub(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH")
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "log")
	script := `#!` + sh + `
echo "GOWORK=$GOWORK GOFLAGS=$GOFLAGS $*" >> "$LOG"
n=0
for a in "$@"; do case "$a" in *@upgrade) n=$((n+1));; esac; done
if [ $n -gt 1 ]; then echo "go: $3 (v1.1.0) requires example.com/z@v2.0.0, not example.com/z@upgrade (v1.0.0)" >&2; exit 1; fi
case " $* " in *" example.com/bad@upgrade "*) echo "go: example.com/bad@upgrade: unknown revision" >&2; exit 1;; esac
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("LOG", log)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOWORK", "")
	dir := t.TempDir()
	mod := "module example.com/x\n\ngo 1.21\n\nrequire example.com/d v1.0.0\n\nrequire (\n\texample.com/a v1.0.0 // indirect\n\texample.com/b v1.0.0 // indirect\n\texample.com/c v1.0.0 // indirect\n\texample.com/r v1.0.0 // indirect\n)\n\nreplace example.com/r => ../r\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"go.sum": []byte("sum\n")}
	if out, err := (Tidier{}).Maintain(context.Background(), dir, files); err != nil || out != nil {
		t.Fatalf("Maintain = %q, %v", out, err)
	}
	b, _ := os.ReadFile(log)
	calls := regexp.MustCompile(`-modfile=\S+/go\.mod`).ReplaceAllString(string(b), "-modfile=go.mod")
	want := `GOWORK=off GOFLAGS= get -modfile=go.mod example.com/a@upgrade example.com/b@upgrade example.com/c@upgrade
GOWORK=off GOFLAGS= get -modfile=go.mod example.com/a@upgrade
GOWORK=off GOFLAGS= get -modfile=go.mod example.com/b@upgrade example.com/c@upgrade
GOWORK=off GOFLAGS= get -modfile=go.mod example.com/b@upgrade
GOWORK=off GOFLAGS= get -modfile=go.mod example.com/c@upgrade
`
	if calls != want {
		t.Errorf("calls:\n%s\nwant:\n%s", calls, want)
	}

	// A failure other than a conflict is an error.
	bad := strings.Replace(mod, "example.com/c v1.0.0", "example.com/bad v1.0.0", 1)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Tidier{}).Maintain(context.Background(), dir, files); err == nil || err.Error() != "go get in "+dir+": go: example.com/bad@upgrade: unknown revision" {
		t.Errorf("err = %v", err)
	}
	// Without indirect requirements go does not run.
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := (Tidier{}).Maintain(context.Background(), dir, files); err != nil || out != nil {
		t.Errorf("no indirect = %q, %v", out, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("go ran without indirect requirements: %v", err)
	}
	if _, err := (Tidier{}).Maintain(context.Background(), t.TempDir(), files); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("without go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("not a go.mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Tidier{}).Maintain(context.Background(), dir, files); err == nil {
		t.Error("unparsable go.mod: no error")
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Tidier{}).Maintain(context.Background(), dir, files); !errors.Is(err, exec.ErrNotFound) || !strings.HasPrefix(err.Error(), "go get in "+dir+": ") {
		t.Errorf("without go: %v", err)
	}
}
