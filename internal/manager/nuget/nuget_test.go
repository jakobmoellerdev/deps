package nuget

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// testdata holds files of real repositories, verbatim (fetched 2026-11-05):
//   - cleanarchitecture: jasontaylordev/CleanArchitecture (central package management with a
//     UTF-8 BOM in Directory.Build.props and src/Web/Web.csproj, global.json with rollForward)
//   - msbuildsdks: microsoft/MSBuildSdks (PackageVersions from properties in conditional
//     ItemGroups, global.json msbuild-sdks, a NuGet.config with a BOM that clears its sources)
//   - polly: App-vNext/Polly (.config/dotnet-tools.json, packageSourceMapping)

// run extracts files (slash paths below dir) in order and links them, as the engine does.
func run(t *testing.T, dir string, files ...string) map[string][]dep.Dep {
	t.Helper()
	m := New()
	var all [][]dep.Dep
	for _, f := range files {
		if !m.Match(f) {
			t.Fatalf("%s does not match", f)
		}
		src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		deps, err := m.Extract(f, src, manager.Env{})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		all = append(all, deps)
	}
	m.(manager.Linker).Link(all)
	out := map[string][]dep.Dep{}
	for i, f := range files {
		out[f] = all[i]
	}
	return out
}

// find returns the dep named name of type depType.
func find(t *testing.T, deps []dep.Dep, depType, name string) dep.Dep {
	t.Helper()
	for _, d := range deps {
		if d.Name == name && d.DepType == depType {
			return d
		}
	}
	t.Fatalf("no %s dep %s in %v", depType, name, deps)
	return dep.Dep{}
}

// checkSpan fails unless d's RefSpan in the file reads want.
func checkSpan(t *testing.T, dir string, d dep.Dep, want string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(d.File)))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != want {
		t.Errorf("%s: span reads %q, want %q", d.Name, got, want)
	}
}

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		"src/App/App.csproj": true, "Lib.fsproj": true, "x/Old.VBPROJ": true, "Directory.Packages.props": true,
		"Directory.Build.targets": true, "eng/Versions.props": true, "global.json": true, ".config/dotnet-tools.json": true,
		"NuGet.Config": true, "src/nuget.config": true, "obj/App.csproj.nuget.g.props": false, "obj/App.csproj.nuget.g.targets": false,
		"package.json": false, "App.sln": false, "web.config": false, "csproj": false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestCleanArchitecture(t *testing.T) {
	dir := "testdata/cleanarchitecture"
	got := run(t, dir, "Directory.Build.props", "Directory.Packages.props", "global.json", "src/Web/Web.csproj")
	if n := len(got["Directory.Build.props"]) + len(got["src/Web/Web.csproj"]); n != 0 {
		t.Errorf("Directory.Build.props and Web.csproj (central management, unversioned Sdk) have %d deps", n)
	}
	pkgs := got["Directory.Packages.props"]
	if len(pkgs) != 46 {
		t.Errorf("Directory.Packages.props: %d deps, want 46", len(pkgs))
	}
	for _, d := range pkgs {
		if d.Skip != "" || d.Source != "" || d.Datasource != "nuget" || d.Versioning != "nuget" || d.Manager != Name || d.Range != "" {
			t.Errorf("%+v", d)
		}
	}
	ef := find(t, pkgs, DepTypeNuGet, "Microsoft.EntityFrameworkCore")
	if ef.Version != "10.0.14" || ef.Line != 20 {
		t.Errorf("ef = %+v", ef)
	}
	checkSpan(t, dir, ef, "10.0.14")
	sdk := find(t, got["global.json"], DepTypeDotnetSDK, "dotnet-sdk")
	if sdk.Version != "10.0.403" || sdk.Datasource != "dotnet-version" || sdk.Line != 3 {
		t.Errorf("sdk = %+v", sdk)
	}
	checkSpan(t, dir, sdk, "10.0.403")

	e, err := New().Edits(dep.Update{Dep: ef, NewVersion: "10.0.15"})
	if err != nil || len(e) != 1 || e[0] != (patch.Edit{Start: ef.RefSpan.Start, End: ef.RefSpan.End, Old: "10.0.14", New: "10.0.15"}) {
		t.Errorf("Edits = %v, %v", e, err)
	}
}

