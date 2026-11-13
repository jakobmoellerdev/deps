package paket

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// testdata holds paket.dependencies and paket.lock of real repositories, verbatim (fetched
// 2026-11-09):
//   - paket: fsprojects/Paket (`>=`, `~>`, `<` constraints with options, `#` comments, groups)
//   - fsharpdata: fsprojects/FSharp.Data (pinned versions aligned with spaces, `github` lines,
//     the same package in several groups)
//   - ionide: ionide/ionide-vscode-fsharp (packages without constraint, `git`/`github` lines)

// extract extracts and reads the locks of testdata/<dir>/paket.dependencies.
func extract(t *testing.T, dir string) ([]dep.Dep, []byte) {
	t.Helper()
	file := dir + "/paket.dependencies"
	src, err := os.ReadFile(filepath.Join("testdata", dir, "paket.dependencies"))
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	if !m.Match(file) {
		t.Fatalf("%s does not match", file)
	}
	deps, err := m.Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if errs := m.(manager.LockReader).ReadLocks("testdata", [][]dep.Dep{deps}); len(errs) > 0 {
		t.Fatal(errs)
	}
	return deps, src
}

// find returns the dep named name at line.
func find(t *testing.T, deps []dep.Dep, name string, line int) dep.Dep {
	t.Helper()
	for _, d := range deps {
		if d.Name == name && d.Line == line {
			return d
		}
	}
	t.Fatalf("no dep %s at line %d", name, line)
	return dep.Dep{}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"paket.dependencies": true, "src/paket.dependencies": true, "paket.lock": false, "paket.references": false,
		"src/App/paket.references": false, "Paket.Dependencies": false, "my.paket.dependencies": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
	if m.Name() != Name {
		t.Errorf("Name = %q", m.Name())
	}
}

func TestExtractFixtures(t *testing.T) {
	type want struct {
		name                       string
		line                       int
		version, rng, locked, span string
		fixed                      bool
	}
	for _, tt := range []struct {
		dir   string
		count int
		want  []want
	}{
		{"paket", 24, []want{
			{"Newtonsoft.Json", 9, "10.0.3", ">= 10.0.3", "13.0.4", ">= 10.0.3", false},
			{"Mono.Cecil", 14, "0.11.1", "~> 0.11.1", "0.11.6", "~> 0.11.1", false},
			{"Nuget.Packaging", 19, "7.9", unconstrained, "7.9", "", true}, // NuGet.Packaging in the lock
			{"FSharp.Compiler.Service", 31, "43.9.303", "< 43.10", "43.9.303", "< 43.10", false},
			{"Fake.Core.Target", 52, "6.2.0", "", "6.2", "6.2.0", false}, // group BuildScript
		}},
		{"fsharpdata", 40, []want{
			{"FSharp.Core", 14, "6.0.1", ">= 6.0.1", "6.0.1", ">= 6.0.1", false},
			{"Microsoft.SourceLink.GitHub", 15, "1.0", "", "1.0", "1.0", false},
			{"Fake.Core.Target", 34, "6.1.4", "", "6.1.4", "6.1.4", false},
			{"FsUnit", 56, "4.2.0", "", "4.2", "4.2.0", false},
			{"NUnit3TestAdapter", 55, "6.3", unconstrained, "6.3", "", true},
			{"OpenTelemetry.Api", 59, "1.15.1", ">= 1.15.1", "1.18", ">= 1.15.1", false},
		}},
		{"ionide", 7, []want{
			{"Fable.Core", 5, "5.6", unconstrained, "5.6", "", true},
			{"fsautocomplete", 24, "0.86", unconstrained, "0.86", "", true}, // group fsac
		}},
	} {
		t.Run(tt.dir, func(t *testing.T) {
			deps, src := extract(t, tt.dir)
			if len(deps) != tt.count {
				t.Errorf("got %d deps, want %d", len(deps), tt.count)
			}
			for _, d := range deps {
				if d.Manager != Name || d.Datasource != Datasource || d.Versioning != Versioning || d.DepType != DepType || d.Source != "" || d.Skip != "" {
					t.Errorf("%s: %+v", d.Name, d)
				}
			}
			for _, w := range tt.want {
				d := find(t, deps, w.name, w.line)
				if d.Version != w.version || d.Range != w.rng || d.Locked != w.locked || d.RangeFixed != w.fixed {
					t.Errorf("%s = version %q range %q locked %q fixed %v, want %+v", d.Name, d.Version, d.Range, d.Locked, d.RangeFixed, w)
				}
				if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != w.span {
					t.Errorf("%s: span reads %q, want %q", d.Name, got, w.span)
				}
			}
		})
	}
}

