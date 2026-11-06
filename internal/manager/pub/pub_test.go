package pub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func extract(t *testing.T, file string) ([]byte, []dep.Dep) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return src, ds
}

func byName(ds []dep.Dep, name string) dep.Dep {
	for _, d := range ds {
		if d.Name == name {
			return d
		}
	}
	return dep.Dep{}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"pubspec.yaml": true, "mobile/pubspec.yaml": true, "pkgs/http/pubspec.yml": true,
		"pubspec.lock": false, "pubspec_overrides.yaml": false, "my_pubspec.yaml": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
	if m.Name() != Name {
		t.Error("Name")
	}
}

// TestExtractImmich reads the Flutter app of immich-app/immich (mobile/pubspec.yaml, fetched
// 2026-11-06): caret and exact constraints, SDK, path and git packages pinned to commits.
func TestExtractImmich(t *testing.T) {
	src, ds := extract(t, "immich/mobile/pubspec.yaml")
	if len(ds) != 81 {
		t.Errorf("deps = %d, want 81 (79 packages, dart and flutter)", len(ds))
	}
	for _, tt := range []struct {
		name, depType, datasource, rng, version, skip string
	}{
		{"async", DepTypeDependencies, "dart", "^2.13.1", "2.13.1", ""},
		{"drift_sqlite_async", DepTypeDependencies, "dart", "0.3.1", "0.3.1", ""},
		{"flutter_hooks", DepTypeDependencies, "dart", "^0.21.3+1", "0.21.3+1", ""},
		{"flutter", DepTypeDependencies, "dart", "", "", skipSDK},
		{"immich_ui", DepTypeDependencies, "dart", "", "", skipPath},
		{"native_video_player", DepTypeDependencies, "git-tags", "", "cdf621bdb7edaf996e118a58a48f6441187d79c6", skipGitCommit},
		{"integration_test", DepTypeDevDependencies, "dart", "", "", skipSDK},
		{"mocktail", DepTypeDevDependencies, "dart", "^1.0.5", "1.0.5", ""},
		{"dart", DepTypeEnvironment, "dart-version", ">=3.12.0 <4.0.0", "3.12.0", ""},
		{"flutter", DepTypeEnvironment, "flutter-version", "3.47.2", "3.47.2", ""},
	} {
		var d dep.Dep
		for _, x := range ds {
			if x.Name == tt.name && x.DepType == tt.depType {
				d = x
			}
		}
		if d.Datasource != tt.datasource || d.Range != tt.rng || d.Skip != tt.skip || tt.skip == "" && d.Version != tt.version || d.Manager != Name {
			t.Errorf("%s: %+v", tt.name, d)
		}
		if tt.skip == "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != tt.rng {
			t.Errorf("%s: span %q", tt.name, src[d.RefSpan.Start:d.RefSpan.End])
		}
	}
	if d := byName(ds, "async"); d.Line != 12 || d.InsertAt != d.RefSpan.Start {
		t.Errorf("async: line %d, plain %v", d.Line, d.InsertAt)
	}
	if d := byName(ds, "dart"); d.InsertAt != 0 || d.Versioning != Versioning {
		t.Errorf("quoted sdk: %+v", d)
	}
}

// TestExtractHTTP reads dart-lang/http's pkgs/http/pubspec.yaml (a Dart package, fetched
// 2026-11-06): `meta` is a normal package outside Flutter, `web` a quoted range.
func TestExtractHTTP(t *testing.T) {
	_, ds := extract(t, "http/pkgs/http/pubspec.yaml")
	var got []string
	for _, d := range ds {
		got = append(got, d.DepType+":"+d.Name+"="+d.Range+"/"+d.Skip)
	}
	want := "dependencies:async=^2.5.0/ dependencies:http_parser=^4.0.0/ dependencies:meta=^1.3.0/ dependencies:web=>=0.5.0 <2.0.0/ " +
		"dev_dependencies:dart_flutter_team_lints=^3.0.0/ dev_dependencies:fake_async=^1.2.0/ dev_dependencies:http_client_conformance_tests=/path dependency " +
		"dev_dependencies:stream_channel=^2.1.1/ dev_dependencies:test=^1.21.2/ environment:dart=^3.5.0/"
	if g := strings.Join(got, " "); g != want {
		t.Errorf("deps:\n%s\nwant:\n%s", g, want)
	}
}

