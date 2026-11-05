package gomod

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const src = `module example.com/x

go 1.27

require github.com/spf13/cobra v1.9.1

require (
	golang.org/x/mod v0.42.0
	github.com/a/ind v1.0.0 // indirect
	github.com/a/rep v1.1.1
	github.com/a/pseudo v0.0.0-20240101000000-abcdefabcdef
)

replace github.com/a/rep => ../rep
`

func TestExtract(t *testing.T) {
	b := []byte(src)
	m := New()
	deps, err := m.Extract("go.mod", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name, ver, skip, depType string
		line                     int
	}{
		{"go", "1.27", "", "go", 3},
		{"github.com/spf13/cobra", "v1.9.1", "", "direct", 5},
		{"golang.org/x/mod", "v0.42.0", "", "direct", 8},
		{"github.com/a/ind", "v1.0.0", "", "indirect", 9},
		{"github.com/a/rep", "v1.1.1", "replaced", "direct", 10},
		{"github.com/a/pseudo", "v0.0.0-20240101000000-abcdefabcdef", "", "direct", 11},
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps", len(deps))
	}
	for i, w := range want {
		d := deps[i]
		ds, vs := "go", "go"
		if i == 0 {
			ds, vs = "go-toolchain", "tag"
		}
		if d.Manager != Name || d.Name != w.name || d.Version != w.ver || d.Skip != w.skip || d.Line != w.line ||
			d.DepType != w.depType || d.Datasource != ds || d.Versioning != vs {
			t.Errorf("%d: %+v", i, d)
		}
		if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != w.ver {
			t.Errorf("%d: span %q", i, got)
		}
		if i > 0 && (len(d.NameParts) != 1 || string(b[d.NameParts[0].Span.Start:d.NameParts[0].Span.End]) != w.name || d.NameParts[0].Text != w.name) {
			t.Errorf("%d: name parts %+v", i, d.NameParts)
		}
	}

	deps, _ = m.Extract("sub/go.mod", b, manager.Env{})
	if deps[1].Skip != "" || deps[3].Skip != "" {
		t.Errorf("skips: %+v", deps)
	}

	work := []byte("go 1.25.0\n\ntoolchain go1.26.1\n\nuse (\n\t./a\n\t./b\n)\n")
	wdeps, err := m.Extract("ws/go.work", work, manager.Env{})
	if err != nil || len(wdeps) != 2 {
		t.Fatalf("go.work: %+v, %v", wdeps, err)
	}
	for i, w := range []struct{ name, ver string }{{"go", "1.25.0"}, {"toolchain", "1.26.1"}} {
		d := wdeps[i]
		if d.Name != w.name || d.DepType != w.name || d.Version != w.ver || d.File != "ws/go.work" || string(work[d.RefSpan.Start:d.RefSpan.End]) != w.ver {
			t.Errorf("go.work %d: %+v", i, d)
		}
	}
	if wdeps, err := m.Extract("go.work", []byte("use ./a\n"), manager.Env{}); err != nil || len(wdeps) != 0 {
		t.Errorf("go.work without directives: %+v, %v", wdeps, err)
	}
	if _, err := m.Extract("go.work", []byte("usee ./a\n"), manager.Env{}); err == nil {
		t.Error("want go.work parse error")
	}
	if _, err := m.Extract("go.mod", []byte("modul x\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
	if !m.Match("go.mod") || !m.Match("a/go.mod") || !m.Match("a/go.work") || m.Match("a/xgo.mod") || m.Match("go.work.sum") || m.Match("go.sum") || m.Name() != Name {
		t.Error("Match/Name")
	}
}

func TestDirectives(t *testing.T) {
	tests := []struct {
		name, src            string
		goVer, goSkip        string
		tcVer, tcSkip, tcRaw string
	}{
		{name: "2-part go, no toolchain", src: "module x\n\ngo 1.24\n", goVer: "1.24"},
		{name: "3-part go with toolchain", src: "module x\n\ngo 1.24.0\n\ntoolchain go1.27.1\n", goVer: "1.24.0", tcVer: "1.27.1"},
		{name: "crlf", src: "module x\r\ngo 1.24.2\r\ntoolchain go1.25.0\r\n", goVer: "1.24.2", tcVer: "1.25.0"},
		{name: "prerelease", src: "module x\ngo 1.26rc1\ntoolchain go1.26rc2\n", goVer: "1.26rc1", goSkip: "prerelease toolchain", tcVer: "1.26rc2", tcSkip: "prerelease toolchain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := []byte(tt.src)
			deps, err := New().Extract("go.mod", b, manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			n := 1
			if tt.tcVer != "" {
				n = 2
			}
			if len(deps) != n {
				t.Fatalf("got %+v", deps)
			}
			check := func(d dep.Dep, name, ver, skip string) {
				t.Helper()
				if d.Name != name || d.DepType != name || d.Version != ver || d.Skip != skip ||
					d.Datasource != "go-toolchain" || d.Versioning != "tag" {
					t.Errorf("%+v", d)
				}
				if got := string(b[d.RefSpan.Start:d.RefSpan.End]); got != ver {
					t.Errorf("span %q", got)
				}
			}
			check(deps[0], "go", tt.goVer, tt.goSkip)
			if n == 2 {
				check(deps[1], "toolchain", tt.tcVer, tt.tcSkip)
				if string(b[deps[1].RefSpan.Start-2:deps[1].RefSpan.Start]) != "go" {
					t.Error("toolchain span must follow the go prefix")
				}
				out, err := applyUpdate(b, deps[1], "1.27.2")
				if err != nil || string(out) != tt.src[:deps[1].RefSpan.Start]+"1.27.2"+tt.src[deps[1].RefSpan.End:] {
					t.Errorf("edit: %q %v", out, err)
				}
			}
		})
	}
}

func TestPolicyDefaults(t *testing.T) {
	deps, err := New().Extract("go.mod", []byte(src+"toolchain go1.27.1\n"), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"go": true, "indirect": true, "toolchain": false, "direct": false}
	def := config.Default()
	f := false
	optIn := &config.Config{Version: 1, Pin: config.PinKeep, Rules: []config.Rule{{DepTypes: []string{"go", "indirect"}, Ignore: &f}}}
	for _, d := range deps {
		if d.Skip != "" {
			continue
		}
		if got := def.Policy(d).Ignore; got != want[d.DepType] {
			t.Errorf("%s (%s): default Ignore = %v", d.Name, d.DepType, got)
		}
		if optIn.Policy(d).Ignore {
			t.Errorf("%s: opt-in rule should un-ignore", d.Name)
		}
	}
}

func applyUpdate(b []byte, d dep.Dep, v string) ([]byte, error) {
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: v})
	if err != nil {
		return nil, err
	}
	return patch.Apply(b, edits)
}