func TestExtract(t *testing.T) {
	src := `source https://www.nuget.org/api/v2
source https://example.com/feed/index.json
// comment
# comment
nuget A
nuget B = 1.2.3 // pinned
nuget C == 1.2.3
nuget D >= 1.2.3 < 1.5 framework: net45
nuget E ~> 1.2 >= 1.2.3
nuget F !~> 1.2
nuget G >= 2 beta rc content: none
nuget H >=1.0
nuget I latest
nuget J >= x
nuget K beta
clitool dotnet-fable
github forki/FsUnit FsUnit.fs
nuget

group Local
  source ./packages
  source %FEED%
  nuget L 1.0

group Default
  nuget M 1.0
`
	deps, err := New().Extract("paket.dependencies", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	type want struct{ name, version, rng, source, skip string }
	feeds := "https://api.nuget.org/v3/index.json https://example.com/feed/index.json"
	wants := []want{
		{"A", "", unconstrained, feeds, ""},
		{"B", "1.2.3", "", feeds, ""},
		{"C", "1.2.3", "", feeds, ""},
		{"D", "1.2.3", ">= 1.2.3 < 1.5", feeds, ""},
		{"E", "1.2.3", "~> 1.2 >= 1.2.3", feeds, ""},
		{"F", "1.2", "!~> 1.2", feeds, ""},
		{"G", "2", ">= 2 beta rc", feeds, ""},
		{"H", "1.0", ">=1.0", feeds, ""},
		{"I", "", unconstrained, feeds, ""}, // `latest` is no constraint token
		{"J", ">= x", "", feeds, skipConstraint},
		{"K", "", unconstrained, feeds, ""},
		{"L", "1.0", "", "", skipNoSources},
		{"M", "1.0", "", "", ""},
	}
	if len(deps) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(wants), deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.Name != w.name || d.Version != w.version || d.Range != w.rng || d.Source != w.source || d.Skip != w.skip {
			t.Errorf("dep %d = %+v, want %+v", i, d, w)
		}
	}
	if d := deps[1]; src[d.RefSpan.Start:d.RefSpan.End] != "1.2.3" {
		t.Errorf("B span reads %q", src[d.RefSpan.Start:d.RefSpan.End])
	}
}

func TestGroupSource(t *testing.T) {
	for _, tt := range []struct {
		sources      []string
		source, skip string
	}{
		{nil, "", ""},
		{[]string{"https://api.nuget.org/v3/index.json"}, "", ""},
		{[]string{"https://nuget.org/api/v2/"}, "", ""},
		{[]string{"http://www.nuget.org/api/v2"}, "", ""},
		{[]string{"https://pkgs.dev.azure.com/x/_packaging/y/nuget/v3/index.json", "C:\\nugets"}, "https://pkgs.dev.azure.com/x/_packaging/y/nuget/v3/index.json", ""},
		{[]string{"~/nugets", "%FEED_URL%"}, "", skipNoSources},
	} {
		if s, skip := groupSource(tt.sources); s != tt.source || skip != tt.skip {
			t.Errorf("groupSource(%q) = %q, %q", tt.sources, s, skip)
		}
	}
}

