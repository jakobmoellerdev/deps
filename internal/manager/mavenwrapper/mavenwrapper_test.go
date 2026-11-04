package mavenwrapper

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixtures (fetched 2026-11-04):
//   - testdata/spring-petclinic.properties: https://raw.githubusercontent.com/spring-projects/spring-petclinic/main/.mvn/wrapper/maven-wrapper.properties
//   - testdata/camel.properties: https://raw.githubusercontent.com/apache/camel/HEAD/.mvn/wrapper/maven-wrapper.properties
//   - testdata/dubbo.properties: https://raw.githubusercontent.com/apache/dubbo/HEAD/.mvn/wrapper/maven-wrapper.properties
//   - testdata/flink.properties: https://raw.githubusercontent.com/apache/flink/HEAD/.mvn/wrapper/maven-wrapper.properties
//
// Central publishes no .sha256 (checked 2026-11-04): apache-maven-3.9.9-bin.zip has .sha1 and
// .sha512, maven-wrapper-3.3.2.jar only .sha1. flink's distributionSha256Sum is the sha256sum
// of https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.16/apache-maven-3.9.16-bin.zip.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMatch(t *testing.T) {
	m := New()
	for path, want := range map[string]bool{
		".mvn/wrapper/maven-wrapper.properties":     true,
		"svc/.mvn/wrapper/maven-wrapper.properties": true,
		"x.mvn/wrapper/maven-wrapper.properties":    false,
		".mvn/wrapper/maven-wrapper.jar":            false,
		"maven-wrapper.properties":                  false,
	} {
		if got := m.Match(path); got != want {
			t.Errorf("Match(%q) = %v", path, got)
		}
	}
}

// want is the expected part of an extracted dep.
type want struct {
	name, depType, version, source, skip string
	line                                 int
	spans                                int // version occurrences
	checksum, artifact                   string
}

// flinkSum is flink's distributionSha256Sum (Maven 3.9.16); sum3915 the sha256sum of
// apache-maven-3.9.15-bin.zip from Central (2026-11-04).
const (
	flinkSum = "5af3b743dd8b876b5c45da33b676251e5f1687712644abb4ee519ca56e1d89ce"
	sum3915  = "b0d9292f06c5faded31ddcb6cb69099f316d6fea40a7778624b259bad9fed18a"
)

