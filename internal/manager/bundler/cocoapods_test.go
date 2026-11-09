package bundler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// describePod renders the fields of d that the cocoapods manager's Extract sets; RefSpan must
// point at Range, or at Version for a git pod.
func describePod(src []byte, d dep.Dep) string {
	want := d.Range
	if d.Datasource == GitDatasource {
		want = d.Version
	}
	if ref := d.RefSpan; ref != (dep.Span{}) && string(src[ref.Start:ref.End]) != want {
		return fmt.Sprintf("%d %s: RefSpan %q is not %q", d.Line, d.Name, src[ref.Start:ref.End], want)
	}
	s := fmt.Sprintf("%d %s %s range=%s constraint=%q version=%q source=%s", d.Line, d.Name, d.Datasource, d.Range, d.Constraint, d.Version, d.Source)
	if d.Skip != "" {
		s += " skip=" + d.Skip
	}
	return s
}

func extractPods(t *testing.T, file string, src []byte) []string {
	t.Helper()
	deps, err := NewCocoaPods().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(deps))
	for i, d := range deps {
		if d.Manager != NameCocoaPods || d.DepType != DepTypePods || d.File != file {
			t.Errorf("%s: manager %s, dep type %s, file %s", d.Name, d.Manager, d.DepType, d.File)
		}
		out[i] = describePod(src, d)
	}
	return out
}

func TestCocoaPodsMatch(t *testing.T) {
	m := NewCocoaPods()
	for p, want := range map[string]bool{
		"Podfile": true, "ios/Podfile": true, "Podfile.lock": false, "MyPodfile": false, "Podfile/x": false, "a/Podfile.rb": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%s) = %v", p, !want)
		}
	}
	if m.Name() != NameCocoaPods {
		t.Error(m.Name())
	}
}

