package depsconfig

import (
	"fmt"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

const sha1 = "0123456789abcdef0123456789abcdef01234567"
const sha2 = "89abcdef0123456789abcdef0123456789abcdef"

// summarize renders each dep as "<line> <name> <datasource> <source> <version> [<digest>] [<skip>]"
// and checks that the spans cover the version or digest.
func summarize(t *testing.T, src []byte, deps []dep.Dep) []string {
	t.Helper()
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if d.Manager != Name || d.DepType != DepTypeExtends || d.Versioning != "tag" || d.File != ".deps.yaml" {
			t.Errorf("%s: manager %q depType %q versioning %q file %q", d.Name, d.Manager, d.DepType, d.Versioning, d.File)
		}
		s := fmt.Sprintf("%d %s %s %s %s", d.Line, d.Name, d.Datasource, d.Source, d.Version)
		switch {
		case d.Skip != "":
			s += " [" + d.Skip + "]"
			if d.RefSpan != (dep.Span{}) || d.DigestSpan != (dep.Span{}) {
				t.Errorf("%s: skipped dep has spans", d.Name)
			}
		case d.Digest != "":
			s += " " + d.Digest
			if got := string(src[d.DigestSpan.Start:d.DigestSpan.End]); got != d.Digest {
				t.Errorf("%s: digest span %q", d.Name, got)
			}
		default:
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Version {
				t.Errorf("%s: ref span %q", d.Name, got)
			}
		}
		out = append(out, s)
	}
	return out
}

func TestExtract(t *testing.T) {
	src := `# yaml-language-server: $schema=https://example.com/deps.schema.json
extends:
  - org/deps-config#v1.2.0
  - "org/deps-config//presets/go.yaml#v2"
  - 'group/sub/cfg//base.yaml#` + sha1 + `'
  - https://github.com/acme/presets.git//renovate.yaml#1.4
  - git@gitlab.com:acme/presets.git#v3.0.1
  - ssh://git@host:2222/acme/presets#v1
  - org/no-ref
  - org/branch#main
  - org/release#release/1.x
  - file:///srv/git/cfg.git#v1
  - not a repo#v1
  - "org/escaped\x23v1"
  - ? key
rules:
  - names: [extends]
`
	deps, err := New().Extract(".deps.yaml", []byte(src), manager.Env{ForgeURL: "https://git.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	got := summarize(t, []byte(src), deps)
	want := []string{
		"3 org/deps-config git-tags https://git.example.com/org/deps-config v1.2.0",
		"4 org/deps-config git-tags https://git.example.com/org/deps-config v2",
		"5 group/sub/cfg git-refs https://git.example.com/group/sub/cfg HEAD " + sha1,
		"6 acme/presets git-tags https://github.com/acme/presets 1.4",
		"7 acme/presets git-tags https://gitlab.com/acme/presets v3.0.1",
		"8 acme/presets git-tags https://host/acme/presets v1",
		"9 org/no-ref git-tags https://git.example.com/org/no-ref  [no ref]",
		"10 org/branch git-tags https://git.example.com/org/branch main [non-version ref]",
		"11 org/release git-tags https://git.example.com/org/release release/1.x [non-version ref]",
		"12 file:///srv/git/cfg.git git-tags   [unsupported repository URL]",
		"13 not a repo#v1 git-tags   [invalid extends entry]",
		"14 org/escaped git-tags https://git.example.com/org/escaped v1 [cannot locate value]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestExtractWithoutForge(t *testing.T) {
	src := []byte("extends: [org/cfg#v1, 'https://github.com/o/r#v2']\n")
	got := summarize(t, src, mustExtract(t, src, manager.Env{}))
	want := "1 org/cfg git-tags   [unknown forge]\n1 o/r git-tags https://github.com/o/r v2"
	if strings.Join(got, "\n") != want {
		t.Errorf("got %q", got)
	}
}

func TestExtractNoExtends(t *testing.T) {
	for _, src := range []string{"", "pin: digest\n", "extends: org/cfg#v1\n", "- a\n- b\n", "extends: []\n"} {
		if deps := mustExtract(t, []byte(src), manager.Env{ForgeURL: "https://h"}); len(deps) != 0 {
			t.Errorf("%q: %v", src, deps)
		}
	}
	if _, err := New().Extract(".deps.yaml", []byte("extends: [\n"), manager.Env{}); err == nil {
		t.Error("invalid YAML: no error")
	}
}

func TestEdits(t *testing.T) {
	src := []byte("extends:\n  - org/cfg#v1.2.0\n  - \"org/cfg//go.yaml#" + sha1 + "  \"\n")
	deps := mustExtract(t, src, manager.Env{ForgeURL: "https://h", YAML: &yamlspan.Parsed{}})
	if len(deps) != 2 {
		t.Fatalf("deps = %+v", deps)
	}
	m := New()
	var edits []patch.Edit
	for _, u := range []dep.Update{{Dep: deps[0], NewVersion: "v1.3.0"}, {Dep: deps[1], NewVersion: headRef, NewDigest: sha2}} {
		e, err := m.Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := "extends:\n  - org/cfg#v1.3.0\n  - \"org/cfg//go.yaml#" + sha2 + "  \"\n"; string(out) != want {
		t.Errorf("patched = %q, want %q", out, want)
	}
	if _, err := m.Edits(dep.Update{Dep: deps[1], NewVersion: headRef}); err == nil {
		t.Error("commit update without a digest: no error")
	}
}

func TestMatch(t *testing.T) {
	m := New()
	if m.Name() != Name {
		t.Errorf("Name() = %q", m.Name())
	}
	for p, want := range map[string]bool{
		".deps.yaml": true, ".deps.yml": true, "sub/.deps.yaml": false, "deps.yaml": false, ".deps.yaml.bak": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
}

func mustExtract(t *testing.T, src []byte, env manager.Env) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(".deps.yaml", src, env)
	if err != nil {
		t.Fatal(err)
	}
	return deps
}
