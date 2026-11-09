package nuget

import (
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// testdata of the cake manager, verbatim (fetched 2026-11-09):
//   - cakebuild: cake-build/cake build.cake (quoted directives with a feed URL, `#load` of
//     local scripts, a UTF-8 BOM)
//   - cakeissues: cake-contrib/Cake.Issues recipe.cake (`#load` from an Azure Artifacts feed, a
//     prerelease, a directive inside a string)
//   - cakerecipe: cake-contrib/Cake.Recipe recipe.cake

// cakeFiles extracts and links in-memory files (slash path → content) in order with the cake
// manager, as the engine does.
func cakeFiles(t *testing.T, files ...string) map[string][]dep.Dep {
	t.Helper()
	m := NewCake()
	var all [][]dep.Dep
	for i := 0; i < len(files); i += 2 {
		if !m.Match(files[i]) {
			t.Fatalf("%s does not match", files[i])
		}
		deps, err := m.Extract(files[i], []byte(files[i+1]), manager.Env{})
		if err != nil {
			t.Fatalf("%s: %v", files[i], err)
		}
		all = append(all, deps)
	}
	m.(manager.Linker).Link(all)
	out := map[string][]dep.Dep{}
	for i := 0; i < len(files); i += 2 {
		out[files[i]] = all[i/2]
	}
	return out
}

func TestCakeMatch(t *testing.T) {
	m := NewCake()
	for p, want := range map[string]bool{
		"build.cake": true, "build/parameters.cake": true, "recipe.cake": true, "NuGet.Config": true, "src/nuget.config": true,
		"build.cs": false, "cake": false, "build.cake.bak": false, "App.csproj": false, "global.json": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
	if m.Name() != CakeName {
		t.Errorf("Name = %q", m.Name())
	}
}

func TestCakeFixtures(t *testing.T) {
	type want struct {
		depType, name, version, source, span string
		line                                 int
	}
	for _, tt := range []struct {
		file string
		want []want
	}{
		{"cakebuild/build.cake", []want{
			{DepTypeAddin, "Cake.Twitter", "6.0.0", "https://api.nuget.org/v3/index.json", "6.0.0", 2},
			{DepTypeTool, "GitVersion.Tool", "6.7.0", "https://api.nuget.org/v3/index.json", "6.7.0", 5},
			{DepTypeTool, "GitReleaseManager.Tool", "0.20.0", "https://api.nuget.org/v3/index.json", "0.20.0", 6},
		}},
		{"cakeissues/recipe.cake", []want{
			{DepTypeLoad, "Cake.Recipe", "4.1.0-alpha0042", "https://pkgs.dev.azure.com/cake-contrib/Home/_packaging/addins/nuget/v3/index.json", "4.1.0-alpha0042", 1},
			{DepTypeAddin, "Cake.Codecov", "3.0.1", "", "3.0.1", 6},
		}},
		{"cakerecipe/recipe.cake", []want{
			{DepTypeAddin, "Cake.FileHelpers", "6.1.3", "", "6.1.3", 1},
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(tt.file)))
			if err != nil {
				t.Fatal(err)
			}
			deps := cakeFiles(t, tt.file, string(src))[tt.file]
			if len(deps) != len(tt.want) {
				t.Fatalf("got %d deps, want %d: %+v", len(deps), len(tt.want), deps)
			}
			for i, w := range tt.want {
				d := deps[i]
				if d.Manager != CakeName || d.Datasource != dsNuGet || d.Versioning != scheme || d.DepType != w.depType || d.Name != w.name ||
					d.Version != w.version || d.Source != w.source || d.Line != w.line || d.Skip != "" || d.Range != "" {
					t.Errorf("dep %d = %+v, want %+v", i, d, w)
				}
				if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != w.span {
					t.Errorf("%s: span reads %q, want %q", d.Name, got, w.span)
				}
			}
		})
	}
}

