package osgi

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
		fmt.Fprintf(&b, "%d %s %s %s", d.Line, d.DepType, d.Name, d.Version)
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
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Version {
				t.Errorf("%s: RefSpan %q, want %q", d.Name, got, d.Version)
			}
		}
		if d.Manager != Name || d.Versioning != "maven" || d.Datasource != "maven" || d.File != file {
			t.Errorf("%s: %+v", d.Name, d)
		}
	}
	return ds
}

// TestExtractFixtures reads feature models of apache/sling-org-apache-sling-starter.
func TestExtractFixtures(t *testing.T) {
	src, err := os.ReadFile("testdata/sling-starter/webconsole.json")
	if err != nil {
		t.Fatal(err)
	}
	want := `4 bundles org.apache.felix:org.apache.felix.webconsole 5.1.0
8 bundles org.apache.felix:org.apache.felix.webconsole.plugins.ds 2.3.0
12 bundles org.apache.felix:org.apache.felix.webconsole.plugins.event 1.2.0
16 bundles org.apache.felix:org.apache.felix.webconsole.plugins.memoryusage 1.1.0
20 bundles org.apache.felix:org.apache.felix.webconsole.plugins.obr 1.1.0
24 bundles org.apache.felix:org.apache.felix.webconsole.plugins.packageadmin 1.1.0
28 bundles org.apache.sling:org.apache.sling.commons.log.webconsole 2.0.0
`
	if got := render(extract(t, "src/main/features/webconsole.json", src)); !strings.HasPrefix(got, want) {
		t.Errorf("deps =\n%s\nwant prefix\n%s", got, want)
	}

	src, err = os.ReadFile("testdata/sling-starter/base.json")
	if err != nil {
		t.Fatal(err)
	}
	ds := extract(t, "src/main/features/base.json", src)
	got := render(ds)
	for _, line := range []string{
		"5 framework org.apache.felix:org.apache.felix.framework 8.0.0\n",
		"38 bundles com.fasterxml.jackson.core:jackson-core  skip=\"variable reference\"\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("deps lack %q:\n%s", line, got)
		}
	}
	if ds[0].DepType != DepTypeFramework {
		t.Errorf("first dep %+v, want the framework", ds[0])
	}
}

func TestExtractEdgeCases(t *testing.T) {
	src := []byte(`{
  // a comment
  "feature-resource-version": "1.0",
  "bundles": [
    "org.example:plain:1.0.0",
    "org.example/slash/2.0.0",
    "org.example:jar:jar:3.0.0",
    "org.example:classified:jar:tests:4.0.0",
    "org.example:too:many:parts:here:5.0.0",
    "org.example:short",
    "org.example:empty:",
    "org.example:var:${v}",
    "org.example:esc\u0061ped:1.0.0",
    "",
    {"start-order": 1},
    {"id": 7},
    {"other": [1], "id": "org.example:object:6.0.0"},
    7,
  ],
  "execution-environment:JSON|false": {"javaVersion": "17", "framework": "org.apache.felix:org.apache.felix.framework:7.0.5"},
  "content-packages:ARTIFACTS|true": [{"id": "org.example:package:zip:1.2.3"}],
  "configurations": {"x": "org.example:not-a-dep:1.0.0"},
  "other:ARTIFACTS|false": {"id": "org.example:not-an-array:1.0.0"},
}`)
	want := `5 bundles org.example:plain 1.0.0
6 bundles org.example:slash 2.0.0
7 bundles org.example:jar 3.0.0
8 bundles org.example:classified 4.0.0
9 bundles org.example:too:many:parts:here:5.0.0  skip="invalid value"
10 bundles org.example:short  skip="invalid value"
11 bundles org.example:empty  skip="no version"
12 bundles org.example:var  skip="variable reference"
13 bundles org.example:escaped 1.0.0 skip="cannot locate value"
17 bundles org.example:object 6.0.0
20 framework org.apache.felix:org.apache.felix.framework 7.0.5
21 artifacts org.example:package 1.2.3
`
	if got := render(extract(t, "src/main/features/x.json", src)); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}

	for _, src := range []string{
		`{"feature-resource-version": "2.0", "bundles": ["a:b:1"]}`,
		`{"feature-resource-version": "x", "bundles": ["a:b:1"]}`,
		`{"feature-resource-version": 2, "bundles": ["a:b:1"]}`,
		`[]`, ``, `{}`, `{"bundles": "a:b:1"}`,
	} {
		if ds := extract(t, "f.json", []byte(src)); len(ds) != 0 {
			t.Errorf("%s: deps %v", src, ds)
		}
	}
	for _, src := range []string{`{"feature-resource-version": "v1.1", "bundles": ["a:b:1"]}`, `{"feature-resource-version": 1, "bundles": ["a:b:1"]}`} {
		if ds := extract(t, "f.json", []byte(src)); len(ds) != 1 {
			t.Errorf("%s: deps %v", src, ds)
		}
	}
	for _, src := range []string{`{`, `{"bundles": [`, `{"bundles": [{`, `{"bundles": [{"id": `, `{"execution-environment:JSON|false": {`,
		`{"execution-environment:JSON|false": {"framework": `, `{"bundles": [], `, `{"bundles": ["a:b:1"`, `{"feature-resource-version": `, `{"x": `, `x`} {
		if _, err := New().Extract("f.json", []byte(src), manager.Env{}); err == nil {
			t.Errorf("%s: no error", src)
		}
	}
}

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		"src/main/features/base.json":        true,
		"mod/src/main/features/app/app.json": true,
		"src/main/features/.json":            false,
		"src/main/features/base.txt":         false,
		"xsrc/main/features/base.json":       false,
		"base.json":                          false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
	if New().Name() != Name {
		t.Error("name")
	}
}

func TestEdits(t *testing.T) {
	src := []byte(`{"bundles": [{"id": "org.example:plain:1.0.0"}, "org.example/slash/jar/2.0.0"]}`)
	ds := extract(t, "f.json", src)
	var edits []patch.Edit
	for _, d := range ds {
		es, err := New().Edits(dep.Update{Dep: d, NewVersion: "9.9.9"})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, es...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"bundles": [{"id": "org.example:plain:9.9.9"}, "org.example/slash/jar/9.9.9"]}`; string(out) != want {
		t.Errorf("got %s", out)
	}
}
