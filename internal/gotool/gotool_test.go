package gotool

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// writeTree writes files (slash paths relative to root -> content) below root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTidierScopeInputs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"staging/apiserver/go.mod":             "module k8s.io/apiserver\n\nreplace (\n\tk8s.io/api => ../api\n\tk8s.io/x => ./third_party/x\n\tk8s.io/y => k8s.io/y v1.0.0\n)\n",
		"staging/apiserver/a.go":               "package a\n\nimport \"k8s.io/api\"\n",
		"staging/api/api.go":                   "package api\n",
		"staging/apiserver/third_party/x/x.go": "package x\n",
	})
	tidier := Tidier{Imports: NewImportCache()}
	paths, digests, err := tidier.ScopeInputs(t.Context(), root, "staging/apiserver", []string{"go.mod"})
	if want := []string{"staging/apiserver/go.mod", "staging/api/go.mod", "staging/apiserver/third_party/x/go.mod"}; err != nil || !slices.Equal(paths, want) {
		t.Errorf("ScopeInputs paths = %q, %v; want %q", paths, err, want)
	}
	var dirs []string
	for _, d := range digests {
		f := strings.Fields(d)
		if len(f) != 4 || f[0] != "go" || f[1] != "imports" || len(f[3]) != 64 {
			t.Errorf("digest %q", d)
			continue
		}
		dirs = append(dirs, f[2])
	}
	if want := []string{"staging/apiserver", "staging/api", "staging/apiserver/third_party/x"}; !slices.Equal(dirs, want) {
		t.Errorf("digest modules = %q, want %q", dirs, want)
	}
	if len(tidier.Imports.m) != 3 {
		t.Errorf("cached %d scans, want 3", len(tidier.Imports.m))
	}

	writeTree(t, root, map[string]string{"go.mod": "module m\n\nreplace k8s.io/api => /abs/api\n"})
	if _, _, err := (Tidier{}).ScopeInputs(t.Context(), root, ".", nil); err == nil {
		t.Error("ScopeInputs accepted an absolute replacement")
	}
	writeTree(t, root, map[string]string{"go.mod": "module m\n\nreplace k8s.io/api => ../api\n"})
	if _, _, err := (Tidier{}).ScopeInputs(t.Context(), root, ".", nil); err == nil {
		t.Error("ScopeInputs accepted a replacement outside the root")
	}
	if _, _, err := (Tidier{}).ScopeInputs(t.Context(), root, "missing", nil); err == nil {
		t.Error("ScopeInputs without go.mod succeeded")
	}
}