func TestMSBuildSdks(t *testing.T) {
	dir := "testdata/msbuildsdks"
	got := run(t, dir, "NuGet.config", "Directory.Packages.props", "global.json")
	if len(got["NuGet.config"]) != 0 {
		t.Error("NuGet.config has deps")
	}
	pkgs := got["Directory.Packages.props"]
	var net8 []dep.Dep
	for _, d := range pkgs {
		if d.Skip != "" {
			t.Errorf("skipped: %+v", d)
		}
		if d.VersionRef == "MicrosoftBuildPackageNet8" {
			net8 = append(net8, d)
		}
	}
	if len(net8) != 4 {
		t.Fatalf("deps of $(MicrosoftBuildPackageNet8) = %v", net8)
	}
	for _, d := range net8 {
		if d.Version != "17.11.48" || d.RefSpan != net8[0].RefSpan || d.File != "Directory.Packages.props" {
			t.Errorf("%+v", d)
		}
		checkSpan(t, dir, d, "17.11.48")
	}
	if l := net8[0].Line; l != 13 {
		t.Errorf("line = %d, want the declaration's 13", l)
	}
	nt := find(t, got["global.json"], DepTypeMSBuildSDK, "Microsoft.Build.NoTargets")
	// The NuGet.config clears the sources and adds nuget.org back: the default source.
	if nt.Version != "3.7.134" || nt.Source != "" || nt.Datasource != "nuget" {
		t.Errorf("NoTargets = %+v", nt)
	}
	checkSpan(t, dir, nt, "3.7.134")
	if sdk := find(t, got["global.json"], DepTypeDotnetSDK, "dotnet-sdk"); sdk.Version != "10.0.203" {
		t.Errorf("sdk = %+v", sdk)
	}
}

func TestPolly(t *testing.T) {
	dir := "testdata/polly"
	got := run(t, dir, "NuGet.config", ".config/dotnet-tools.json", "Directory.Packages.props", "global.json")
	tools := got[".config/dotnet-tools.json"]
	var names []string
	for _, d := range tools {
		names = append(names, d.Name+"@"+d.Version)
		if d.DepType != DepTypeNuGet || d.Skip != "" || d.Source != "NuGet=https://api.nuget.org/v3/index.json" && d.Source != "" {
			t.Errorf("%+v", d)
		}
	}
	want := []string{"authenticodelint@0.13.0", "cake.tool@6.3.0", "docfx@2.81.0", "dotnet-reportgenerator-globaltool@5.5.11",
		"dotnet-stryker@5.0.0", "dotnet-validate@0.0.1-preview.582", "markdownsnippets.tool@28.5.1",
		"MartinCostello.WaitForNuGetPackage@1.3.3", "meziantou.framework.nugetpackagevalidation.tool@2.0.18", "sign@0.9.1-beta.26475.3"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v", names)
	}
	checkSpan(t, dir, find(t, tools, DepTypeNuGet, "sign"), "0.9.1-beta.26475.3")
	polly := find(t, got["Directory.Packages.props"], DepTypeNuGet, "Polly")
	if polly.Version != "8.9.0" || polly.Skip != "" {
		t.Errorf("polly = %+v", polly)
	}
	if sdk := find(t, got["global.json"], DepTypeMSBuildSDK, "Cake.Sdk"); sdk.Version != "6.3.0" {
		t.Errorf("Cake.Sdk = %+v", sdk)
	}
}