// testdata/cocoapods holds Podfiles of StepicOrg/stepik-ios, sora-xor/sora-ios (with their
// Podfile.lock files) and mattermost/mattermost-mobile (ios/Podfile), verbatim, 2026-11-09.
func TestCocoaPodsExtractRealWorld(t *testing.T) {
	for dir := range map[string]bool{"stepik": true, "sora": true, "mattermost": true} {
		t.Run(dir, func(t *testing.T) {
			file := filepath.Join("testdata", "cocoapods", dir, "Podfile")
			got := strings.Join(extractPods(t, file, read(t, file)), "\n")
			golden := filepath.Join("testdata", "cocoapods", dir, "deps.golden")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(golden, []byte(got+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if want := strings.TrimSuffix(string(read(t, golden)), "\n"); got != want {
				t.Errorf("deps:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestCocoaPodsExtractForms(t *testing.T) {
	src := `source 'https://cdn.cocoapods.org/'
source "https://github.com/org/specs.git"
platform :ios, '13.0'

target 'App' do
  pod('Paren', '~> 1.2')
  pod 'Multi', '> 0.1', '< 0.3'
  pod 'Array', ['>= 2.0', '< 3']
  pod 'Own', '1.0.0', :source => 'https://github.com/other/specs.git'
  pod 'Label', '2.0', source: 'https://example.com/cdn/'
  pod 'Head', :head
  pod 'Var', version
  pod 'Bad', '~> v1'
  pod 'Upper', '< 2'
  pod 'Sub/Spec', '~> 3.0', :configurations => ['Debug'], :modular_headers => true
  pod 'Git', git: 'https://github.com/o/git.git', tag: 'v1.2.3'
  pod 'Scp', :git => 'git@gitlab.com:o/scp.git', :tag => '0.1'
  pod 'Commit', :git => 'https://github.com/o/c.git', :commit => 'abc'
  pod 'GitTagVar', :git => 'https://github.com/o/v.git', :tag => tag
  pod 'NoTag', :git => 'https://github.com/o/n.git'
  pod 'FileGit', :git => 'file:///tmp/repo', :tag => '1.0'
  pod 'Path', :path => '../path'
  pod 'Spec', :podspec => 'https://example.com/x.podspec'
  pod 'Http', :http => 'https://example.com/x.zip'
  pod 'SrcVar', '1.0', :source => ENV['SPECS']
  pod 'Bare'
  pod name
end

post_install do |installer|
  installer.pods_project.targets.each do |target|
    source "not a source", "x"
  end
end
`
	global := "source=https://cdn.cocoapods.org https://github.com/org/specs.git"
	want := []string{
		`6 Paren pod range='~> 1.2' constraint="~> 1.2" version="1.2" ` + global,
		`7 Multi pod range='> 0.1', '< 0.3' constraint="> 0.1, < 0.3" version="0.1" ` + global,
		`8 Array pod range='>= 2.0', '< 3' constraint=">= 2.0, < 3" version="2.0" ` + global,
		`9 Own pod range='1.0.0' constraint="1.0.0" version="1.0.0" source=https://github.com/other/specs.git`,
		`10 Label pod range='2.0' constraint="2.0" version="2.0" source=https://example.com/cdn`,
		`11 Head pod range= constraint="" version="" ` + global + ` skip=` + skipNonLiteral,
		`12 Var pod range= constraint="" version="" ` + global + ` skip=` + skipNonLiteral,
		`13 Bad pod range= constraint="" version="~> v1" ` + global + ` skip=` + skipUnsupported,
		`14 Upper pod range='< 2' constraint="< 2" version="" ` + global,
		`15 Sub/Spec pod range='~> 3.0' constraint="~> 3.0" version="3.0" ` + global,
		`16 Git git-tags range= constraint="" version="v1.2.3" source=https://github.com/o/git`,
		`17 Scp git-tags range= constraint="" version="0.1" source=https://gitlab.com/o/scp`,
		`18 Commit git-tags range= constraint="" version="" ` + global + ` skip=` + skipGit,
		`19 GitTagVar git-tags range= constraint="" version="" ` + global + ` skip=` + skipGit,
		`20 NoTag git-tags range= constraint="" version="" ` + global + ` skip=` + skipGit,
		`21 FileGit git-tags range= constraint="" version="" source= skip=` + skipGitURL,
		`22 Path pod range= constraint="" version="" ` + global + ` skip=` + skipPath,
		`23 Spec pod range= constraint="" version="" ` + global + ` skip=` + skipPodspec,
		`24 Http pod range= constraint="" version="" ` + global + ` skip=` + skipExternal,
		`25 SrcVar pod range= constraint="" version="" source= skip=` + skipNonLiteralSource,
		`26 Bare pod range= constraint="" version="" ` + global + ` skip=` + skipUnspecified,
	}
	got := extractPods(t, "Podfile", []byte(src))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("deps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCocoaPodsEdits(t *testing.T) {
	src := []byte("pod 'Alamofire', '~> 5.8'\npod 'Git', :git => 'https://github.com/o/git.git', :tag => 'v1.2.3'\npod 'Multi', '>= 1.0', '< 2'\n")
	m := NewCocoaPods()
	deps, err := m.Extract("Podfile", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	rr := m.(manager.RangeRewriter)
	for _, c := range []struct {
		d              dep.Dep
		v, strategy    string
		want, newRange string
		wantErr        bool
	}{
		{d: deps[0], v: "6.0.1", want: "pod 'Alamofire', '~> 6.0'\n", newRange: "'~> 6.0'"},
		{d: deps[0], v: "6.0.1", strategy: "pin", want: "pod 'Alamofire', '= 6.0.1'\n", newRange: "'= 6.0.1'"},
		{d: deps[1], v: "v1.3.0", want: "pod 'Git', :git => 'https://github.com/o/git.git', :tag => 'v1.3.0'\n"},
		{d: deps[2], v: "2.1.0", strategy: "widen", want: "pod 'Multi', '>= 1.0', '< 3'\n", newRange: "'>= 1.0', '< 3'"},
		{d: dep.Dep{Name: "Head", Datasource: PodDatasource}, v: "1.0.0", wantErr: true},
	} {
		edits, err := m.Edits(dep.Update{Dep: c.d, NewVersion: c.v, RangeStrategy: c.strategy})
		if c.wantErr {
			if err == nil {
				t.Errorf("%s %s %s: want error", c.d.Name, c.v, c.strategy)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.d.Name, err)
		}
		out, err := patch.Apply(src, edits)
		if err != nil {
			t.Fatal(err)
		}
		if line := strings.SplitAfter(string(out), "\n")[c.d.Line-1]; line != c.want {
			t.Errorf("%s %s %s: %q, want %q", c.d.Name, c.v, c.strategy, line, c.want)
		}
		if c.d.Datasource == GitDatasource {
			continue
		}
		if nr, ok := rr.NewRange(c.d, c.v, c.strategy); !ok || nr != c.newRange {
			t.Errorf("NewRange(%s, %s, %s) = %q, %v", c.d.Name, c.v, c.strategy, nr, ok)
		}
	}
}

func TestLockedPods(t *testing.T) {
	pods, err := lockedPods(read(t, "testdata/cocoapods/stepik/Podfile.lock"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"Alamofire": "5.10.1", "Firebase": "10.29.0", "AppAuth": "1.7.5", "Presentr": "1.9", "GoogleUtilities": "7.13.3", "DGCharts": "5.1.0"} {
		if pods[name] != want {
			t.Errorf("%s = %q, want %s", name, pods[name], want)
		}
	}
	if _, ok := pods["Firebase/Core"]; ok {
		t.Error("subspecs are recorded under their pod")
	}
	if _, ok := pods["Alamofire (= 5.10.1)"]; ok || len(pods) > 120 {
		t.Errorf("read past PODS: %d pods", len(pods))
	}
	if _, err := lockedPods([]byte("DEPENDENCIES:\n  - A\n")); err == nil {
		t.Error("lock without PODS: want error")
	}
}

func TestCocoaPodsReadLocks(t *testing.T) {
	root := t.TempDir()
	stepik := read(t, "testdata/cocoapods/stepik/Podfile.lock")
	for p, b := range map[string][]byte{
		"ios/Podfile.lock": stepik, "ios/Podfile": nil,
		"bad/Podfile.lock": []byte("not: a lock\n"), "bad/Podfile": nil,
		"none/Podfile": nil,
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := []byte("pod 'Alamofire', '~> 5.0'\npod 'Firebase/Core', '~> 10.0'\npod 'Kanna', '< 6'\npod 'Missing', '< 2'\npod 'Git', :git => 'https://github.com/o/g.git', :tag => '1.0'\n")
	var files [][]dep.Dep
	for _, f := range []string{"ios/Podfile", "bad/Podfile", "none/Podfile"} {
		deps, err := NewCocoaPods().Extract(f, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, deps)
	}
	errs := NewCocoaPods().(manager.LockReader).ReadLocks(root, files)
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "bad/Podfile.lock: not a Podfile.lock") {
		t.Errorf("errors = %v", errs)
	}
	describe := func(d dep.Dep) string {
		return fmt.Sprintf("%s version=%s locked=%s unread=%v skip=%s", d.Name, d.Version, d.Locked, d.LockUnread, d.Skip)
	}
	want := [][]string{{
		"Alamofire version=5.0 locked=5.10.1 unread=false skip=",
		"Firebase/Core version=10.0 locked=10.29.0 unread=false skip=",
		"Kanna version=5.2.7 locked=5.2.7 unread=false skip=",
		"Missing version= locked= unread=false skip=" + skipPodNoLock,
		"Git version=1.0 locked= unread=false skip=",
	}, {
		"Alamofire version=5.0 locked= unread=true skip=",
		"Firebase/Core version=10.0 locked= unread=true skip=",
		"Kanna version= locked= unread=true skip=" + skipPodNoLock,
		"Missing version= locked= unread=true skip=" + skipPodNoLock,
		"Git version=1.0 locked= unread=false skip=",
	}, {
		"Alamofire version=5.0 locked= unread=false skip=",
		"Firebase/Core version=10.0 locked= unread=false skip=",
		"Kanna version= locked= unread=false skip=" + skipPodNoLock,
		"Missing version= locked= unread=false skip=" + skipPodNoLock,
		"Git version=1.0 locked= unread=false skip=",
	}}
	for i, f := range files {
		for j, d := range f {
			if got := describe(d); got != want[i][j] {
				t.Errorf("%s: %s, want %s", d.File, got, want[i][j])
			}
		}
	}
	roots := NewCocoaPods().(manager.LockMaintainer).LockRoots(root, []string{"none/Podfile", "ios/Podfile", "bad/Podfile"})
	if len(roots) != 2 || roots[0] != (manager.LockRoot{Manifest: "bad/Podfile", Lockfile: "bad/Podfile.lock"}) || roots[1].Lockfile != "ios/Podfile.lock" {
		t.Errorf("lock roots = %v", roots)
	}
}
