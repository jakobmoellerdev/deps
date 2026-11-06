package rpm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// syntheticPrimary is a Fedora-shaped primary.xml: n packages pkg-0…pkg-<n-1> with one build
// each, and versions builds of the package multi.
func syntheticPrimary(n, versions int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="%d">
`, n+versions)
	write := func(name string, i int) {
		fmt.Fprintf(&sb, `<package type="rpm">
  <name>%[1]s</name>
  <arch>x86_64</arch>
  <version epoch="0" ver="1.%[2]d.0" rel="%[2]d.fc43"/>
  <checksum type="sha256" pkgid="YES">%064[2]x</checksum>
  <summary>Summary of %[1]s</summary>
  <description>The %[1]s package &amp; its description.</description>
  <packager>Fedora Project</packager>
  <url>https://example.com/%[1]s</url>
  <time file="%[3]d" build="%[3]d"/>
  <size package="13447028" installed="25989839" archive="25989839"/>
  <location href="Packages/p/%[1]s-1.%[2]d.0-%[2]d.fc43.x86_64.rpm"/>
  <format>
    <rpm:license>MIT</rpm:license>
    <rpm:sourcerpm>%[1]s-1.%[2]d.0-%[2]d.fc43.src.rpm</rpm:sourcerpm>
    <rpm:header-range start="4504" end="30825"/>
    <rpm:provides>
      <rpm:entry name="%[1]s" flags="EQ" epoch="0" ver="1.%[2]d.0" rel="%[2]d.fc43"/>
      <rpm:entry name="%[1]s(x86-64)" flags="EQ" epoch="0" ver="1.%[2]d.0" rel="%[2]d.fc43"/>
    </rpm:provides>
    <rpm:requires>
      <rpm:entry name="libc.so.6()(64bit)"/>
      <rpm:entry name="rtld(GNU_HASH)"/>
    </rpm:requires>
    <file>/usr/bin/%[1]s</file>
  </format>
</package>
`, name, i, 1700000000+i)
	}
	for i := range n {
		write(fmt.Sprintf("pkg-%d", i), i)
	}
	for i := range versions {
		write("multi", i)
	}
	sb.WriteString("</metadata>\n")
	return sb.String()
}

// serveSynthetic serves one repository per path prefix, each with primary as its plain
// primary.xml.
func serveSynthetic(b *testing.B, primary string) *httptest.Server {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/repodata/repomd.xml"):
			_, _ = io.WriteString(w, `<repomd><data type="primary"><location href="repodata/primary.xml"/></data></repomd>`)
		case strings.HasSuffix(r.URL.Path, "/repodata/primary.xml"):
			_, _ = io.WriteString(w, primary)
		default:
			http.NotFound(w, r)
		}
	}))
	b.Cleanup(srv.Close)
	return srv
}

// BenchmarkReleasesLargeRepo indexes a 20000-package repository for the two packages a run
// asks for, as a run's first lookup does.
func BenchmarkReleasesLargeRepo(b *testing.B) {
	primary := syntheticPrimary(20000, 20)
	srv := serveSynthetic(b, primary)
	deps := []dep.Dep{
		{Name: "pkg-10000", Source: srv.URL + "/fedora/repodata/"},
		{Name: "multi", Source: srv.URL + "/fedora/repodata/"},
	}
	ctx := context.Background()
	b.SetBytes(int64(len(primary)))
	b.ReportAllocs()
	for b.Loop() {
		s := New(srv.Client(), nil)
		if p, ok := any(s).(datasource.Planner); ok {
			p.Plan(deps)
		}
		for _, d := range deps {
			if _, err := s.Releases(ctx, d, ""); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkReleaseTimeMerged looks up the releases of a package listed by a release and an
// updates repository, then the time of every release, as the engine does for its candidates.
func BenchmarkReleaseTimeMerged(b *testing.B) {
	srv := serveSynthetic(b, syntheticPrimary(10, 300))
	d := dep.Dep{Name: "multi", Source: srv.URL + "/release/repodata/ " + srv.URL + "/updates/repodata/"}
	s := New(srv.Client(), nil)
	ctx := context.Background()
	if _, err := s.Releases(ctx, d, ""); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		rels, err := s.Releases(ctx, d, "")
		if err != nil {
			b.Fatal(err)
		}
		for _, r := range rels {
			if _, err := s.ReleaseTime(ctx, d, r.Version); err != nil {
				b.Fatal(err)
			}
		}
	}
}
