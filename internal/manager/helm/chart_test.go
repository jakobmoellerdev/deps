package helm

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// wantChartDep is the expected outcome of one extracted dep; span is the text under RefSpan.
type wantChartDep struct {
	name, source, version, span, skip string
	line                              int
}

func checkChartDeps(t *testing.T, src string, got []dep.Dep, want []wantChartDep, depType string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d deps, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		d := got[i]
		if d.Manager != Name || d.Datasource != Datasource || d.Versioning != Versioning || d.DepType != depType {
			t.Errorf("dep %d: manager/datasource/versioning/type = %s/%s/%s/%s", i, d.Manager, d.Datasource, d.Versioning, d.DepType)
		}
		if d.Name != w.name || d.Source != w.source || d.Skip != w.skip || d.Line != w.line {
			t.Errorf("dep %d = {name %q source %q skip %q line %d}, want %+v", i, d.Name, d.Source, d.Skip, d.Line, w)
		}
		if w.skip == "" {
			if d.Version != w.version || src[d.RefSpan.Start:d.RefSpan.End] != w.span {
				t.Errorf("dep %d: version %q span %q, want %q %q", i, d.Version, src[d.RefSpan.Start:d.RefSpan.End], w.version, w.span)
			}
		}
	}
}

func TestChartDeps(t *testing.T) {
	const src = `apiVersion: v2
name: app
version: 0.1.0 # the chart's own version is not a dep
dependencies:
  - name: cert-manager
    version: v1.14.0
    repository: https://charts.jetstack.io/
  - name: redis
    version: "18.0.0"
    repository: oci://registry-1.docker.io/bitnamicharts
    alias: cache
  - name: ranged
    version: ~1.2.0
    repository: https://example.com/charts
  - name: partial
    version: "1.2"
    repository: https://example.com/charts
  - name: local
    version: 1.0.0
    repository: file://../local
  - name: subchart
    version: 1.0.0
  - name: aliased
    version: 1.0.0
    repository: "@stable"
  - name: aliased2
    version: 1.0.0
    repository: alias:stable
  - name: odd
    version: 1.0.0
    repository: stable
  - name: noversion
    repository: https://example.com/charts
  - name: escaped
    version: "1.0.\u0030"
    repository: https://example.com/charts
  - {name: "", version: 1.0.0}
  - just-a-string
`
	m := New()
	if !m.Match("charts/app/Chart.yaml") || !m.Match("Chart.yaml") {
		t.Fatal("Chart.yaml not matched")
	}
	got, err := m.Extract("charts/app/Chart.yaml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	checkChartDeps(t, src, got, []wantChartDep{
		{name: "cert-manager", source: "https://charts.jetstack.io", version: "v1.14.0", span: "v1.14.0", line: 6},
		{name: "redis", source: "oci://registry-1.docker.io/bitnamicharts", version: "18.0.0", span: "18.0.0", line: 9},
		{name: "ranged", source: "https://example.com/charts", skip: "version range", line: 13},
		{name: "partial", source: "https://example.com/charts", skip: "version range", line: 16},
		{name: "local", skip: "local chart", line: 18},
		{name: "subchart", skip: "local chart", line: 21},
		{name: "aliased", skip: "repository alias", line: 23},
		{name: "aliased2", skip: "repository alias", line: 26},
		{name: "odd", skip: "unknown chart repository stable", line: 29},
		{name: "noversion", source: "https://example.com/charts", skip: "no version", line: 32},
		{name: "escaped", source: "https://example.com/charts", skip: "cannot locate value", line: 35},
	}, TypeChart)
}

func TestChartDepsEdgeCases(t *testing.T) {
	m := New()
	for _, src := range []string{"", "apiVersion: v2\n", "dependencies: {}\n", "- a\n"} {
		got, err := m.Extract("Chart.yaml", []byte(src), manager.Env{})
		if err != nil || len(got) != 0 {
			t.Errorf("%q: %v, %v", src, got, err)
		}
	}
	if _, err := m.Extract("Chart.yaml", []byte("dependencies: [\n"), manager.Env{}); err == nil {
		t.Error("broken Chart.yaml accepted")
	}
}

func TestVersionSkip(t *testing.T) {
	for v, want := range map[string]string{
		"1.2.3": "", "v1.2.3": "", "1.2.3-rc.1+build.5": "", "0.0.0": "",
		"": "no version", "{{ .Values.v }}": "template value",
		"1.2": "version range", "^1.2.3": "version range", "~1.2.0": "version range", ">=1.0.0 <2": "version range",
		"1.2.x": "version range", "*": "version range", "01.2.3": "version range", "1.2.3 || 2.0.0": "version range",
	} {
		if got := versionSkip(v); got != want {
			t.Errorf("versionSkip(%q) = %q, want %q", v, got, want)
		}
	}
}

func TestEdits(t *testing.T) {
	src := "version: v1.14.0\n"
	d := dep.Dep{Version: "v1.14.0", RefSpan: dep.Span{Start: 9, End: 16}}
	edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "v1.15.1"})
	if err != nil || len(edits) != 1 || edits[0].Old != "v1.14.0" || edits[0].New != "v1.15.1" || src[edits[0].Start:edits[0].End] != "v1.14.0" {
		t.Fatalf("Edits = %+v, %v", edits, err)
	}
}
