package triage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// counting serves the three risk endpoints and counts the deps.dev advisory requests.
type counting struct {
	depsDev atomic.Int64
	all     atomic.Int64
}

func (c *counting) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.all.Add(1)
	switch {
	case strings.HasPrefix(r.URL.Path, "/v3/advisories/"):
		c.depsDev.Add(1)
		id := strings.TrimPrefix(r.URL.Path, "/v3/advisories/")
		n := strings.TrimPrefix(id, "GHSA-")
		fmt.Fprintf(w, `{"advisoryKey":{"id":%q},"aliases":["CVE-%s"],"cvss3Score":7.5}`, id, n)
	case r.URL.Path == "/data/v1/epss":
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i, cve := range strings.Split(r.URL.Query().Get("cve"), ",") {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"cve":%q,"epss":"0.100000000"}`, cve)
		}
		b.WriteString(`]}`)
		_, _ = w.Write([]byte(b.String()))
	case r.URL.Path == "/kev.json":
		_, _ = w.Write([]byte(`{"count":1,"vulnerabilities":[{"cveID":"CVE-1"}]}`))
	default:
		http.NotFound(w, r)
	}
}

// scoredAdvisories is one advisory per dep, each already scored by its own CVSS vector and
// with a CVE alias, as a GHSA record carries them.
func scoredAdvisories(n int) *stub {
	vulns := make([][]datasource.Vuln, n)
	for i := range vulns {
		vulns[i] = []datasource.Vuln{{
			Advisory: dep.Advisory{ID: fmt.Sprintf("GHSA-%d", i), Summary: "s", CVSS: 7.5, Aliases: []string{fmt.Sprintf("CVE-%d", i)}},
			Fixed:    []string{"1.0.1"},
		}}
	}
	return &stub{vulns: vulns}
}

func benchServer(tb testing.TB) (func(src datasource.SecuritySource) datasource.SecuritySource, *counting) {
	tb.Helper()
	c := &counting{}
	srv := httptest.NewServer(c)
	tb.Cleanup(srv.Close)
	return func(src datasource.SecuritySource) datasource.SecuritySource {
		return newSource(src, srv.Client(), make(chan struct{}, 8), srv.URL, srv.URL, srv.URL+"/kev.json")
	}, c
}

// BenchmarkAdvisories200 reports the requests 200 distinct scored advisories cost.
func BenchmarkAdvisories200(b *testing.B) {
	build, c := benchServer(b)
	deps := make([]dep.Dep, 200)
	ctx := context.Background()
	n := 0
	c.all.Store(0)
	c.depsDev.Store(0)
	for b.Loop() {
		s := build(scoredAdvisories(200))
		if _, err := s.Advisories(ctx, deps); err != nil {
			b.Fatal(err)
		}
		n++
	}
	b.ReportMetric(float64(c.all.Load())/float64(n), "reqs/op")
	b.ReportMetric(float64(c.depsDev.Load())/float64(n), "depsdevreqs/op")
}

// TestScoreSkippedWhenComplete pins the annotations: an advisory that carries a score and a
// CVE alias is not looked up, one that is missing either still is, and both end up with the
// same EPSS and KEV data as before.
func TestScoreSkippedWhenComplete(t *testing.T) {
	build, c := benchServer(t)
	src := &stub{vulns: [][]datasource.Vuln{{
		{Advisory: dep.Advisory{ID: "GHSA-1", Summary: "s", CVSS: 9.8, Aliases: []string{"CVE-1"}}, Fixed: []string{"1.0.1"}}, // complete
		{Advisory: dep.Advisory{ID: "GHSA-2", Summary: "s", Aliases: []string{"CVE-2"}}, Fixed: []string{"1.0.1"}},            // no score
		{Advisory: dep.Advisory{ID: "GHSA-3", Summary: "s", CVSS: 5.0, Aliases: []string{"GO-3"}}, Fixed: []string{"1.0.1"}},  // no CVE alias
		{Advisory: dep.Advisory{ID: "CVE-4", Summary: "s", CVSS: 4.0, Aliases: []string{"GHSA-4"}}, Fixed: []string{"1.0.1"}}, // its own ID is the CVE
	}}}
	got, err := build(src).Advisories(t.Context(), make([]dep.Dep, 1))
	if err != nil {
		t.Fatal(err)
	}
	if n := c.depsDev.Load(); n != 2 {
		t.Errorf("deps.dev requests = %d, want 2 (GHSA-2 and GHSA-3)", n)
	}
	for i, want := range []float64{9.8, 7.5, 5.0, 4.0} {
		if a := got[0][i].Advisory; a.CVSS != want {
			t.Errorf("%s CVSS = %v, want %v", a.ID, a.CVSS, want)
		}
	}
	first := got[0][0].Advisory
	if first.EPSS != 0.1 || !first.KEV {
		t.Errorf("GHSA-1 = EPSS %v, KEV %v, want the EPSS and KEV of CVE-1", first.EPSS, first.KEV)
	}
	// The lookup of GHSA-3 still adds the CVE alias deps.dev knows, and it keys EPSS.
	third := got[0][2].Advisory
	if len(third.Aliases) != 2 || third.Aliases[1] != "CVE-3" || third.EPSS != 0.1 {
		t.Errorf("GHSA-3 = %+v, want the CVE-3 alias and its EPSS", third)
	}
}
