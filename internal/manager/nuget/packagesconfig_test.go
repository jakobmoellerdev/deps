package nuget

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestPackagesConfig(t *testing.T) {
	const cfg = "Legacy.App/packages.config"
	deps := run(t, "testdata/packagesconfig", cfg, "Legacy.App/Legacy.App.csproj")
	got := deps[cfg]
	if len(got) != 3 || len(deps["Legacy.App/Legacy.App.csproj"]) != 0 {
		t.Fatalf("deps = %+v", deps)
	}
	for i, want := range [][2]string{{"EntityFramework", "6.4.0"}, {"Newtonsoft.Json", "12.0.3"}, {"Microsoft.CodeDom.Providers.DotNetCompilerPlatform", "2.0.1"}} {
		if d := got[i]; d.Name != want[0] || d.Version != want[1] || d.DepType != DepTypeNuGet || d.Datasource != dsNuGet || d.Skip != "" || d.Line != 3+i {
			t.Errorf("dep %d = %+v", i, d)
		}
		checkSpan(t, "testdata/packagesconfig", got[i], want[1])
	}

	m := &nugetManager{}
	if !m.Match("src/App/Packages.Config") || m.Match("src/App/app.config") {
		t.Error("Match")
	}
	for src, n := range map[string]int{"<configuration />": 0, `<packages><package id="A" version="1.0"/><group><package id="B" version="1.0"/></group></packages>`: 1} {
		if items, err := parsePackagesConfig([]byte(src)); err != nil || len(items) != n {
			t.Errorf("parse %q = %v, %v", src, items, err)
		}
	}
	for _, src := range []string{"<packages><package", "<?xml version=\"1.0\" encoding=\"utf-16\"?><packages/>"} {
		if _, err := parsePackagesConfig([]byte(src)); err == nil {
			t.Errorf("parse %q: no error", src)
		}
	}
}

func TestPackagesConfigFileEdits(t *testing.T) {
	root, err := filepath.Abs("testdata/packagesconfig")
	if err != nil {
		t.Fatal(err)
	}
	const proj = "Legacy.App/Legacy.App.csproj"
	src, err := os.ReadFile(filepath.Join(root, "Legacy.App", "Legacy.App.csproj"))
	if err != nil {
		t.Fatal(err)
	}
	m := &nugetManager{}
	up := func(name, from, to string) dep.Update {
		return dep.Update{Dep: dep.Dep{Manager: Name, File: "Legacy.App/packages.config", Name: name, Version: from}, NewVersion: to}
	}

	edits, err := m.FileEdits(root, up("newtonsoft.json", "12.0.3", "13.0.3"))
	if err != nil || len(edits) != 1 || len(edits[proj]) != 1 {
		t.Fatalf("FileEdits = %v, %v", edits, err)
	}
	out, err := patch.Apply(src, edits[proj])
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); !strings.Contains(s, `..\packages\Newtonsoft.Json.13.0.3\lib\net45\Newtonsoft.Json.dll`) || !strings.Contains(s, `Newtonsoft.Json.Bson.12.0.3`) || !strings.Contains(s, "Version=12.0.0.0") {
		t.Errorf("patched:\n%s", s)
	}

	edits, err = m.FileEdits(root, up("Microsoft.CodeDom.Providers.DotNetCompilerPlatform", "2.0.1", "4.1.0"))
	if err != nil || len(edits[proj]) != 4 {
		t.Fatalf("FileEdits = %v, %v", edits, err)
	}
	if out, err = patch.Apply(src, edits[proj]); err != nil || strings.Contains(string(out), "DotNetCompilerPlatform.2.0.1") {
		t.Errorf("patched = %v:\n%s", err, out)
	}

	for _, u := range []dep.Update{
		up("EntityFramework", "6.4.0", "6.4.0"),
		{Dep: dep.Dep{File: proj, Name: "EntityFramework", Version: "6.4.0"}, NewVersion: "6.5.1"},
		up("NotReferenced", "1.0.0", "2.0.0"),
	} {
		if edits, err := m.FileEdits(root, u); edits != nil || err != nil {
			t.Errorf("FileEdits(%s %s) = %v, %v", u.Dep.File, u.Dep.Name, edits, err)
		}
	}
	if _, err := m.FileEdits(t.TempDir(), up("A", "1.0.0", "2.0.0")); err == nil {
		t.Error("missing directory")
	}
}