func TestCakeExtract(t *testing.T) {
	// Renovate's cake fixture (lib/modules/manager/cake/__fixtures__/build.cake) and more.
	src := `foo
#addin nuget:?package=Foo.Foo
#addin "nuget:?package=Bim.Bim&version=6.6.6"
#tool nuget:https://example.com?package=Bar.Bar&version=2.2.2
#tool nuget:https://example.com/feed/v3/?package=Cake.Git&version=2.2.3
#tool nuget:https://example.com/feed/v3/index.json?package=Cake.MinVer&version=2.2.4
#module nuget:file:///tmp/?package=Baz.Baz&version=3.3.3
#load nuget:?package=Cake.7zip&version=1.0.3
#l nuget:?package=Cake.asciidoctorj&version=1.0.0
// #module nuget:?package=Qux.Qux&version=4.4.4
/*
#module nuget:?package=Quux.Quux&version=5.5.5
*/
bar
#module nuget:foobar!@#
  #addin	nuget:?PACKAGE=Indented&Version=1.2&include=./**/*.dll   // trailing comment
#addin "nuget:?package=Unclosed&version=1.0.0
#addin "nuget:?package=Trailing&version=1.0.0" x
#addin nuget:?version=1.0.0
#addin nuget:?package=Escaped&version=1.0.0%2Bmeta
#addin nuget:?package=Float&version=1.*
#addin nuget:?package=All&version=*
#addin nuget:?package=Bad&version=latest
#addin nuget:?package=Empty&version=
#addin nuget:https://[::1?package=BadURL&version=1.0.0
#tool dotnet:?package=Interval&version=[1.0,2.0)
#load "./build/parameters.cake"
#r "System.Net.Http"
#line 12
#loader nuget:?package=NotADirective&version=1.0.0
var s = "#tool nuget:?package=InString&version=1.0.0";
var c = '"'; // a quote character must not open a string
#addin nuget:?package=AfterChar&version=1.0.0
var v = @"multi
#addin nuget:?package=InVerbatim&version=1.0.0
line";
var r = """
#addin nuget:?package=InRaw&version=1.0.0
""";
`
	deps := cakeFiles(t, "build.cake", src)["build.cake"]
	type want struct{ depType, name, version, rng, source, skip string }
	wants := []want{
		{DepTypeAddin, "Foo.Foo", "", "", "", skipNoVersion},
		{DepTypeAddin, "Bim.Bim", "6.6.6", "", "", ""},
		{DepTypeTool, "Bar.Bar", "2.2.2", "", "https://example.com", ""},
		{DepTypeTool, "Cake.Git", "2.2.3", "", "https://example.com/feed/v3/", ""},
		{DepTypeTool, "Cake.MinVer", "2.2.4", "", "https://example.com/feed/v3/index.json", ""},
		{DepTypeModule, "Baz.Baz", "3.3.3", "", "", skipCakeSource},
		{DepTypeLoad, "Cake.7zip", "1.0.3", "", "", ""},
		{DepTypeLoad, "Cake.asciidoctorj", "1.0.0", "", "", ""},
		{DepTypeAddin, "Indented", "1.2", "", "", ""},
		{DepTypeAddin, "Escaped", "1.0.0+meta", "", "", skipLocate},
		{DepTypeAddin, "Float", "1", "1.*", "", ""},
		{DepTypeAddin, "All", "*", "", "", skipFloatAll + " *"},
		{DepTypeAddin, "Bad", "latest", "", "", skipInvalid + ` "latest"`},
		{DepTypeAddin, "Empty", "", "", "", skipNoVersion},
		{DepTypeAddin, "BadURL", "1.0.0", "", "", skipCakeSource},
		{DepTypeTool, "Interval", "1.0", "[1.0,2.0)", "", ""},
		{DepTypeAddin, "AfterChar", "1.0.0", "", "", ""},
	}
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.DepType != w.depType || d.Name != w.name || d.Version != w.version || d.Range != w.rng || d.Source != w.source || d.Skip != w.skip {
			t.Errorf("dep %d = %+v, want %+v", i, d, w)
		}
		if d.Skip == "" {
			want := w.version
			if w.rng != "" {
				want = w.rng
			}
			if got := src[d.RefSpan.Start:d.RefSpan.End]; got != want {
				t.Errorf("%s: span reads %q, want %q", d.Name, got, want)
			}
		}
	}
	if d := deps[len(deps)-1]; d.Line != 33 {
		t.Errorf("AfterChar line = %d, want 33", d.Line)
	}
}

func TestCakeInstallTools(t *testing.T) {
	// Renovate's cake specs: a .NET single file build (Cake.Sdk) installing tools.
	src := `#:sdk Cake.Sdk

// Install single tool
InstallTool("dotnet:https://api.nuget.org/v3/index.json?package=SingleTool.Install.First&version=1.0.0");
InstallTool("dotnet:?package=SingleTool.Install.Second&version=1.2.0");

// Install multiple tools at once
InstallTools(
    "dotnet:https://api.nuget.org/v3/index.json?package=MultipleTools.Install.First&version=2.0.0",
    "dotnet:?package=MultipleTools.Install.Second&version=2.1.1" // a comment
    /* "dotnet:?package=Commented&version=1.0.0" */
);

InstallTools(
  "dotnet:bad uri",
  "dotnet:?package=Good.Tool&version=1.2.3",
  @"dotnet:?package=Verbatim&version=1.0.0",
  "dotnet:?package=Escaped&version=1.0.0\u0030"
);
context.InstallTool(new Uri("nuget:?package=Nested&version=3.0.0"));
var installTools = "dotnet:?Should.Not.Match&version=1.0.0";
NotInstallTool("dotnet:?package=Other&version=1.0.0");
InstallTool;
InstallTool(
`
	deps := cakeFiles(t, "build.cake", src)["build.cake"]
	type want struct {
		name, version, source string
		line                  int
	}
	wants := []want{
		{"SingleTool.Install.First", "1.0.0", "https://api.nuget.org/v3/index.json", 4},
		{"SingleTool.Install.Second", "1.2.0", "", 5},
		{"MultipleTools.Install.First", "2.0.0", "https://api.nuget.org/v3/index.json", 9},
		{"MultipleTools.Install.Second", "2.1.1", "", 10},
		{"Good.Tool", "1.2.3", "", 16},
		{"Nested", "3.0.0", "", 20},
	}
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.DepType != DepTypeTool || d.Name != w.name || d.Version != w.version || d.Source != w.source || d.Line != w.line || d.Skip != "" {
			t.Errorf("dep %d = %+v, want %+v", i, d, w)
		}
		if got := src[d.RefSpan.Start:d.RefSpan.End]; got != w.version {
			t.Errorf("%s: span reads %q", d.Name, got)
		}
	}
}

