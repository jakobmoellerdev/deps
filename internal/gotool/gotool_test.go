package gotool

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTidyOffline tidies a module without requirements, which needs no network.
func TestTidyOffline(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	dir := t.TempDir()
	orig := []byte("module example.com/x\n\ngo 1.21\n")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Tidier{}.Process(context.Background(), dir, map[string][]byte{"go.mod": []byte("module   example.com/x\n\n\n\ngo 1.21\n")}, map[string][]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if names := (Tidier{}).Siblings(); len(names) != 1 || names[0] != "go.sum" {
		t.Errorf("Siblings = %v", names)
	}
	for manifest, want := range map[string]string{"a/b/go.mod": "a/b", "go.mod": "."} {
		if scope, ok := (Tidier{}).Scope(dir, manifest); scope != want || !ok {
			t.Errorf("Scope(%s) = %q, %v, want %q", manifest, scope, ok, want)
		}
	}
	mod, sum := out["go.mod"], out["go.sum"]
	if !strings.HasPrefix(string(mod), "module example.com/x\n") {
		t.Errorf("tidied go.mod not reformatted:\n%s", mod)
	}
	if len(sum) != 0 {
		t.Errorf("go.sum = %q, want empty", sum)
	}
	got, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Errorf("real go.mod changed:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.sum")); !os.IsNotExist(err) {
		t.Errorf("real go.sum created: %v", err)
	}

	_, err = Tidier{}.Process(context.Background(), dir, map[string][]byte{"go.mod": []byte("this is not a go.mod\n")}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "go mod tidy in "+dir+": ") {
		t.Fatalf("err = %v, want go mod tidy in %s: ...", err, dir)
	}
}

// TestModuleTime serves a module from a file:// GOPROXY, which needs no network.
func TestModuleTime(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	proxy := t.TempDir()
	v := filepath.Join(proxy, "example.com", "m", "@v")
	if err := os.MkdirAll(v, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"list":        "v1.0.0\nv1.1.0\n",
		"v1.0.0.info": `{"Version":"v1.0.0","Time":"2026-10-30T12:00:00Z"}`,
		"v1.0.0.mod":  "module example.com/m\n",
		"v1.1.0.info": `{"Version":"v1.1.0"}`,
		"v1.1.0.mod":  "module example.com/m\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(v, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOFLAGS", "-modcacherw")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		version, want, wantErr string
	}{
		{version: "v1.0.0", want: "2026-10-30T12:00:00Z"},
		{version: "v1.1.0", want: "0001-01-01T00:00:00Z"},
		{version: "v9.0.0", wantErr: "go list -m -json example.com/m@v9.0.0"},
	}
	for _, tt := range tests {
		got, err := ModuleTime(context.Background(), dir, "example.com/m", tt.version)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%s: err = %v, want %q", tt.version, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tt.version, err)
		}
		if s := got.UTC().Format(time.RFC3339); s != tt.want {
			t.Errorf("%s: time = %s, want %s", tt.version, s, tt.want)
		}
	}
	m, err := ModuleLatest(context.Background(), dir, "example.com/m")
	if err != nil || m.Version != "v1.1.0" || m.Hash() != "" {
		t.Errorf("ModuleLatest = %+v, %v", m, err)
	}
	if _, err := ModuleLatest(context.Background(), dir, "example.com/missing"); err == nil || !strings.Contains(err.Error(), "example.com/missing@latest") {
		t.Errorf("ModuleLatest missing: %v", err)
	}
	if h := (Module{Origin: &struct{ Hash string }{"abc"}}).Hash(); h != "abc" {
		t.Errorf("Hash = %q", h)
	}
}

// writeModule writes module path@version (one package, file a.go) to a file:// proxy dir.
func writeModule(t *testing.T, proxy, path, version string) {
	t.Helper()
	publish(t, proxy, path, version, "module "+path+"\n", map[string]string{"a.go": "package m\n\nconst V = 1\n"})
}

// publish adds module path@version with go.mod mod and the source files src to a file://
// proxy dir.
func publish(t *testing.T, proxy, path, version, mod string, src map[string]string) {
	t.Helper()
	v := filepath.Join(proxy, filepath.FromSlash(path), "@v")
	if err := os.MkdirAll(v, 0o755); err != nil {
		t.Fatal(err)
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, content := range src {
		w, err := zw.Create(path + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	w, err := zw.Create(path + "@" + version + "/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(mod)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		version + ".info": []byte(`{"Version":"` + version + `"}`),
		version + ".mod":  []byte(mod),
		version + ".zip":  zb.Bytes(),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(v, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list, err := os.OpenFile(filepath.Join(v, "list"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer list.Close()
	if _, err := list.WriteString(version + "\n"); err != nil {
		t.Fatal(err)
	}
}

// TestTidyOverlay moves a module to its /v2 path: the patched source file is only an overlay,
// and tidy must see its import, not the real file's.
func TestTidyOverlay(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	proxy := t.TempDir()
	writeModule(t, proxy, "example.com/m", "v1.0.0")
	writeModule(t, proxy, "example.com/m/v2", "v2.0.0")
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	cache := t.TempDir()
	t.Setenv("GOMODCACHE", cache)
	// Tidy clears GOFLAGS, so the cache is read-only; clean it before TempDir removes it.
	t.Cleanup(func() { _ = exec.Command("go", "clean", "-modcache").Run() })
	dir := t.TempDir()
	src := "package main\n\nimport \"example.com/m\"\n\nfunc main() { _ = m.V }\n"
	for name, content := range map[string]string{
		"go.mod":      "module example.com/x\n\ngo 1.21\n\nrequire example.com/m v1.0.0\n",
		"cmd/main.go": src,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifests := map[string][]byte{
		"go.mod":      []byte("module example.com/x\n\ngo 1.21\n\nrequire example.com/m/v2 v2.0.0\n"),
		"cmd/main.go": []byte(strings.Replace(src, `"example.com/m"`, `"example.com/m/v2"`, 1)),
	}
	out, err := Tidier{}.Process(context.Background(), dir, manifests, map[string][]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if mod := string(out["go.mod"]); !strings.Contains(mod, "example.com/m/v2 v2.0.0") || strings.Contains(mod, "example.com/m v1.0.0") {
		t.Errorf("go.mod:\n%s", mod)
	}
	if !strings.Contains(string(out["go.sum"]), "example.com/m/v2 v2.0.0 h1:") {
		t.Errorf("go.sum:\n%s", out["go.sum"])
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "cmd", "main.go")); string(b) != src {
		t.Errorf("real source changed: %s", b)
	}
	if _, err := (Tidier{}).Process(context.Background(), dir, map[string][]byte{"cmd/main.go": nil}, nil); err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Errorf("want error without go.mod, got %v", err)
	}

	for manifest, want := range map[string]string{"cmd/main.go": ".", "cmd/sub/x.go": "."} {
		if scope, ok := (Tidier{}).Scope(dir, manifest); scope != want || !ok {
			t.Errorf("Scope(%s) = %q, %v, want %q", manifest, scope, ok, want)
		}
	}
	if scope, ok := (Tidier{}).Scope(t.TempDir(), "a/b.go"); ok {
		t.Errorf("Scope without go.mod = %q, true", scope)
	}
}
