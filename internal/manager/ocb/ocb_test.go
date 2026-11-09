package ocb

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

// Fixtures, copied unchanged (Apache-2.0):
//   - testdata/otelcol-manifest.yaml is distributions/otelcol/manifest.yaml of
//     github.com/open-telemetry/opentelemetry-collector-releases@b9425f2;
//   - testdata/otelcorecol-builder-config.yaml is cmd/otelcorecol/builder-config.yaml of
//     github.com/open-telemetry/opentelemetry-collector@v0.80.0.

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// summarize renders each dep as "<line> <depType> <name> <version> <versioning> [<skip>]" and
// checks that RefSpan covers the version.
func summarize(t *testing.T, file string, src []byte, deps []dep.Dep) []string {
	t.Helper()
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if d.Manager != Name || d.Datasource != "go" || d.File != file {
			t.Errorf("%s: manager %q datasource %q file %q", d.Name, d.Manager, d.Datasource, d.File)
		}
		s := fmt.Sprintf("%d %s %s %s %s", d.Line, d.DepType, d.Name, d.Version, d.Versioning)
		if d.Skip != "" {
			s += " [" + d.Skip + "]"
		} else if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Version {
			t.Errorf("%s: span %q", d.Name, got)
		}
		out = append(out, s)
	}
	return out
}

func extract(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	deps, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

func TestManifest(t *testing.T) {
	const file = "distributions/otelcol/manifest.yaml"
	src := read(t, "otelcol-manifest.yaml")
	got := summarize(t, file, src, extract(t, file, src))
	if len(got) != 36 {
		t.Errorf("got %d deps, want 36", len(got))
	}
	for _, w := range []string{
		"10 receivers go.opentelemetry.io/collector/receiver/nopreceiver v0.165.0 go",
		"20 exporters go.opentelemetry.io/collector/exporter/debugexporter v0.165.0 go",
		"28 exporters github.com/open-telemetry/opentelemetry-collector-contrib/exporter/zipkinexporter v0.165.0 go",
	} {
		if !strings.Contains(strings.Join(got, "\n"), w) {
			t.Errorf("missing %q in\n%s", w, strings.Join(got, "\n"))
		}
	}
	counts := map[string]int{}
	for _, d := range extract(t, file, src) {
		counts[d.DepType]++
		if d.Skip != "" {
			t.Errorf("%s skipped: %s", d.Name, d.Skip)
		}
	}
	for _, dt := range componentLists {
		if counts[dt] == 0 {
			t.Errorf("no %s deps", dt)
		}
	}
}

func TestBuilderConfigWithReplaces(t *testing.T) {
	const file = "cmd/otelcorecol/builder-config.yaml"
	src := read(t, "otelcorecol-builder-config.yaml")
	got := summarize(t, file, src, extract(t, file, src))
	want := []string{
		"6 collector go.opentelemetry.io/collector 0.80.0 semver",
		"21 connectors go.opentelemetry.io/collector/connector/forwardconnector v0.80.0 go [replaced]",
		"11 exporters go.opentelemetry.io/collector/exporter/loggingexporter v0.80.0 go [replaced]",
		"12 exporters go.opentelemetry.io/collector/exporter/otlpexporter v0.80.0 go [replaced]",
		"13 exporters go.opentelemetry.io/collector/exporter/otlphttpexporter v0.80.0 go [replaced]",
		"15 extensions go.opentelemetry.io/collector/extension/ballastextension v0.80.0 go [replaced]",
		"16 extensions go.opentelemetry.io/collector/extension/zpagesextension v0.80.0 go [replaced]",
		"18 processors go.opentelemetry.io/collector/processor/batchprocessor v0.80.0 go [replaced]",
		"19 processors go.opentelemetry.io/collector/processor/memorylimiterprocessor v0.80.0 go [replaced]",
		"9 receivers go.opentelemetry.io/collector/receiver/otlpreceiver v0.80.0 go [replaced]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if deps := extract(t, file, src); deps[0].ExtractVersion != goExtract {
		t.Errorf("collector extractVersion = %q", deps[0].ExtractVersion)
	}
}

func TestEntryForms(t *testing.T) {
	src := []byte(`dist:
  otelcol_version: "0.110.0"
receivers:
  - gomod: "go.opentelemetry.io/collector/receiver/otlpreceiver   v0.110.0"
  - gomod: github.com/acme/receiver
  - gomod: github.com/acme/local v0.1.0
    path: ./local
  - gomod: "github.com/acme/escaped\u0020v1.0.0"
  - gomod: ""
  - name: no-gomod
processors: notalist
providers:
  - gomod: go.opentelemetry.io/collector/confmap/provider/envprovider v1.18.0
`)
	got := summarize(t, "b.yaml", src, extract(t, "b.yaml", src))
	want := []string{
		"2 collector go.opentelemetry.io/collector 0.110.0 semver",
		"13 providers go.opentelemetry.io/collector/confmap/provider/envprovider v1.18.0 go",
		"4 receivers go.opentelemetry.io/collector/receiver/otlpreceiver v0.110.0 go",
		"5 receivers github.com/acme/receiver  go [no version]",
		"6 receivers github.com/acme/local v0.1.0 go [local module]",
		"8 receivers github.com/acme/escaped v1.0.0 go [cannot locate value]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	deps := extract(t, "b.yaml", src)
	var edits []patch.Edit
	for _, d := range deps[:3] {
		e, err := New().Edits(dep.Update{Dep: d, NewVersion: map[string]string{"0.110.0": "0.115.0", "v1.18.0": "v1.21.0", "v0.110.0": "v0.115.0"}[d.Version]})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply(src, edits)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`otelcol_version: "0.115.0"`, `otlpreceiver   v0.115.0"`, `envprovider v1.21.0`} {
		if !strings.Contains(string(out), s) {
			t.Errorf("patched lacks %s:\n%s", s, out)
		}
	}
}

func TestNotBuilderConfig(t *testing.T) {
	for _, src := range []string{
		"apiVersion: v1\nkind: ConfigMap\n",
		"dist: x\nreceivers:\n  - gomod: a v1\n",
		"receivers:\n  - gomod: a v1\n# dist:\n",
		"dist: {\n  gomod: [\n",
		"- dist:\n- gomod:\n",
	} {
		if deps := extract(t, "x.yaml", []byte(src)); len(deps) != 0 {
			t.Errorf("%q: %+v", src, deps)
		}
	}
}

func TestMatch(t *testing.T) {
	m := New()
	if m.Name() != Name {
		t.Errorf("Name() = %q", m.Name())
	}
	for p, want := range map[string]bool{
		"builder-config.yaml": true, "distributions/otelcol/manifest.yaml": true, "ocb.yml": true, "go.mod": false, "x.yaml.bak": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
}