// write lays files out below a temp dir.
func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const appProj = `<Project Sdk="Microsoft.Build.NoTargets/3.7.0;Microsoft.NET.Sdk">
  <Sdk Name="Microsoft.Build.CentralPackageVersions" Version="2.1.3" />
  <Import Project="Sdk.targets" Sdk="Microsoft.Build.Traversal" Version="4.1.0" />
  <PropertyGroup>
    <SerilogVersion>3.0.0</SerilogVersion>
    <serilogversion>3.1.1</serilogversion>
    <Floating>1.2.*</Floating>
  </PropertyGroup>
  <PropertyGroup Condition="'$(Configuration)' == 'Debug'">
    <XunitVersion>9.9.9</XunitVersion>
  </PropertyGroup>
  <ItemGroup Condition="'$(TargetFramework)' == 'net8.0'">
    <PackageReference Include="Serilog" Version="$(SerilogVersion)" />
    <PackageReference Include="Xunit" Version="$(XunitVersion)" />
    <PackageReference Include="Shared" Version="$(SharedVersion)" />
    <PackageReference Include="Floating" Version="$(Floating)" />
    <PackageReference Include="Moq">
      <Version>[4.20.70]</Version>
    </PackageReference>
    <PackageReference Include="Override" Version="1.0.0" VersionOverride='2.0.0' />
    <PackageReference Include="ChildOverride">
      <VersionOverride> 5.0.0 </VersionOverride>
    </PackageReference>
    <PackageReference Include="Range" Version=" [1.0, 2.0) " />
    <PackageReference Update="Updated" Version="7.0.0" />
    <PackageReference Include="Central" />
    <PackageReference Include="Star" Version="*" />
    <PackageReference Include="Entity" Version="1.0.0&#x2D;beta" />
    <PackageReference Include="Bad" Version="latest" />
    <PackageReference Include="Empty" Version="" />
    <PackageReference Include="Concat" Version="$(Major).1.0" />
    <PackageReference Include="$(Prefix).Core" Version="1.0.0" />
    <PackageReference Include="Loop" Version="$(LoopA)" />
    <DotNetCliToolReference Include="Legacy.Tool" Version="2.0.0" />
  </ItemGroup>
  <Target Name="T">
    <ItemGroup>
      <PackageReference Include="InTarget" Version="1.0.0" />
    </ItemGroup>
  </Target>
</Project>
`

