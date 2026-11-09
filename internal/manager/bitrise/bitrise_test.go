package bitrise

import (
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const steplib = "https://github.com/bitrise-io/bitrise-steplib.git"

type want struct {
	line                            int
	ds, name, source, version, skip string
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract("bitrise.yml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range deps {
		if d.Manager != Name || d.DepType != DepTypeStep {
			t.Errorf("%d: %s/%s", i, d.Manager, d.DepType)
		}
		if d.Skip == "" && (d.Versioning != "tag" || string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version) {
			t.Errorf("%d: versioning %q, src[RefSpan] = %q, want %q", i, d.Versioning, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
		}
	}
	return deps
}

func check(t *testing.T, deps []dep.Dep, n int, wants map[int]want) {
	t.Helper()
	if len(deps) != n {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), n, deps)
	}
	for i, w := range wants {
		d := deps[i]
		if d.Line != w.line || d.Datasource != w.ds || d.Name != w.name || d.Source != w.source || d.Version != w.version || d.Skip != w.skip {
			t.Errorf("%d: got line=%d ds=%q name=%q source=%q version=%q skip=%q\nwant %+v", i, d.Line, d.Datasource, d.Name, d.Source, d.Version, d.Skip, w)
		}
	}
}

func TestMatch(t *testing.T) {
	for path, ok := range map[string]bool{
		"bitrise.yml":          true,
		"bitrise.yaml":         true,
		".bitrise/bitrise.yml": true,
		"app/bitrise.yml":      true,
		"my-bitrise.yml":       false,
		"bitrise.yml.bak":      false,
		".bitrise.secrets.yml": false,
	} {
		if got := New().Match(path); got != ok {
			t.Errorf("Match(%q) = %v, want %v", path, got, ok)
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`format_version: "13"
workflows:
  primary:
    steps:
    - script@1.2:
        title: hi
    - https://github.com/acme/steplib.git::custom-step@0.3.1: {}
    - git::https://github.com/acme/steps-x.git@v2.0.0: {}
    - git::git@gitlab.com:acme/steps-y.git@1.4.0: {}
    - git::git@github.com:acme/steps-z.git: {}
    - git::steps-local: {}
    - path::./steps/local: {}
    - cache-pull:
    - bundle::shared: {}
    - with:
        container: node
        steps:
        - npm@1: {}
    - "\x73cript@1": {}
  other: {steps: x}
step_bundles:
  shared:
    steps:
    - git-clone@8: {}
`)
	check(t, extract(t, src), 11, map[int]want{
		0:  {line: 5, ds: "bitrise", name: "script", version: "1.2"},
		1:  {line: 7, ds: "bitrise", name: "custom-step", source: "https://github.com/acme/steplib.git", version: "0.3.1"},
		2:  {line: 8, ds: "git-tags", name: "github.com/acme/steps-x", source: "https://github.com/acme/steps-x", version: "v2.0.0"},
		3:  {line: 9, ds: "git-tags", name: "gitlab.com/acme/steps-y", source: "https://gitlab.com/acme/steps-y", version: "1.4.0"},
		4:  {line: 10, ds: "git-tags", name: "github.com/acme/steps-z", skip: "no version"},
		5:  {line: 11, ds: "git-tags", name: "steps-local", skip: "invalid step"},
		6:  {line: 12, name: "./steps/local", skip: "local path"},
		7:  {line: 13, ds: "bitrise", name: "cache-pull", skip: "no version"},
		8:  {line: 18, ds: "bitrise", name: "npm", version: "1"},
		9:  {line: 19, ds: "bitrise", name: "script", skip: "cannot locate value"},
		10: {line: 24, ds: "bitrise", name: "git-clone", version: "8"},
	})
}

func TestExtractEdgeCases(t *testing.T) {
	for _, src := range []string{"", "- a\n", "workflows: x\n", "workflows: {a: {steps: [x, [y]]}}\n"} {
		if deps := extract(t, []byte(src)); len(deps) != 0 {
			t.Errorf("%q: deps = %+v", src, deps)
		}
	}
	if _, err := New().Extract("bitrise.yml", []byte("workflows: [\n"), manager.Env{}); err == nil {
		t.Error("want parse error")
	}
}

// The fixtures are real configurations: rainbow-me/rainbow (partial versions, a step bundle,
// bundle references), instructure/canvas-ios (git steps at branches) and
// bitrise-steplib/steps-xcode-archive (git steps without version, steps without version, a
// local path step).
func TestExtractFixtures(t *testing.T) {
	for _, tt := range []struct {
		file  string
		n     int
		wants map[int]want
	}{
		{"testdata/rainbow.yml", 29, map[int]want{
			0:  {line: 11, ds: "bitrise", name: "activate-ssh-key", source: steplib, version: "4"},
			2:  {line: 35, ds: "bitrise", name: "git-clone", source: steplib, version: "8.1"},
			13: {line: 234, ds: "bitrise", name: "cocoapods-install", source: steplib, version: "2.3"},
			28: {line: 509, ds: "bitrise", name: "slack", source: steplib, version: "4.0"},
		}},
		{"testdata/canvas-ios.yml", 6, map[int]want{
			1: {line: 22, ds: "git-tags", name: "github.com/instructure/steps-verify-pr", source: "https://github.com/instructure/steps-verify-pr", version: "master"},
		}},
		{"testdata/steps-xcode-archive.yml", 6, map[int]want{
			0: {line: 7, ds: "git-tags", name: "github.com/bitrise-steplib/steps-check", skip: "no version"},
			2: {line: 25, ds: "bitrise", name: "script", skip: "no version"},
			4: {line: 36, name: "./", skip: "local path"},
		}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			src, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			check(t, extract(t, src), tt.n, tt.wants)
		})
	}
}

func TestEdits(t *testing.T) {
	src := []byte("workflows:\n  w:\n    steps:\n    - git-clone@8.1: {}\n")
	deps := extract(t, src)
	edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "8.5"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := patch.Apply(src, edits)
	if want := "workflows:\n  w:\n    steps:\n    - git-clone@8.5: {}\n"; err != nil || string(got) != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	if _, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "8.5", NewDigest: "abc"}); err == nil {
		t.Error("want digest error")
	}
}