func TestEdits(t *testing.T) {
	b := []byte(src)
	deps, err := New().Extract("go.mod", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 2, 3} {
		out, err := applyUpdate(b, deps[i], "v9.9.9")
		if err != nil {
			t.Fatal(err)
		}
		if want := string(b[:deps[i].RefSpan.Start]) + "v9.9.9" + string(b[deps[i].RefSpan.End:]); string(out) != want {
			t.Errorf("%d: got %q", i, out)
		}
	}
}

func TestMajorPathEdits(t *testing.T) {
	const mod = "module example.com/x\n\nrequire (\n\tgithub.com/google/go-github/v92 v92.0.0\n\tgithub.com/b/c v1.0.0\n)\n"
	b := []byte(mod)
	deps, err := New().Extract("go.mod", b, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	u := dep.Update{Dep: deps[0], Type: dep.Major, NewVersion: "v93.0.0", NewName: "github.com/google/go-github/v93"}
	edits, err := New().Edits(u)
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(b, edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(mod, "go-github/v92 v92.0.0", "go-github/v93 v93.0.0", 1); string(out) != want {
		t.Errorf("got %q", out)
	}
	d := deps[0]
	d.NameParts = nil
	if _, err := New().Edits(dep.Update{Dep: d, NewVersion: "v93.0.0", NewName: "x/v93"}); err == nil {
		t.Error("want error without a name span")
	}
}

func TestFileEdits(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"mod/go.mod": "module example.com/x\n\nrequire github.com/google/go-github/v92 v92.0.0\n",
		"mod/a.go": "package a\n\nimport (\n\t\"fmt\"\n\tgh \"github.com/google/go-github/v92/github\"\n\t`github.com/google/go-github/v92`\n\t\"github.com/google/go-github/v921\"\n)\n\n" +
			"// github.com/google/go-github/v92 in a comment stays\nvar _ = fmt.Sprint(gh.Client{})\n",
		"mod/sub/b_test.go":        "package sub\n\nimport \"github.com/google/go-github/v92/github\"\n",
		"mod/sub/c.go":             "package sub\n",
		"mod/notes.txt":            "github.com/google/go-github/v92\n",
		"mod/testdata/x.go":        "package x\n\nimport \"github.com/google/go-github/v92/github\"\n",
		"mod/vendor/y/y.go":        "package y\n\nimport \"github.com/google/go-github/v92/github\"\n",
		"mod/_tools/z.go":          "package z\n\nimport \"github.com/google/go-github/v92/github\"\n",
		"mod/nested/go.mod":        "module example.com/x/nested\n",
		"mod/nested/n.go":          "package n\n\nimport \"github.com/google/go-github/v92/github\"\n",
		"other/o.go":               "package o\n\nimport \"github.com/google/go-github/v92/github\"\n",
		"mod/broken/syntax_err.go": "package\n",
	}
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fe := New().(manager.FileEditor)
	d := dep.Dep{Manager: Name, File: "mod/go.mod", Name: "github.com/google/go-github/v92", Version: "v92.0.0"}
	if got, err := fe.FileEdits(root, dep.Update{Dep: d, NewVersion: "v92.1.0"}); got != nil || err != nil {
		t.Errorf("same path: %v %v", got, err)
	}
	u := dep.Update{Dep: d, Type: dep.Major, NewVersion: "v93.0.0", NewName: "github.com/google/go-github/v93"}
	got, err := fe.FileEdits(root, u)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"mod/a.go": "package a\n\nimport (\n\t\"fmt\"\n\tgh \"github.com/google/go-github/v93/github\"\n\t`github.com/google/go-github/v93`\n\t\"github.com/google/go-github/v921\"\n)\n\n" +
			"// github.com/google/go-github/v92 in a comment stays\nvar _ = fmt.Sprint(gh.Client{})\n",
		"mod/sub/b_test.go": "package sub\n\nimport \"github.com/google/go-github/v93/github\"\n",
	}
	if len(got) != len(want) {
		t.Fatalf("files %v", got)
	}
	for p, w := range want {
		out, err := patch.Apply([]byte(files[p]), got[p])
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != w {
			t.Errorf("%s: got %q", p, out)
		}
	}

	// A file that mentions the path but does not parse is an error.
	if err := os.WriteFile(filepath.Join(root, "mod", "broken", "syntax_err.go"), []byte("package\nimport \"github.com/google/go-github/v92\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fe.FileEdits(root, u); err == nil || !strings.Contains(err.Error(), "syntax_err.go") {
		t.Errorf("want parse error, got %v", err)
	}
	if _, err := fe.FileEdits(filepath.Join(root, "missing"), u); err == nil {
		t.Error("want walk error")
	}
}

func TestLockRoots(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "", "go.sum": "", "b/go.mod": "", "b/go.sum": "", "nosum/go.mod": "",
		"vend/go.mod": "", "vend/go.sum": "", "vend/vendor/modules.txt": "",
		// A vendoring workspace: its vendor directory derives from all modules.
		"ws/go.work": "go 1.22\n\nuse ./m\n", "ws/vendor/modules.txt": "", "ws/m/go.mod": "", "ws/m/go.sum": "",
		"ws2/go.work": "go 1.22\n\nuse ./m\n", "ws2/m/go.mod": "", "ws2/m/go.sum": "",
	}
	for p, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := New().(manager.LockMaintainer).LockRoots(root, []string{"vend/go.mod", "nosum/go.mod", "go.mod", "b/go.mod", "ws/m/go.mod", "ws/go.work", "ws2/m/go.mod"})
	want := []manager.LockRoot{
		{Manifest: "b/go.mod", Lockfile: "b/go.sum"}, {Manifest: "go.mod", Lockfile: "go.sum"},
		{Manifest: "vend/go.mod", Lockfile: "vend/go.sum"}, {Manifest: "ws2/m/go.mod", Lockfile: "ws2/m/go.sum"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %+v, want %+v", got, want)
	}
}

func TestWorkspace(t *testing.T) {
	root := t.TempDir()
	for p, content := range map[string]string{
		"go.work":     "go 1.22\n\nuse (\n\t.\n\t./a\n\t../out\n\t/abs\n)\n",
		"a/go.mod":    "module a\n",
		"a/b/go.mod":  "module b\n",
		"bad/go.work": "usee x\n",
	} {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		dir, ws string
		ok      bool
	}{{".", ".", true}, {"a", ".", true}, {"a/b", ".", false}, {"bad/m", "bad", false}} {
		if ws, ok := Workspace(root, c.dir); ws != c.ws || ok != c.ok {
			t.Errorf("Workspace(%s) = %q, %v; want %q, %v", c.dir, ws, ok, c.ws, c.ok)
		}
	}
	if mods, err := WorkspaceModules(root); err != nil || !slices.Equal(mods, []string{".", "a"}) {
		t.Errorf("WorkspaceModules = %q, %v", mods, err)
	}
	if _, ok := Workspace(t.TempDir(), "a"); ok {
		t.Error("Workspace without go.work")
	}
	if Vendors(root) {
		t.Error("Vendors without vendor/modules.txt")
	}
}