func TestExtract(t *testing.T) {
	const sum = flinkSum
	mirror := "wrapperVersion=3.3.2\ndistributionUrl=https\\://nexus.example.com/repository/maven-public/org/apache/maven/apache-maven/3.9.9/apache-maven-3.9.9-bin.zip\n" +
		"distributionSha256Sum=" + sum + "\nwrapperUrl=https://nexus.example.com/repository/maven-public/org/apache/maven/wrapper/maven-wrapper/3.3.1/maven-wrapper-3.3.1.jar\n"
	for _, tc := range []struct {
		name, src string
		want      []want
	}{
		{name: "spring-petclinic", src: fixture(t, "spring-petclinic.properties"), want: []want{
			{name: MavenName, depType: DepTypeMaven, version: "3.9.16", line: 3, spans: 2},
			{name: WrapperName, depType: DepTypeWrapper, version: "3.3.4", line: 1, spans: 1},
		}},
		{name: "camel", src: fixture(t, "camel.properties"), want: []want{
			{name: MavenName, depType: DepTypeMaven, version: "3.9.16", line: 19, spans: 2},
			{name: WrapperName, depType: DepTypeWrapper, version: "3.3.4", line: 20, spans: 3},
		}},
		{name: "dubbo", src: fixture(t, "dubbo.properties"), want: []want{
			{name: MavenName, depType: DepTypeMaven, version: "3.9.9", line: 17, spans: 2},
			{name: WrapperName, depType: DepTypeWrapper, version: "3.1.1", line: 18, spans: 2},
		}},
		{name: "flink", src: fixture(t, "flink.properties"), want: []want{
			{name: MavenName, depType: DepTypeMaven, version: "3.9.16", line: 21, spans: 2, checksum: flinkSum,
				artifact: "https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/{{version}}/apache-maven-{{version}}-bin.zip"},
			{name: WrapperName, depType: DepTypeWrapper, version: "3.3.4", line: 19, spans: 1},
		}},
		{name: "mirror", src: mirror, want: []want{
			{name: MavenName, depType: DepTypeMaven, version: "3.9.9", line: 2, spans: 2, source: "https://nexus.example.com/repository/maven-public/", checksum: sum,
				artifact: "https://nexus.example.com/repository/maven-public/org/apache/maven/apache-maven/{{version}}/apache-maven-{{version}}-bin.zip"},
			{name: WrapperName, depType: DepTypeWrapper, version: "3.3.2", line: 1, spans: 1},
			{name: WrapperName, depType: DepTypeWrapper, version: "3.3.1", line: 4, spans: 2, source: "https://nexus.example.com/repository/maven-public/"},
		}},
		{name: "unsupported", src: "distributionUrl=https://x/maven-mvnd-1.0.2-bin.zip\nwrapperUrl=https://x/org/apache/maven/wrapper/maven-wrapper/3.3.1/maven-wrapper-3.3.2.jar\nwrapperVersion=${v}\n", want: []want{
			{name: MavenName, depType: DepTypeMaven, line: 1, skip: "unsupported distributionUrl"},
			{name: WrapperName, depType: DepTypeWrapper, version: "${v}", line: 3, skip: "unsupported wrapperVersion"},
			{name: WrapperName, depType: DepTypeWrapper, line: 2, skip: "unsupported wrapperUrl"},
		}},
		{name: "bad sums", src: "distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.9/apache-maven-3.9.9-bin.zip\ndistributionSha256Sum=abc\n" +
			"wrapperUrl=https://repo1.maven.org/maven2/org/apache/maven/wrapper/maven-wrapper/3.3.1/maven-wrapper-3.3.1.jar\nwrapperSha256Sum=" + sum + "\n", want: []want{
			{name: MavenName, depType: DepTypeMaven, version: "3.9.9", line: 1, skip: "invalid distributionSha256Sum"},
			{name: WrapperName, depType: DepTypeWrapper, version: "3.3.1", line: 3, spans: 2, source: "https://repo1.maven.org/maven2/", checksum: sum,
				artifact: "https://repo1.maven.org/maven2/org/apache/maven/wrapper/maven-wrapper/{{version}}/maven-wrapper-{{version}}.jar"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds, err := New().Extract(".mvn/wrapper/maven-wrapper.properties", []byte(tc.src), manager.Env{})
			if err != nil || len(ds) != len(tc.want) {
				t.Fatalf("deps %+v, err %v", ds, err)
			}
			for i, w := range tc.want {
				d := ds[i]
				if d.Manager != Name || d.Datasource != "maven" || d.Versioning != "maven" || d.Name != w.name || d.DepType != w.depType ||
					d.Version != w.version || d.Line != w.line || d.Source != w.source || d.Skip != w.skip {
					t.Errorf("dep %d = %+v, want %+v", i, d, w)
					continue
				}
				if w.skip != "" {
					continue
				}
				n := 0
				for _, s := range []dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
					if s.End > s.Start {
						n++
						if got := tc.src[s.Start:s.End]; got != w.version {
							t.Errorf("dep %d span %v = %q", i, s, got)
						}
					}
				}
				if n != w.spans {
					t.Errorf("dep %d: %d spans, want %d", i, n, w.spans)
				}
				if d.Checksum != w.checksum || w.checksum != "" && tc.src[d.ChecksumSpan.Start:d.ChecksumSpan.End] != w.checksum {
					t.Errorf("dep %d checksum %q span %v", i, d.Checksum, d.ChecksumSpan)
				}
				if (d.ChecksumSource != nil) != (w.artifact != "") || d.ChecksumSource != nil && *d.ChecksumSource != (dep.ChecksumSource{Artifact: w.artifact}) {
					t.Errorf("dep %d ChecksumSource = %+v", i, d.ChecksumSource)
				}
			}
		})
	}
	if ds, err := New().Extract("f", []byte("distributionType=only-script\n"), manager.Env{}); err != nil || len(ds) != 0 {
		t.Errorf("no urls: %v, %v", ds, err)
	}
}

func TestEdits(t *testing.T) {
	apply := func(t *testing.T, src string, d dep.Dep, version, sum string) string {
		t.Helper()
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: version, NewChecksum: sum})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	src := fixture(t, "camel.properties")
	ds, _ := New().Extract("f", []byte(src), manager.Env{})
	out := apply(t, src, ds[1], "3.3.5", "")
	if !strings.Contains(out, "wrapperVersion=3.3.5\n") ||
		!strings.Contains(out, "wrapperUrl=https://repo.maven.apache.org/maven2/org/apache/maven/wrapper/maven-wrapper/3.3.5/maven-wrapper-3.3.5.jar\n") ||
		strings.Contains(out, "3.3.4") {
		t.Errorf("wrapper update:\n%s", out)
	}
	out = apply(t, src, ds[0], "4.0.0", "")
	if !strings.Contains(out, "distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/4.0.0/apache-maven-4.0.0-bin.zip\n") {
		t.Errorf("maven update:\n%s", out)
	}

	src = fixture(t, "flink.properties")
	ds, _ = New().Extract("f", []byte(src), manager.Env{})
	if out := apply(t, src, ds[0], "3.9.15", sum3915); out != strings.ReplaceAll(strings.ReplaceAll(src, "3.9.16", "3.9.15"), flinkSum, sum3915) {
		t.Errorf("flink update:\n%s", out)
	}
	const newSum = sum3915
	for _, tc := range []struct {
		u   dep.Update
		err string
	}{
		{dep.Update{Dep: ds[0], NewVersion: "3.9.10"}, "no checksum was resolved"},
		{dep.Update{Dep: dep.Dep{Version: "1", RefSpan: ds[0].RefSpan}, NewVersion: "2", NewChecksum: newSum}, "no checksum to update"},
	} {
		if _, err := New().Edits(tc.u); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("err = %v, want %q", err, tc.err)
		}
	}
}