func TestExtractForms(t *testing.T) {
	src := `name: x
environment:
  sdk: <4.0.0
dependencies:
  plain: 1.2.3
  none:
  anything: any
  tilde: ~1.2.3
  hosted_short:
    hosted: https://pub.example.com
    version: ^2.0.0
  hosted_long:
    hosted:
      name: real_name
      url: https://pub.example.com/
    version: '>=1.0.0 <3.0.0'
  hosted_unversioned:
    hosted: https://pub.example.com
  git_tag:
    git:
      url: git@github.com:acme/pkg.git
      ref: v1.4.0
      path: packages/pkg
  git_string:
    git: https://github.com/acme/pkg.git
  git_bad:
    git:
      url: file:///tmp/pkg
      ref: v1.0.0
  escaped: "\x5e1.0.0"
  block: |
    ^1.0.0
  list: [1]
  no_floor: <2.0.0
dev_dependencies: [oops]
`
	ds, err := New().Extract("pubspec.yaml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]dep.Dep{}
	for _, d := range ds {
		got[d.Name] = d
	}
	for name, want := range map[string]string{
		"plain": "dart||1.2.3|1.2.3|", "none": "dart||||" + skipUnversioned, "anything": "dart||||" + skipUnversioned, "tilde": "dart||||" + skipUnsupported,
		"hosted_short": "dart|https://pub.example.com|^2.0.0|2.0.0|", "real_name": "dart|https://pub.example.com/|>=1.0.0 <3.0.0|1.0.0|",
		"hosted_unversioned": "dart|https://pub.example.com|||" + skipUnversioned,
		"git_tag":            "git-tags|https://github.com/acme/pkg||v1.4.0|", "git_string": "git-tags|https://github.com/acme/pkg|||" + skipGitNoRef,
		"git_bad": "git-tags||||" + skipGitURL, "escaped": "dart||||" + skipEscaped, "block": "dart||||" + skipUnsupported, "list": "dart||||" + skipUnsupported,
		"no_floor": "dart||<2.0.0||", "dart": "dart-version||||" + skipNoFloor,
	} {
		d := got[name]
		v := d.Version
		if d.Skip != "" {
			v, d.Range = "", ""
		}
		if g := strings.Join([]string{d.Datasource, d.Source, d.Range, v, d.Skip}, "|"); g != want {
			t.Errorf("%s: %q, want %q", name, g, want)
		}
	}
	if d := got["git_tag"]; d.Versioning != TagVersioning || src[d.RefSpan.Start:d.RefSpan.End] != "v1.4.0" {
		t.Errorf("git_tag: %+v", d)
	}
	for _, bad := range []string{"a: [", "- 1\n"} {
		if _, err := New().Extract("pubspec.yaml", []byte(bad), manager.Env{}); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if ds, err := New().Extract("pubspec.yaml", nil, manager.Env{}); err != nil || ds != nil {
		t.Errorf("empty: %v, %v", ds, err)
	}
}

func TestEdits(t *testing.T) {
	src, ds := extract(t, "immich/mobile/pubspec.yaml")
	for _, tt := range []struct {
		name, version, strategy, want string
	}{
		{"async", "3.0.0", "", "async: ^3.0.0\n"},
		{"async", "3.0.0", dep.RangeWiden, "async: '>=2.13.1 <4.0.0'\n"},
		{"drift_sqlite_async", "0.4.0", "", "drift_sqlite_async: 0.4.0\n"},
		{"dart", "4.1.0", "", "sdk: '>=4.1.0 <5.0.0'\n"},
		{"flutter", "3.47.9", "", "flutter: 3.47.9\n"},
	} {
		d := byName(ds, tt.name)
		if tt.name == "flutter" {
			d = ds[len(ds)-1]
		}
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: tt.version, RangeStrategy: tt.strategy})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		out, err := patch.Apply(src, edits)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), tt.want) {
			t.Errorf("%s %s: no %q in edit %+v", tt.name, tt.version, tt.want, edits)
		}
		if _, err := New().Extract("pubspec.yaml", out, manager.Env{}); err != nil {
			t.Errorf("%s: edited pubspec does not parse: %v", tt.name, err)
		}
	}
	git := dep.Dep{Range: "", Version: "v1.4.0", RefSpan: dep.Span{Start: 3, End: 9}}
	if e, err := New().Edits(dep.Update{Dep: git, NewVersion: "v1.5.0"}); err != nil || e[0].New != "v1.5.0" || e[0].Old != "v1.4.0" {
		t.Errorf("git edit = %+v, %v", e, err)
	}
	if _, err := New().Edits(dep.Update{Dep: dep.Dep{Range: "^1.2.3"}, NewVersion: "1.2.3"}); err == nil {
		t.Error("an unchanged constraint was edited")
	}
	if r, ok := New().(manager.RangeRewriter).NewRange(dep.Dep{Range: "^1.2.3"}, "2.0.0", ""); !ok || r != "^2.0.0" {
		t.Errorf("NewRange = %q, %v", r, ok)
	}
}