func TestCakeNuGetConfig(t *testing.T) {
	config := `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="nuget.org" value="https://api.nuget.org/v3/index.json" />
    <add key="cake-contrib" value="https://pkgs.dev.azure.com/cake-contrib/Home/_packaging/addins/nuget/v3/index.json" />
  </packageSources>
  <packageSourceMapping>
    <packageSource key="nuget.org"><package pattern="*" /></packageSource>
    <packageSource key="cake-contrib"><package pattern="Cake.Recipe" /></packageSource>
  </packageSourceMapping>
</configuration>
`
	local := `<configuration><packageSources><clear /><add key="local" value="./packages" /></packageSources></configuration>`
	got := cakeFiles(t,
		"NuGet.Config", config,
		"build.cake", "#load nuget:?package=Cake.Recipe&version=4.0.0\n#addin nuget:?package=Cake.Git&version=5.0.1\n#addin nuget:https://example.com/v3/index.json?package=Cake.Own&version=1.0.0\n",
		"offline/nuget.config", local,
		"offline/build.cake", "#addin nuget:?package=Cake.Git&version=5.0.1\n",
	)
	deps := got["build.cake"]
	if s := deps[0].Source; s != "cake-contrib=https://pkgs.dev.azure.com/cake-contrib/Home/_packaging/addins/nuget/v3/index.json" {
		t.Errorf("Cake.Recipe source = %q", s)
	}
	if s := deps[1].Source; s != "" {
		t.Errorf("Cake.Git source = %q, want nuget.org", s)
	}
	if s := deps[2].Source; s != "https://example.com/v3/index.json" {
		t.Errorf("Cake.Own source = %q, want its own", s)
	}
	if d := got["offline/build.cake"][0]; d.Skip != skipNoSources {
		t.Errorf("offline: %+v", d)
	}
	m := NewCake()
	if _, err := m.Extract("nuget.config", []byte("<configuration>"), manager.Env{}); err == nil {
		t.Error("broken nuget.config: no error")
	}
}

func TestCakeEdits(t *testing.T) {
	src := "#addin nuget:?package=Cake.Git&version=5.0.1&loaddependencies=true\n#tool dotnet:?package=Tool&version=[1.0,2.0)\n"
	deps := cakeFiles(t, "build.cake", src)["build.cake"]
	m := NewCake()
	for _, tt := range []struct {
		d    dep.Dep
		v    string
		want string
	}{
		{deps[0], "5.1.0", "#addin nuget:?package=Cake.Git&version=5.1.0&loaddependencies=true\n"},
		{deps[1], "2.5.1", "#tool dotnet:?package=Tool&version=[1.0,3.0)\n"},
	} {
		edits, err := m.Edits(dep.Update{Dep: tt.d, NewVersion: tt.v})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil {
			t.Fatal(err)
		}
		if line := string(out[lineStart(out, tt.d.Line) : lineEnd(out, lineStart(out, tt.d.Line))+1]); line != tt.want {
			t.Errorf("%s: %q, want %q", tt.d.Name, line, tt.want)
		}
	}
	if r, ok := m.(manager.RangeUpdater); !ok || !r.InRange(deps[1], "1.5.0") {
		t.Error("cake is no RangeUpdater admitting 1.5.0 in [1.0,2.0)")
	}
	if _, err := m.Edits(dep.Update{Dep: deps[1], NewVersion: "x"}); err == nil {
		t.Error("unwritable range: no error")
	}
}

// lineStart returns the offset of the 1-based line in src.
func lineStart(src []byte, line int) int {
	off := 0
	for ; line > 1; line-- {
		off = lineEnd(src, off) + 1
	}
	return off
}