func TestReadLocks(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "nuget A\nnuget B ~> 1.0\n\ngroup Build\n  nuget A\n  nuget C\n"
	write("paket.dependencies", manifest)
	write("paket.lock", "NUGET\r\n  remote: https://api.nuget.org/v3/index.json\r\n    a (1.0)\r\n      B (>= 1.0)\r\n    B (1.5)\r\nGITHUB\r\n  remote: forki/FsUnit\r\n    FsUnit.fs (fa4eb37)\r\nGROUP Build\r\nNUGET\r\n  remote: https://api.nuget.org/v3/index.json\r\n    A (2.0)\r\n    C (bad)\r\n")
	write("nolock/paket.dependencies", "nuget A\nnuget B 1.0\n")
	m := New()
	var files [][]dep.Dep
	for _, f := range []string{"paket.dependencies", "nolock/paket.dependencies"} {
		src, _ := os.ReadFile(filepath.Join(root, f))
		deps, err := m.Extract(f, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, deps)
	}
	files = append(files, nil)
	if errs := m.(manager.LockReader).ReadLocks(root, files); len(errs) > 0 {
		t.Fatal(errs)
	}
	for _, tt := range []struct {
		d                     dep.Dep
		version, locked, skip string
	}{
		{files[0][0], "1.0", "1.0", ""},
		{files[0][1], "1.0", "1.5", ""},
		{files[0][2], "2.0", "2.0", ""},
		{files[0][3], "", "", skipNoLock},
		{files[1][0], "", "", skipNoLock},
		{files[1][1], "1.0", "", ""},
	} {
		if tt.d.Version != tt.version || tt.d.Locked != tt.locked || tt.d.Skip != tt.skip {
			t.Errorf("%s:%d = %q locked %q skip %q, want %+v", tt.d.Name, tt.d.Line, tt.d.Version, tt.d.Locked, tt.d.Skip, tt)
		}
	}
	roots := m.(manager.LockMaintainer).LockRoots(root, []string{"paket.dependencies", "nolock/paket.dependencies"})
	if len(roots) != 1 || roots[0] != (manager.LockRoot{Manifest: "paket.dependencies", Lockfile: "paket.lock"}) {
		t.Errorf("LockRoots = %+v", roots)
	}
	// an unreadable lock (a directory) is an error
	write("dirlock/paket.dependencies", "nuget A\n")
	if err := os.Mkdir(filepath.Join(root, "dirlock", "paket.lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	if errs := m.(manager.LockReader).ReadLocks(root, [][]dep.Dep{{{Name: "A", File: "dirlock/paket.dependencies", Line: 1, Range: unconstrained}}}); len(errs) != 1 {
		t.Errorf("errs = %v", errs)
	}
}

func TestAdmits(t *testing.T) {
	for _, tt := range []struct {
		constraint string
		yes, no    []string
	}{
		{"1.2.3", []string{"1.2.3", "1.2.3.0"}, []string{"1.2.4", "1.2.2"}},
		{"== 1.2.3", []string{"1.2.3"}, []string{"1.2.4"}},
		{">= 1.2.3 < 1.5", []string{"1.2.3", "1.4.9"}, []string{"1.5", "1.2.2", "1.4.0-beta"}},
		{"> 1.2 <= 2.0", []string{"1.2.1", "2.0"}, []string{"1.2", "2.0.1"}},
		{"~> 0", []string{"0.5"}, []string{"1.0"}},
		{"~> 1.2", []string{"1.2", "1.9.9"}, []string{"2.0", "1.1"}},
		{"~> 1.2.3", []string{"1.2.9"}, []string{"1.3"}},
		{"~> 1.2.3.4", []string{"1.2.3.9"}, []string{"1.2.4"}},
		{"~> 1.2 >= 1.2.3", []string{"1.2.3", "1.9"}, []string{"1.2.2", "2.0"}},
		{">= 2 beta rc", []string{"2.1-beta.1", "2.1-RC1", "2.1"}, []string{"2.1-alpha"}},
		{">= 3 prerelease", []string{"3.1-alpha"}, nil},
		{"~> 1.2.3-alpha001", []string{"1.2.3-alpha002", "1.2.3-beta"}, []string{"1.2.4-alpha"}},
		{"!>= 1.0", []string{"1.0"}, []string{"0.9", "x"}},
	} {
		c, err := parseConstraint(tt.constraint)
		if err != nil {
			t.Fatalf("%s: %v", tt.constraint, err)
		}
		for _, v := range tt.yes {
			if !c.admits(v) {
				t.Errorf("%s does not admit %s", tt.constraint, v)
			}
		}
		for _, v := range tt.no {
			if c.admits(v) {
				t.Errorf("%s admits %s", tt.constraint, v)
			}
		}
	}
	for _, bad := range []string{"", ">=", "latest", "beta", ">= 1 beta < 2", ">= x", "~= 1.0", "1.0 a:b"} {
		if _, err := parseConstraint(bad); err == nil {
			t.Errorf("parseConstraint(%q): no error", bad)
		}
	}
}

func TestNewRange(t *testing.T) {
	m := New().(*paketManager)
	for _, tt := range []struct{ rng, v, strategy, want string }{
		{"~> 1.2", "2.1.0", "", "~> 2.1"},
		{"~> 1.2", "2.1.0", dep.RangeWiden, ">= 1.2 < 3"},
		{"~> 1.2.3", "1.4.1", "", "~> 1.4.1"},
		{"~> 1.2", "1.5.0", "", "~> 1.5"}, // admitted: written under bump only
		{"~> 1.2 >= 1.2.3", "2.1.0", "", "~> 2.1"},
		{"!~> 1.2 >= 1.2.3", "2.1.0", "", "!~> 2.1"},
		{"~> 1.2 >= 1.2.3", "1.5.0", "", "~> 1.5 >= 1.5.0"},
		{">= 1.2.3 < 2.0", "2.1.4", "", ">= 1.2.3 < 2.2"},
		{">= 1.2.3 < 2", "2.1.4", "", ">= 1.2.3 < 3"},
		{"<= 2.0", "2.1.4", "", "<= 2.1.4"},
		{">= 1.0", "2.0", "", ">= 2.0"},
		{"> 1.0", "2.0", "", ""}, // admitted, no >= floor to raise
		{"< 2.0", "1.5", "", ""},
		{">= 2.0", "1.0", "", ""}, // below the floor
		{">= 2 beta", "3.0-rc1", "", ""},
		{"~> 1.2", "2.0", dep.RangePin, "2.0"},
		{"= 2.0", "2.0", dep.RangePin, ""},
		{"~> 1.2", "x", "", ""},
		{"bad", "1.0", "", ""},
	} {
		got, ok := m.NewRange(dep.Dep{Range: tt.rng}, tt.v, tt.strategy)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("NewRange(%q, %s, %q) = %q, %v; want %q", tt.rng, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
	if _, ok := m.NewRange(dep.Dep{Range: unconstrained, RangeFixed: true}, "2.0", dep.RangePin); ok {
		t.Error("an unconstrained package was rewritten")
	}
	if !m.InRange(dep.Dep{Range: "~> 1.2"}, "1.9") || m.InRange(dep.Dep{Range: "bad"}, "1.0") {
		t.Error("InRange")
	}
}

func TestEdits(t *testing.T) {
	src := "nuget A    1.2.3 copy_local: true\nnuget B ~> 1.2 redirects: force\n"
	deps, err := New().Extract("paket.dependencies", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	for _, tt := range []struct {
		d       dep.Dep
		v, want string
	}{
		{deps[0], "1.3.0", "nuget A    1.3.0 copy_local: true\nnuget B ~> 1.2 redirects: force\n"},
		{deps[1], "2.1.0", "nuget A    1.2.3 copy_local: true\nnuget B ~> 2.1 redirects: force\n"},
	} {
		edits, err := m.Edits(dep.Update{Dep: tt.d, NewVersion: tt.v})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != tt.want {
			t.Errorf("%s: %q", tt.d.Name, out)
		}
	}
	if _, err := m.Edits(dep.Update{Dep: deps[1], NewVersion: "1.0"}); err == nil {
		t.Error("unwritable constraint: no error")
	}
}

// fakePaket puts a paket script on PATH that appends its arguments to <dir>/args and writes
// the lock content to paket.lock (or fails when lock is "fail").
func fakePaket(t *testing.T, lock string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "args")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n"
	if lock == "fail" {
		script += "echo \"$PWD: resolution failed\" >&2\nexit 1\n"
	} else {
		script += "printf '%s' '" + lock + "' > paket.lock\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "paket"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return log
}

func TestLocker(t *testing.T) {
	dir := t.TempDir()
	manifest := "nuget A ~> 1.0\n\ngroup Build\n  nuget B 1.0\n"
	if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lockFile), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l := Locker{}
	if scope, ok := l.Scope(ctx, filepath.Dir(dir), filepath.Base(dir)+"/"+manifestFile); !ok || scope != filepath.Base(dir) {
		t.Errorf("Scope = %q, %v", scope, ok)
	}
	if _, ok := l.Scope(ctx, dir, "x/"+manifestFile); ok {
		t.Error("Scope without paket.lock")
	}
	if l.Siblings()[0] != lockFile {
		t.Error("Siblings")
	}
	if out, err := l.Process(ctx, dir, nil, nil); out != nil || err != nil {
		t.Errorf("Process = %v, %v", out, err)
	}
	log := fakePaket(t, "new")
	ups := map[string][]dep.Update{manifestFile: {{Dep: dep.Dep{Name: "B", Line: 4}, NewVersion: "1.1"}}}
	locks := map[string][]dep.Update{manifestFile: {{Dep: dep.Dep{Name: "A", Line: 1}, NewVersion: "1.5"}, {Dep: dep.Dep{Name: "a", Line: 1}, NewVersion: "1.5"}}}
	files := map[string][]byte{lockFile: []byte("old")}
	out, err := l.ProcessUpdates(ctx, dir, map[string][]byte{manifestFile: []byte(manifest)}, files, ups, locks)
	if err != nil || string(out[lockFile]) != "new" {
		t.Fatalf("ProcessUpdates = %q, %v", out, err)
	}
	args, _ := os.ReadFile(log)
	if want := "update --no-install --group Build --version 1.1 B\nupdate --no-install --group Main --version 1.5 A\n"; string(args) != want {
		t.Errorf("paket ran %q, want %q", args, want)
	}
	if out, err := l.ProcessUpdates(ctx, dir, nil, files, nil, nil); out != nil || err != nil {
		t.Errorf("no updates: %v, %v", out, err)
	}
	if _, err := l.ProcessUpdates(ctx, dir, nil, nil, ups, nil); err == nil {
		t.Error("no paket.lock: no error")
	}
	if _, err := l.ProcessUpdates(ctx, dir, nil, files, map[string][]dep.Update{manifestFile: {{Dep: dep.Dep{Name: "Z", Line: 2}, NewVersion: "1.0"}}}, nil); err == nil {
		t.Error("an update of no nuget line: no error")
	}
	if out, err := l.Maintain(ctx, dir, files); err != nil || string(out[lockFile]) != "new" {
		t.Errorf("Maintain = %q, %v", out, err)
	}
	if out, err := l.Maintain(ctx, dir, map[string][]byte{}); out != nil || err != nil {
		t.Errorf("Maintain without lock = %v, %v", out, err)
	}
	fakePaket(t, "old")
	if out, err := l.Maintain(ctx, dir, files); out != nil || err != nil {
		t.Errorf("unchanged lock: %v, %v", out, err)
	}
	fakePaket(t, "fail")
	_, err = l.Maintain(ctx, dir, files)
	if err == nil || !strings.Contains(err.Error(), "paket update in "+dir+": "+dir+": resolution failed") {
		t.Errorf("failure: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := l.Maintain(ctx, dir, files); err == nil || err.Error() != "paket not found (needed to update paket.lock)" {
		t.Errorf("no paket: %v", err)
	}
}

// TestParseLockBlankLine covers a line of exactly four spaces inside a NUGET block, which
// paket writes between the packages of two remotes.
func TestParseLockBlankLine(t *testing.T) {
	src := "NUGET\n  remote: https://api.nuget.org/v3/index.json\n    Newtonsoft.Json (13.0.3)\n    \n    Serilog (3.1.1)\n"
	got := parseLock([]byte(src))
	want := map[string]string{"newtonsoft.json": "13.0.3", "serilog": "3.1.1"}
	for id, v := range want {
		if got[strings.ToLower(mainGroup)][id] != v {
			t.Errorf("parseLock()[%q] = %q, want %q", id, got[strings.ToLower(mainGroup)][id], v)
		}
	}
}
