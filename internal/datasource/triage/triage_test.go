package triage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// stub is the wrapped security source.
type stub struct {
	vulns [][]datasource.Vuln
	err   error
	calls int
}

func (s *stub) Advisories(context.Context, []dep.Dep) ([][]datasource.Vuln, error) {
	s.calls++
	return s.vulns, s.err
}

// fake serves the three risk endpoints and records the requests.
type fake struct {
	t      *testing.T
	mu     sync.Mutex
	paths  []string
	status map[string]int
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.RequestURI())
	f.mu.Unlock()
	if code := f.status[r.URL.Path]; code != 0 {
		w.WriteHeader(code)
		return
	}
	switch {
	case r.URL.Path == "/v3/advisories/GHSA-1":
		_, _ = w.Write([]byte(`{"advisoryKey":{"id":"GHSA-1"},"aliases":["CVE-1","GO-1"],"cvss3Score":9.8,"cvss3Vector":"CVSS:3.1/AV:N"}`))
	case r.URL.Path == "/v3/advisories/GHSA-2":
		_, _ = w.Write([]byte(`{"advisoryKey":{"id":"GHSA-2"},"aliases":["CVE-2"],"cvss3Score":4.3}`))
	case strings.HasPrefix(r.URL.Path, "/v3/advisories/"):
		http.NotFound(w, r)
	case r.URL.Path == "/data/v1/epss":
		cves := r.URL.Query().Get("cve")
		if !strings.Contains(cves, "CVE-1") {
			f.t.Errorf("epss query = %q", cves)
		}
		_, _ = w.Write([]byte(`{"data":[{"cve":"CVE-1","epss":"0.942100000","percentile":"0.99"},{"cve":"CVE-2","epss":"0.000210000"}]}`))
	case r.URL.Path == "/kev.json":
		_, _ = w.Write([]byte(`{"count":2,"vulnerabilities":[{"cveID":"CVE-1"},{"cveID":"CVE-9"}]}`))
	default:
		f.t.Errorf("unexpected %s", r.URL)
		w.WriteHeader(http.StatusTeapot)
	}
}

func start(t *testing.T, src datasource.SecuritySource, status map[string]int) (datasource.SecuritySource, *fake) {
	f := &fake{t: t, status: status}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return newSource(src, srv.Client(), make(chan struct{}, 4), srv.URL, srv.URL, srv.URL+"/kev.json"), f
}

func vuln(id string, aliases ...string) datasource.Vuln {
	return datasource.Vuln{Advisory: dep.Advisory{ID: id, Summary: "s", Aliases: aliases}, Fixed: []string{"1.0.1"}}
}

func TestAdvisoriesEnriched(t *testing.T) {
	src := &stub{vulns: [][]datasource.Vuln{
		{vuln("GHSA-1"), vuln("GHSA-2", "CVE-2")},
		{vuln("GHSA-1")}, // the same advisory on another dep gets the same data
		nil,
		{vuln("OSV-NONE")}, // no GHSA ID, no CVE alias: nothing to look up
	}}
	s, f := start(t, src, nil)
	got, err := s.Advisories(t.Context(), make([]dep.Dep, 4))
	if err != nil {
		t.Fatal(err)
	}
	first := got[0][0].Advisory
	if first.CVSS != 9.8 || first.Severity() != "critical" || !first.KEV {
		t.Errorf("GHSA-1 = %+v", first)
	}
	if first.EPSS != 0.9421 {
		t.Errorf("GHSA-1 EPSS = %v", first.EPSS)
	}
	if want := []string{"CVE-1", "GO-1"}; !reflect.DeepEqual(first.Aliases, want) {
		t.Errorf("GHSA-1 aliases = %v, want %v", first.Aliases, want)
	}
	if risk := first.Risk(); risk != "CVSS 9.8 critical, EPSS 94%, CISA KEV" {
		t.Errorf("GHSA-1 risk = %q", risk)
	}
	second := got[0][1].Advisory
	if second.CVSS != 4.3 || second.Severity() != "medium" || second.KEV {
		t.Errorf("GHSA-2 = %+v", second)
	}
	if risk := second.Risk(); risk != "CVSS 4.3 medium, EPSS <0.1%" {
		t.Errorf("GHSA-2 risk = %q", risk)
	}
	if other := got[1][0].Advisory; other.CVSS != 9.8 || !other.KEV {
		t.Errorf("second dep's GHSA-1 = %+v", other)
	}
	if none := got[3][0].Advisory; none.CVSS != 0 || none.KEV || none.Risk() != "" {
		t.Errorf("OSV-NONE = %+v", none)
	}
	// One deps.dev request per advisory with a GHSA ID, one EPSS batch, one KEV catalog.
	if len(f.paths) != 4 {
		t.Errorf("requests = %v", f.paths)
	}
}

func TestAdvisoriesRiskSourcesFail(t *testing.T) {
	src := &stub{vulns: [][]datasource.Vuln{{vuln("GHSA-1", "CVE-1")}}}
	s, _ := start(t, src, map[string]int{"/v3/advisories/GHSA-1": 500, "/data/v1/epss": 503, "/kev.json": 404})
	got, err := s.Advisories(t.Context(), make([]dep.Dep, 1))
	if err != nil {
		t.Fatalf("failing risk lookups must not fail the run: %v", err)
	}
	want := [][]datasource.Vuln{{vuln("GHSA-1", "CVE-1")}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAdvisoriesWrappedError(t *testing.T) {
	src := &stub{err: http.ErrServerClosed}
	s, f := start(t, src, nil)
	if _, err := s.Advisories(t.Context(), nil); err == nil {
		t.Fatal("want the wrapped source's error")
	}
	if len(f.paths) != 0 {
		t.Errorf("requests after a failed lookup = %v", f.paths)
	}
}