func TestImportDigest(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":                  "module example.com/m\n",
		"main.go":                 "// Copyright\n\n/* block */\npackage main\n\nimport (\n\t\"fmt\"\n\t_ \"example.com/dep\"\n)\n\nfunc main() { fmt.Println() }\n",
		"main_test.go":            "package main\n\nimport \"testing\"\n",
		"README.md":               "readme\n",
		"testdata/x.go":           "package x\n\nimport \"example.com/testdata\"\n",
		"_tools/t.go":             "package t\n\nimport \"example.com/underscore\"\n",
		".hidden/h.go":            "package h\n\nimport \"example.com/hidden\"\n",
		"nested/go.mod":           "module example.com/m/nested\n",
		"nested/n.go":             "package n\n\nimport \"example.com/nested\"\n",
		"vendor/v.go":             "package v\n",
		"vendor/example.com/d.go": "package d\n\nimport \"example.com/vendored\"\n",
		"tools.go":                "//go:build ignore\n\npackage main\n\nimport \"example.com/ignored\"\n",
		"linux.go":                "//go:build linux && !cgo\n\npackage main\n\nimport \"example.com/linux\"\n",
		"lib/l.go":                "package lib\n",
		"testonly/t_test.go":      "package testonly\n",
	})
	digest := func() string {
		t.Helper()
		lines, err := scanModule(root)
		if err != nil {
			t.Fatal(err)
		}
		return importDigest(lines, map[string]bool{"example.com": true, "local": true})
	}
	base := digest()
	// Neither code outside the imports nor other files nor excluded directories count.
	for rel, content := range map[string]string{
		"main.go":                 "// Copyright 2026\n\npackage main\n\nimport (\n\t_ \"example.com/dep\"\n\t\"fmt\"\n)\n\nfunc main() { fmt.Println(\"changed\") }\n",
		"README.md":               "changed\n",
		"testdata/x.go":           "package x\n\nimport \"example.com/other\"\n",
		"_tools/t.go":             "package t\n\nimport \"example.com/other\"\n",
		"nested/n.go":             "package n\n\nimport \"example.com/other\"\n",
		"vendor/example.com/d.go": "package d\n\nimport \"example.com/other\"\n",
		"tools.go":                "//go:build ignore\n\npackage main\n\nimport \"example.com/other\"\n",
		"helper.go":               "package main\n\nimport \"fmt\"\n\nvar _ = fmt.Sprint\n",
		"std.go":                  "package main\n\nimport (\n\t\"os\"\n\t\"C\"\n\t\"net/http\"\n\t\"notstd/x\"\n)\n",
		"own.go":                  "package main\n\nimport (\n\t\"example.com/m/lib\"\n)\n",
	} {
		writeTree(t, root, map[string]string{rel: content})
		if got := digest(); got != base {
			t.Errorf("editing %s changed the digest", rel)
		}
	}
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"an added import", map[string]string{"helper.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/new\"\n)\n"}},
		{"an import of a dotless module path of go.mod", map[string]string{"helper.go": "package main\n\nimport \"local/x\"\n"}},
		{"an import of the nested module", map[string]string{"own.go": "package main\n\nimport \"example.com/m/nested\"\n"}},
		{"an import of a test-only package", map[string]string{"own.go": "package main\n\nimport \"example.com/m/testonly\"\n"}},
		{"an import moved to a test", map[string]string{"main.go": "package main\n\nimport \"fmt\"\n", "main_test.go": "package main\n\nimport (\n\t\"testing\"\n\t_ \"example.com/dep\"\n)\n"}},
		{"a file no longer ignored", map[string]string{"tools.go": "package main\n\nimport \"example.com/ignored\"\n"}},
		{"a new package", map[string]string{"pkg/p.go": "package p\n\nimport \"fmt\"\n"}},
		{"a new nested module", map[string]string{"other/go.mod": "module example.com/m/other\n"}},
		{"a vendored package of the vendor directory itself", map[string]string{"vendor/v.go": "package v\n\nimport \"example.com/v\"\n"}},
	} {
		writeTree(t, root, tc.files)
		got := digest()
		if got == base {
			t.Errorf("%s kept the digest", tc.name)
		}
		base = got
	}
	if _, err := scanModule(filepath.Join(root, "missing")); err == nil {
		t.Error("scanModule of a missing directory succeeded")
	}
}

func TestShouldBuild(t *testing.T) {
	for src, want := range map[string]bool{
		"package p\n":                                        true,
		"//go:build linux\n\npackage p\n":                    true,
		"//go:build !linux && !windows\n\npackage p\n":       true,
		"//go:build ignore\n\npackage p\n":                   false,
		"//go:build !ignore\n\npackage p\n":                  true,
		"// +build ignore\n\npackage p\n":                    false,
		"// +build linux\n// +build ignore\n\npackage p\n":   false,
		"// +build ignore\npackage p\n":                      true, // not followed by a blank line
		"//go:build linux\n//go:build darwin\n\npackage p\n": false,
		"//go:build (\n\npackage p\n":                        false,
		"/* x */\n//go:build ignore\n\npackage p\n":          false,
		"//go:build ignore\n\n/* x\n*/\npackage p\n":         false,
		"package p\n//go:build ignore\n":                     true,
	} {
		if got := shouldBuild([]byte(src)); got != want {
			t.Errorf("shouldBuild(%q) = %v, want %v", src, got, want)
		}
	}
}

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
		if scope, ok := (Tidier{}).Scope(t.Context(), dir, manifest); scope != want || !ok {
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
		if scope, ok := (Tidier{}).Scope(t.Context(), dir, manifest); scope != want || !ok {
			t.Errorf("Scope(%s) = %q, %v, want %q", manifest, scope, ok, want)
		}
	}
	if scope, ok := (Tidier{}).Scope(t.Context(), t.TempDir(), "a/b.go"); ok {
		t.Errorf("Scope without go.mod = %q, true", scope)
	}
}