func TestNewRange(t *testing.T) {
	for _, tt := range []struct {
		rng, v, strategy, want string
	}{
		// admitted versions raise the lower bound
		{"^1.2.3", "1.4.0", dep.RangeWiden, "^1.4.0"},
		{"^1.2.3", "1.4.0", "", "^1.4.0"},
		{">=1.2.3 <2.0.0", "1.4.0", "bump", ">=1.4.0 <2.0.0"},
		{">1.2.3", "1.4.0", "bump", ">=1.4.0"},
		{"> 1.2.3 <2.0.0", "1.4.0", "bump", ">= 1.4.0 <2.0.0"},
		{"<2.0.0", "1.4.0", "bump", ""},
		{"^1.2.3", "1.2.3", "bump", ""},
		// exact versions
		{"1.2.3", "1.4.0", "", "1.4.0"},
		{"1.2.3", "2.0.0", dep.RangeWiden, "2.0.0"},
		// replace
		{"^1.2.3", "2.1.0", "", "^2.1.0"},
		{"^0.21.3+1", "0.22.0", "replace", "^0.22.0"},
		{">=1.0.0 <2.0.0", "2.1.0", "", ">=2.1.0 <3.0.0"},
		{">=1.0.0<2.0.0", "2.1.0", "", ">=2.1.0<3.0.0"},
		{">=0.5.0 <2.0.0", "2.1.0", "update-lockfile", ">=2.1.0 <3.0.0"},
		{">=0.1.0 <0.2.0", "0.3.1", "", ">=0.3.1 <0.4.0"},
		{"<=2.0.0", "2.1.0", "", "<=2.1.0"},
		{"<2.0.0", "2.1.0", "", "<3.0.0"},
		// widen
		{"^1.2.3", "2.1.0", dep.RangeWiden, ">=1.2.3 <3.0.0"},
		{"^0.4.1", "0.5.0", dep.RangeWiden, ">=0.4.1 <0.6.0"},
		{">=1.0.0 <2.0.0", "2.1.0", dep.RangeWiden, ">=1.0.0 <3.0.0"},
		{">=1.0.0 <=2.0.0", "2.1.0", dep.RangeWiden, ">=1.0.0 <=2.1.0"},
		// pin
		{"^1.2.3", "1.4.0", dep.RangePin, "1.4.0"},
		{"1.4.0", "1.4.0", dep.RangePin, ""},
		// not rewritable
		{"any", "1.0.0", "", ""},
		{"~1.2.3", "2.0.0", "", ""},
		{">=2.0.0", "1.0.0", "", ""},
	} {
		got, ok := newRange(tt.rng, tt.v, tt.strategy)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("newRange(%q, %s, %q) = %q, %v; want %q", tt.rng, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
}

func TestNeedsQuote(t *testing.T) {
	for s, want := range map[string]bool{"^1.2.3": false, "1.2.3": false, "<2.0.0": false, ">=1.0.0 <2.0.0": true, "": true, "a: b": true, "a #b": true} {
		if needsQuote(s) != want {
			t.Errorf("needsQuote(%q) = %v", s, !want)
		}
	}
}