func TestProjectForms(t *testing.T) {
	dir := write(t, map[string]string{
		"Directory.Build.props":        "<Project><PropertyGroup><SharedVersion>6.0.1</SharedVersion><LoopA>$(LoopB)</LoopA><LoopB>$(LoopA)</LoopB></PropertyGroup></Project>",
		"src/Directory.Packages.props": "<Project><PropertyGroup><SharedVersion>6.0.2</SharedVersion></PropertyGroup></Project>",
		"src/App/App.csproj":           appProj,
	})
	got := run(t, dir, "Directory.Build.props", "src/Directory.Packages.props", "src/App/App.csproj")
	deps := got["src/App/App.csproj"]
	type want struct{ version, rng, span, file, ref, skip string }
	for _, tt := range []struct {
		depType, name string
		want
	}{
		{DepTypeMSBuildSDK, "Microsoft.Build.NoTargets", want{version: "3.7.0", span: "3.7.0"}},
		{DepTypeMSBuildSDK, "Microsoft.Build.CentralPackageVersions", want{version: "2.1.3", span: "2.1.3"}},
		{DepTypeMSBuildSDK, "Microsoft.Build.Traversal", want{version: "4.1.0", span: "4.1.0"}},
		// The last definition wins, case-insensitively.
		{DepTypeNuGet, "Serilog", want{version: "3.1.1", span: "3.1.1", ref: "SerilogVersion"}},
		{DepTypeNuGet, "Xunit", want{version: "$(XunitVersion)", skip: "undefined property $(XunitVersion)"}},
		// Directory.Packages.props of the nearer directory wins over Directory.Build.props.
		{DepTypeNuGet, "Shared", want{version: "6.0.2", span: "6.0.2", file: "src/Directory.Packages.props", ref: "SharedVersion"}},
		{DepTypeNuGet, "Floating", want{version: "1.2", rng: "1.2.*", span: "1.2.*", ref: "Floating"}},
		{DepTypeNuGet, "Moq", want{version: "4.20.70", span: "4.20.70"}},
		{DepTypeNuGet, "Override", want{version: "2.0.0", span: "2.0.0"}},
		{DepTypeNuGet, "ChildOverride", want{version: "5.0.0", span: "5.0.0"}},
		{DepTypeNuGet, "Range", want{version: "1.0", rng: "[1.0, 2.0)", span: "[1.0, 2.0)"}},
		{DepTypeNuGet, "Updated", want{version: "7.0.0", span: "7.0.0"}},
		{DepTypeNuGet, "Star", want{version: "*", skip: "floating version *"}},
		{DepTypeNuGet, "Entity", want{version: "1.0.0-beta", skip: "cannot locate value"}},
		{DepTypeNuGet, "Bad", want{version: "latest", skip: `invalid version "latest"`}},
		{DepTypeNuGet, "Empty", want{skip: "no version"}},
		{DepTypeNuGet, "Concat", want{version: "$(Major).1.0", skip: "non-literal version $(Major).1.0"}},
		{DepTypeNuGet, "$(Prefix).Core", want{skip: "non-literal package id"}},
		{DepTypeNuGet, "Loop", want{version: "$(LoopA)", skip: "property cycle $(LoopA)"}},
		{DepTypeNuGet, "Legacy.Tool", want{version: "2.0.0", span: "2.0.0"}},
	} {
		d := find(t, deps, tt.depType, tt.name)
		file := cmpOr(tt.file, "src/App/App.csproj")
		if d.Version != tt.version || d.Range != tt.rng || d.File != file || d.VersionRef != tt.ref || d.Skip != tt.skip {
			t.Errorf("%s: %+v, want %+v", tt.name, d, tt.want)
			continue
		}
		if tt.span != "" {
			checkSpan(t, dir, d, tt.span)
		}
	}
	for _, d := range deps {
		if d.Name == "Central" || d.Name == "InTarget" || d.Name == "Microsoft.NET.Sdk" {
			t.Errorf("unexpected dep %+v", d)
		}
	}
	if n := len(deps); n != 20 {
		t.Errorf("%d deps, want 20", n)
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestSources(t *testing.T) {
	dir := write(t, map[string]string{
		"nuget.config": `<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <add key="company feed" value="https://pkgs.example.com/nuget/v3/index.json" />
    <add key="local" value="./packages" />
    <add key="old" value="https://old.example.com/v3/index.json" />
  </packageSources>
  <disabledPackageSources><add key="old" value="true" /></disabledPackageSources>
</configuration>`,
		"mapped/NuGet.Config": `<configuration>
  <packageSources><remove key="local" /></packageSources>
  <packageSourceMapping>
    <packageSource key="nuget.org"><package pattern="*" /></packageSource>
    <packageSource key="company feed"><package pattern="Company.*" /><package pattern="Exact.Id" /></packageSource>
  </packageSourceMapping>
</configuration>`,
		"mapped/only/nuget.config": `<configuration>
  <packageSources><clear /><add key="company feed" value="https://pkgs.example.com/nuget/v3/index.json" /></packageSources>
</configuration>`,
		"local/nuget.config": `<configuration><packageSources><clear /><add key="l" value="C:\feed" /></packageSources></configuration>`,
		"a.csproj":           `<Project><ItemGroup><PackageReference Include="Any" Version="1.0.0" /></ItemGroup></Project>`,
		"mapped/b.csproj": `<Project><ItemGroup><PackageReference Include="Company.Core" Version="1.0.0" />
<PackageReference Include="exact.id" Version="1.0.0" /><PackageReference Include="Other" Version="1.0.0" /></ItemGroup></Project>`,
		"mapped/only/c.csproj": `<Project><ItemGroup><PackageReference Include="Other" Version="1.0.0" /><PackageReference Include="Company.X" Version="1.0.0" /></ItemGroup></Project>`,
		"local/d.csproj":       `<Project><ItemGroup><PackageReference Include="Other" Version="1.0.0" /></ItemGroup></Project>`,
		"global.json":          "{\n  // the SDK\n  \"sdk\": { \"version\": \"8.0.100\" /* pinned */ },\n  \"msbuild-sdks\": { \"Company.Sdk\": \"1.0.0\" }\n}\n",
	})
	got := run(t, dir, "nuget.config", "mapped/NuGet.Config", "mapped/only/nuget.config", "local/nuget.config",
		"a.csproj", "mapped/b.csproj", "mapped/only/c.csproj", "local/d.csproj", "global.json")
	both := "nuget.org=https://api.nuget.org/v3/index.json company+feed=https://pkgs.example.com/nuget/v3/index.json"
	company := "company+feed=https://pkgs.example.com/nuget/v3/index.json"
	for _, tt := range []struct{ file, name, source, skip string }{
		{"a.csproj", "Any", both, ""},
		{"mapped/b.csproj", "Company.Core", company, ""}, // Company.* is more specific than *
		{"mapped/b.csproj", "exact.id", company, ""},
		{"mapped/b.csproj", "Other", "", ""},
		{"mapped/only/c.csproj", "Other", "", skipUnmapped}, // nuget.org is cleared, Company.* does not match
		{"mapped/only/c.csproj", "Company.X", company, ""},
		{"local/d.csproj", "Other", "", skipNoSources},
		{"global.json", "Company.Sdk", both, ""},
	} {
		depType := DepTypeNuGet
		if tt.file == "global.json" {
			depType = DepTypeMSBuildSDK
		}
		d := find(t, got[tt.file], depType, tt.name)
		if d.Source != tt.source || d.Skip != tt.skip {
			t.Errorf("%s %s: source %q skip %q, want %q %q", tt.file, tt.name, d.Source, d.Skip, tt.source, tt.skip)
		}
	}
	sdk := find(t, got["global.json"], DepTypeDotnetSDK, "dotnet-sdk")
	if sdk.Source != "" || sdk.Version != "8.0.100" || sdk.Line != 3 {
		t.Errorf("sdk = %+v", sdk)
	}
	checkSpan(t, dir, sdk, "8.0.100")
}

func TestExtractErrors(t *testing.T) {
	m := New()
	for file, src := range map[string]string{
		"a.csproj":          "<Project><ItemGroup>",
		"b.csproj":          "<Project></ItemGroup>",
		"c.csproj":          "not xml <",
		"d.csproj":          `<?xml version="1.0" encoding="utf-16"?><Project/>`,
		"global.json":       `{"sdk": {"version": "8.0.100"}`,
		"dotnet-tools.json": `{"tools": [}`,
		"nuget.config":      "<configuration>",
	} {
		if _, err := m.Extract(file, []byte(src), manager.Env{}); err == nil {
			t.Errorf("%s: no error", file)
		}
	}
	// Another root element is no MSBuild file.
	if deps, err := m.Extract("x.props", []byte(`<Other><PackageReference Include="A" Version="1.0" /></Other>`), manager.Env{}); err != nil || deps != nil {
		t.Errorf("non-Project root: %v, %v", deps, err)
	}
	if deps, err := m.Extract("global.json", []byte(`["x", {"sdk": 1}]`), manager.Env{}); err != nil || deps != nil {
		t.Errorf("odd global.json: %v, %v", deps, err)
	}
}

func TestNewRange(t *testing.T) {
	m := New().(*nugetManager)
	for _, tt := range []struct{ rng, v, strategy, want string }{
		{"1.*", "2.1.0", "", "2.*"},
		{"1.*", "1.5.0", "", ""}, // admitted
		{"1.2.*", "1.4.3", "", "1.4.*"},
		{"1.2.*", "2.0", "", "2.0.*"},
		{"1.*-*", "2.0.0-rc.1", "", "2.*-*"},
		{"1.0.0-rc*", "1.1.0-rc.2", "", "1.1.0-rc*"},
		{"1.0.0-rc*", "1.1.0", "", ""}, // a release is no -rc* prerelease
		{"1.0.0-*", "1.0.1", "", "1.0.1-*"},
		{"[1.0,2.0)", "2.5.1", "", "[1.0,3.0)"},
		{"[1.0, 2.0)", "2.5.1", "replace", "[1.0, 3.0)"},
		{"[1.0,1.5)", "1.7.2", "", "[1.0,1.8)"},
		{"[1.0,2.0]", "2.5.1", "", "[1.0,2.5.1]"},
		{"(,2.0)", "2.0.0", "", "(,3.0)"},
		{"(,1.2.0)", "1.2.5", "", "(,1.3.0)"},
		{"[1.0,2.0)", "1.5.0", "bump", "[1.5.0,2.0)"},
		{"[1.0,2.0)", "1.0", "bump", ""}, // the floor already
		{"(,2.0)", "1.5.0", "bump", ""},  // no floor to raise
		{"[1.0,)", "3.0", "", "[3.0,)"},  // admitted: the floor is raised (written under bump only)
		{"[2.0,3.0)", "1.0", "", ""},     // below
		{"[1.0,2.0)", "1.5.0-rc1", "", ""},
		{"[1.0,2.0)", "3.1.0", dep.RangePin, "3.1.0"},
		{"1.*", "2.0.0", dep.RangeWiden, "2.*"},
		{"bad", "1.0", "", ""},
		{"1.*", "x", "", ""},
	} {
		got, ok := m.NewRange(dep.Dep{Range: tt.rng}, tt.v, tt.strategy)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("NewRange(%s, %s, %q) = %q, %v, want %q", tt.rng, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
	if !m.InRange(dep.Dep{Range: "[1.0,2.0)"}, "1.5") || m.InRange(dep.Dep{Range: "[1.0,2.0)"}, "2.0") || m.InRange(dep.Dep{Range: "x"}, "1") {
		t.Error("InRange")
	}

	d := dep.Dep{Range: "1.2.*", Version: "1.2", RefSpan: dep.Span{Start: 10, End: 15}}
	e, err := m.Edits(dep.Update{Dep: d, NewVersion: "1.3.0"})
	if err != nil || len(e) != 1 || e[0] != (patch.Edit{Start: 10, End: 15, Old: "1.2.*", New: "1.3.*"}) {
		t.Errorf("Edits = %v, %v", e, err)
	}
	if _, err := m.Edits(dep.Update{Dep: d, NewVersion: "1.2.5"}); err == nil || !strings.Contains(err.Error(), "cannot be rewritten") {
		t.Errorf("admitted Edits: %v", err)
	}
}

// A closed interval with equal bounds (`[1.0,1.0]`) is an interval, not the exact form `[1.0]`:
// editing only its lower bound would write `[2.0,1.0]`.
func TestClosedIntervalEdit(t *testing.T) {
	src := `<Project><ItemGroup><PackageReference Include="X" Version="[1.0,1.0]" /></ItemGroup></Project>`
	dir := write(t, map[string]string{"App.csproj": src})
	d := find(t, run(t, dir, "App.csproj")["App.csproj"], DepTypeNuGet, "X")
	e, err := New().Edits(dep.Update{Dep: d, NewVersion: "2.0"})
	if err != nil || len(e) != 1 {
		t.Fatalf("Edits = %v, %v", e, err)
	}
	got := src[:e[0].Start] + e[0].New + src[e[0].End:]
	if !strings.Contains(got, `Version="[1.0,2.0]"`) {
		t.Errorf("edited to %s", got)
	}
}

func TestFloatAnchor(t *testing.T) {
	for in, want := range map[string]string{"1.*": "1", "1.2.*": "1.2", "1.0.0-rc*": "1.0.0-rc", "1.*-*": "1-0", "1.0.0-rc.*": "1.0.0-rc"} {
		d := dep.Dep{}
		setVersion(&d, in, dep.Span{Start: 0, End: len(in)})
		if d.Version != want || d.Range != in {
			t.Errorf("setVersion(%s) = %+v, want version %s", in, d, want)
		}
	}
	_ = path.Base
}
