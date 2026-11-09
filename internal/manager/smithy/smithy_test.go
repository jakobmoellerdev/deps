package smithy

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
		fmt.Fprintf(&b, "%d %s %s", d.Line, d.Name, d.Version)
		if d.Skip != "" {
			fmt.Fprintf(&b, " skip=%q", d.Skip)
		}
		if d.Source != "" {
			fmt.Fprintf(&b, " source=%q", d.Source)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func extract(t *testing.T, src []byte) []dep.Dep {
	t.Helper()
	ds, err := New().Extract("smithy-build.json", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Skip == "" {
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Version {
				t.Errorf("%s: RefSpan %q, want %q", d.Name, got, d.Version)
			}
		}
		if d.Manager != Name || d.Versioning != "maven" || d.Datasource != "maven" || d.DepType != DepTypeMaven {
			t.Errorf("%s: %+v", d.Name, d)
		}
	}
	return ds
}

// TestExtractFixtures reads smithy-lang/smithy-examples build files.
func TestExtractFixtures(t *testing.T) {
	for file, want := range map[string]string{
		"testdata/smithy-to-openapi/smithy-build.json": `6 software.amazon.smithy:smithy-openapi 1.73.0
7 software.amazon.smithy:smithy-aws-traits 1.73.0
`,
		"testdata/docgen-cli/smithy-build.json": `8 software.amazon.smithy:smithy-docgen 1.60.3
9 software.amazon.smithy:smithy-protocol-traits 1.60.3
10 software.amazon.smithy:smithy-aws-traits 1.60.3
`,
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := render(extract(t, src)); got != want {
			t.Errorf("%s: deps =\n%s\nwant\n%s", file, got, want)
		}
	}
}

func TestExtractEdgeCases(t *testing.T) {
	src := []byte(`{
  // comment
  "version": "1.0",
  "maven": {
    "dependencies": [
      "a.b:plain:1.0.0",
      "a.b:nover",
      "a.b:empty:",
      "a.b:var:${v}",
      "${g}:x:1",
      "a.b:c:d:1",
      ":x:1",
      "a.b:esc\u0061ped:1.0",
      7,
    ],
    "repositories": [
      {"url": "https://maven.example.com/releases"},
      {"url": "https://maven.example.com/releases/"},
      {"url": "${REPO}"},
      {"id": "x"},
      {"url": 1},
      "not an object",
    ],
    "other": true,
  },
}`)
	const s = ` source="https://maven.example.com/releases/ https://repo.maven.apache.org/maven2/"`
	want := `6 a.b:plain 1.0.0` + s + `
7 a.b:nover  skip="no version"` + s + `
8 a.b:empty  skip="no version"` + s + `
9 a.b:var  skip="variable reference"` + s + `
10 ${g}:x:1  skip="variable reference"` + s + `
11 a.b:c:d:1  skip="invalid dependency specification"` + s + `
12 :x:1  skip="invalid dependency specification"` + s + `
13 a.b:escaped 1.0 skip="cannot locate value"` + s + `
`
	if got := render(extract(t, src)); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}

	central := `{"maven": {"repositories": [{"url": "https://repo.maven.apache.org/maven2"}], "dependencies": ["a:b:1"]}}`
	if got, want := render(extract(t, []byte(central))), "1 a:b 1 source=\"https://repo.maven.apache.org/maven2/\"\n"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	for _, src := range []string{`{"maven": {"repositories": [{"url": "https://x/"}]}}`, `{"maven": {"dependencies": []}}`, `{"maven": []}`, `[]`, ``} {
		if ds := extract(t, []byte(src)); len(ds) != 0 {
			t.Errorf("%s: deps %v", src, ds)
		}
	}
	for _, src := range []string{`{`, `{"maven": {`, `{"maven": {"dependencies": [`, `{"maven": {"dependencies": ["a:b:1"`,
		`{"maven": {"repositories": [{`, `{"maven": {"repositories": [{"url": `, `{"maven": {"repositories": [{"url": "x"`, `{"maven": {"repositories": [`, `{"x": `, `x`} {
		if _, err := New().Extract("smithy-build.json", []byte(src), manager.Env{}); err == nil {
			t.Errorf("%s: no error", src)
		}
	}
}

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{"smithy-build.json": true, "a/smithy-build.json": true, "a/xsmithy-build.json": false, "smithy-build.json5": false} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
	if New().Name() != Name {
		t.Error("name")
	}
}

func TestEdits(t *testing.T) {
	src := []byte(`{"maven": {"dependencies": ["a:b:1.0.0"]}}`)
	ds := extract(t, src)
	es, err := New().Edits(dep.Update{Dep: ds[0], NewVersion: "1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(src, es)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"maven": {"dependencies": ["a:b:1.1.0"]}}`; string(out) != want {
		t.Errorf("got %s", out)
	}
}