// TestTidyPathChanged requires a module version whose go.mod declares another path (a module
// renamed upstream): tidy cannot succeed, so the update is skipped; other tidy failures stay
// errors.
func TestTidyPathChanged(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	proxy := t.TempDir()
	writeModule(t, proxy, "example.com/old", "v1.0.0")
	publish(t, proxy, "example.com/old", "v1.1.0", "module example.com/new\n", map[string]string{"a.go": "package m\n\nconst V = 1\n"})
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Cleanup(func() { _ = exec.Command("go", "clean", "-modcache").Run() })
	dir := t.TempDir()
	src := "package main\n\nimport \"example.com/old\"\n\nfunc main() { _ = m.V }\n"
	mod := func(version string) string {
		return "module example.com/x\n\ngo 1.21\n\nrequire example.com/old " + version + "\n"
	}
	for name, content := range map[string]string{"main.go": src, "go.mod": mod("v1.0.0")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tidy := func(version string) error {
		_, err := Tidier{}.Process(context.Background(), dir, map[string][]byte{"go.mod": []byte(mod(version))}, map[string][]byte{})
		return err
	}
	err := tidy("v1.1.0")
	if skip, ok := errors.AsType[*dep.SkipError](err); !ok || skip.Reason != "module path changed to example.com/new (example.com/old@v1.1.0)" {
		t.Errorf("renamed: %v", err)
	}
	err = tidy("v1.2.0") // no such version
	if _, ok := errors.AsType[*dep.SkipError](err); ok || err == nil || !strings.Contains(err.Error(), "go mod tidy in ") {
		t.Errorf("missing version: %v", err)
	}
	if err := tidy("v1.0.0"); err != nil {
		t.Errorf("valid version: %v", err)
	}
}

func TestPathChanged(t *testing.T) {
	for stderr, want := range map[string]string{
		// through an import chain (kubernetes' crypto11 update)
		"go: k8s.io/kms/plugins/mock imports\n\tgithub.com/golang/protobuf/proto: github.com/ThalesIgnite/crypto11@v1.6.8: parsing go.mod:\n\tmodule declares its path as: github.com/eclipse-keypont/crypto11\n\t        but was required as: github.com/ThalesIgnite/crypto11": "module path changed to github.com/eclipse-keypont/crypto11 (github.com/ThalesIgnite/crypto11@v1.6.8)",
		"go: github.com/flatcar/ignition/v2@v2.2.0: parsing go.mod:\n\tmodule declares its path as: github.com/coreos/ignition/v2\n\t        but was required as: github.com/flatcar/ignition/v2":                                                                                "module path changed to github.com/coreos/ignition/v2 (github.com/flatcar/ignition/v2@v2.2.0)",
		// a replacement's path differs from the requirement: not this case
		"go: example.com/a@v1.0.0 (replaced by ../a): parsing ../a/go.mod:\n\tmodule declares its path as: example.com/b\n\t        but was required as: example.com/a": "",
		"go: example.com/a@v1.2.0: reading example.com/a/go.mod at revision v1.2.0: unknown revision v1.2.0":                                                            "",
	} {
		got := pathChanged(&cmdError{args: "mod tidy", stderr: stderr, err: errors.New("exit status 1")})
		if (got == nil) != (want == "") || got != nil && got.Reason != want {
			t.Errorf("pathChanged(%q) = %v, want %q", stderr, got, want)
		}
	}
	if got := pathChanged(errors.New("go mod tidy: boom")); got != nil {
		t.Errorf("non-command error = %v", got)
	}
}
