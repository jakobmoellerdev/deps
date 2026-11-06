package homeassistant

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func render(ds []dep.Dep) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "%d %s %s %s %s", d.Line, d.DepType, d.Datasource, d.Name, d.Version)
		if d.Range != "" {
			fmt.Fprintf(&b, " range=%q", d.Range)
		}
		if d.Skip != "" {
			fmt.Fprintf(&b, " skip=%q", d.Skip)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func extract(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	ds, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Skip == "" {
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Range {
				t.Errorf("%s: RefSpan %q, want %q", d.Name, got, d.Range)
			}
		}
		if d.Manager != Name || d.Versioning != "pep440" {
			t.Errorf("%s: manager %q versioning %q", d.Name, d.Manager, d.Versioning)
		}
	}
	return ds
}

func read(t *testing.T, file string) []byte {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// TestExtractFixtures reads alandtse/alexa_media_player's and hacs/integration's manifests.
func TestExtractFixtures(t *testing.T) {
	for file, want := range map[string]string{
		"testdata/alexa_media/manifest.json": `11 requirements pypi alexapy 1.34.0 range="==1.34.0"
11 requirements pypi wrapt 1.14.0 range=">=1.14.0"
11 requirements pypi dictor 0.1.12 range=">=0.1.12,<0.2"
`,
		"testdata/hacs/manifest.json": `23 requirements pypi aiogithubapi 22.10.1 range=">=22.10.1"
`,
	} {
		if got := render(extract(t, file, read(t, file))); got != want {
			t.Errorf("%s: deps =\n%s\nwant\n%s", file, got, want)
		}
	}
}

func TestExtractEdgeCases(t *testing.T) {
	src := []byte(`{
  "requirements": [
    "pyfoo[bar] == 1.2.3 ; python_version >= '3.12'",
    "unpinned",
    "lib @ git+https://github.com/o/lib@v1",
    "remote @ https://example.com/r.whl",
    "esc\u0061ped==1.0",
    "~not a name",
    42,
    "pkg (>=2.0)"
  ],
  "domain": "x",
  "name": "X",
  "extra": {"requirements": ["nested==1"]}
}`)
	want := `3 requirements pypi pyfoo 1.2.3 range="== 1.2.3"
4 requirements pypi unpinned  skip="unconstrained"
5 requirements pypi lib  skip="git dependency"
6 requirements pypi remote  skip="url dependency"
7 requirements pypi escaped  skip="cannot locate value"
8 requirements pypi ~not a name  skip="invalid dependency specification"
10 requirements pypi pkg 2.0 range=">=2.0"
`
	if got := render(extract(t, "custom_components/x/manifest.json", src)); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
}

// TestExtractNotHomeAssistant: manifests without string domain and name have no deps.
func TestExtractNotHomeAssistant(t *testing.T) {
	for _, src := range []string{
		`{"name": "My PWA", "requirements": ["a==1"]}`,
		`{"domain": 1, "name": "x", "requirements": ["a==1"]}`,
		`{"manifest_version": 3, "name": "ext"}`,
		`[]`,
		``,
	} {
		if ds := extract(t, "manifest.json", []byte(src)); len(ds) != 0 {
			t.Errorf("%s: deps %v", src, ds)
		}
	}
}

func TestExtractInvalid(t *testing.T) {
	for _, src := range []string{`{"domain": "x", "requirements": [`, `{"domain"`, `{`, `{"a": }`, `{"requirements": ["a" "b"]}`} {
		if _, err := New().Extract("manifest.json", []byte(src), manager.Env{}); err == nil {
			t.Errorf("%s: no error", src)
		}
	}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"manifest.json": true,
		"custom_components/alexa_media/manifest.json":   true,
		"homeassistant/components/matter/manifest.json": true,
		"mymanifest.json":   false,
		"manifest.json.bak": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
	if m.Name() != Name {
		t.Errorf("Name = %q", m.Name())
	}
}

func TestEdits(t *testing.T) {
	file := "testdata/alexa_media/manifest.json"
	src := read(t, file)
	ds := extract(t, file, src)
	m := New().(manager.RangeUpdater)
	for _, c := range []struct {
		i           int
		v, strategy string
		want        string
	}{
		{0, "1.35.1", "replace", `"alexapy==1.35.1"`},
		{2, "0.2.1", "replace", `"dictor>=0.2.1,<0.3"`},
		{1, "1.17.2", "bump", `"wrapt>=1.17.2"`},
	} {
		d := ds[c.i]
		if c.strategy == "replace" && m.InRange(d, c.v) {
			t.Errorf("%s admits %s", d.Range, c.v)
		}
		if _, ok := m.NewRange(d, c.v, c.strategy); !ok {
			t.Errorf("%s: no new range for %s", d.Range, c.v)
		}
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: c.v, RangeStrategy: c.strategy})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply(src, edits)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), c.want) {
			t.Errorf("%s → %s: %s", d.Range, c.v, out)
		}
	}
}
