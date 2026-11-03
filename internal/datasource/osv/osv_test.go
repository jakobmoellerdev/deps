package osv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// fakeOSV serves /v1/querybatch from results (keyed by "ecosystem|name|version|pageToken")
// and /v1/vulns/{id} from vulns. It records every batch and every vuln fetch.
type fakeOSV struct {
	t       *testing.T
	results map[string]result
	vulns   map[string]string // id -> JSON document
	status  map[string]int    // path -> forced status

	mu      sync.Mutex
	batches [][]query
	fetches map[string]int
}

type result struct {
	ids   []string
	token string
}

func newFake(t *testing.T) *fakeOSV {
	return &fakeOSV{t: t, results: map[string]result{}, vulns: map[string]string{}, status: map[string]int{}, fetches: map[string]int{}}
}

func key(q query) string {
	return q.Package.Ecosystem + "|" + q.Package.Name + "|" + q.Version + "|" + q.PageToken
}

func (f *fakeOSV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if code := f.status[r.URL.Path]; code != 0 {
		w.WriteHeader(code)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/querybatch":
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			f.t.Errorf("Content-Type = %q", ct)
		}
		var req struct{ Queries []query }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("decode: %v", err)
		}
		f.mu.Lock()
		f.batches = append(f.batches, req.Queries)
		f.mu.Unlock()
		var b strings.Builder
		b.WriteString(`{"results":[`)
		for i, q := range req.Queries {
			if i > 0 {
				b.WriteByte(',')
			}
			res := f.results[key(q)]
			b.WriteString(`{"vulns":[`)
			for j, id := range res.ids {
				if j > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"id":%q,"modified":"2026-01-01T00:00:00Z"}`, id)
			}
			b.WriteString("]")
			if res.token != "" {
				fmt.Fprintf(&b, `,"next_page_token":%q`, res.token)
			}
			b.WriteString("}")
		}
		b.WriteString("]}")
		_, _ = w.Write([]byte(b.String()))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/vulns/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/vulns/")
		f.mu.Lock()
		f.fetches[id]++
		f.mu.Unlock()
		doc, ok := f.vulns[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(doc))
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func (f *fakeOSV) start(sem chan struct{}) *Source {
	srv := httptest.NewServer(f)
	f.t.Cleanup(srv.Close)
	return newSource(srv.Client(), sem, srv.URL)
}

func goDep(name, version string) dep.Dep {
	return dep.Dep{Manager: "gomod", DepType: "direct", Name: name, Version: version}
}

func actionDep(name, version string) dep.Dep {
	owner, rest, _ := strings.Cut(name, "/")
	repo, _, _ := strings.Cut(rest, "/")
	return dep.Dep{Manager: "github-actions", DepType: "action", Name: name, Source: "https://github.com/" + owner + "/" + repo, Version: version}
}

func goVuln(id string, aliases []string, pkgName string, fixed ...string) string {
	var events []string
	events = append(events, `{"introduced":"0"}`)
	for _, f := range fixed {
		events = append(events, fmt.Sprintf(`{"fixed":%q}`, f))
	}
	al, _ := json.Marshal(aliases)
	return fmt.Sprintf(`{"id":%q,"summary":"summary of %s","aliases":%s,
		"references":[{"type":"REPORT","url":"https://example.com/report"},{"type":"WEB","url":"https://example.com/%s"}],
		"affected":[{"package":{"name":"stdlib","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"fixed":"1.21.9"}]}]},
		            {"package":{"name":%q,"ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[%s]},{"type":"GIT","events":[{"fixed":"abc"}]}]}]}`,
		id, id, al, id, pkgName, strings.Join(events, ","))
}

func actionVuln(id, pkgName string, ranges ...string) string {
	return fmt.Sprintf(`{"id":%q,"summary":"s","affected":[{"package":{"name":%q,"ecosystem":"GitHub Actions"},"ranges":[%s]}]}`,
		id, pkgName, strings.Join(ranges, ","))
}

func eco(events ...string) string {
	return `{"type":"ECOSYSTEM","events":[` + strings.Join(events, ",") + `]}`
}

func ids(vs []datasource.Vuln) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.Advisory.ID)
	}
	return out
}

func TestAdvisoriesGo(t *testing.T) {
	f := newFake(t)
	f.results["Go|golang.org/x/net|v0.17.0|"] = result{ids: []string{"GHSA-4v7x-pqxf-cx7m", "GO-2024-2687", "GO-2024-3333", "GO-WD"}}
	f.results["Go|example.com/sub|v1.0.0|"] = result{ids: []string{"GO-SUB"}}
	f.vulns["GO-2024-2687"] = goVuln("GO-2024-2687", []string{"CVE-2023-45288", "GHSA-4v7x-pqxf-cx7m"}, "golang.org/x/net", "0.23.0")
	f.vulns["GHSA-4v7x-pqxf-cx7m"] = goVuln("GHSA-4v7x-pqxf-cx7m", []string{"CVE-2023-45288"}, "golang.org/x/net", "0.23.0")
	f.vulns["GO-2024-3333"] = goVuln("GO-2024-3333", nil, "golang.org/x/net", "v0.33.0", "0.33.0", "1.2.0")
	f.vulns["GO-WD"] = `{"id":"GO-WD","withdrawn":"2025-01-01T00:00:00Z","affected":[{"package":{"name":"golang.org/x/net","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"fixed":"0.30.0"}]}]}]}`
	// Only a package inside the module is listed, as GHSA does for Go.
	f.vulns["GO-SUB"] = `{"id":"GO-SUB","details":"first line\nsecond","affected":[{"package":{"name":"example.com/sub/pkg","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"fixed":"1.0.1"}]}]},{"package":{"name":"example.com/subother","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"fixed":"9.0.0"}]}]}]}`
	s := f.start(make(chan struct{}, 2))

	deps := []dep.Dep{
		goDep("golang.org/x/net", "v0.17.0"),
		{Manager: "gomod", DepType: "indirect", Name: "example.com/sub", Version: "v1.0.0"},
		{Manager: "gomod", DepType: "go", Name: "go", Version: "1.22"},
		goDep("golang.org/x/net", "v0.17.0"),
		{Manager: "docker", Name: "golang", Version: "1.22"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]datasource.Vuln{
		{
			{Advisory: dep.Advisory{ID: "GO-2024-2687", Summary: "summary of GO-2024-2687", URL: "https://example.com/GO-2024-2687"}, Fixed: []string{"v0.23.0"}},
			{Advisory: dep.Advisory{ID: "GO-2024-3333", Summary: "summary of GO-2024-3333", URL: "https://example.com/GO-2024-3333"}, Fixed: []string{"v0.33.0", "v1.2.0"}},
		},
		{{Advisory: dep.Advisory{ID: "GO-SUB", Summary: "first line", URL: "https://osv.dev/vulnerability/GO-SUB"}, Fixed: []string{"v1.0.1"}}},
		nil,
		nil, // filled below: same as deps[0]
		nil,
	}
	want[3] = want[0]
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 2 {
		t.Errorf("batches = %+v, want one batch with 2 distinct queries", f.batches)
	}
	for id, n := range f.fetches {
		if n != 1 {
			t.Errorf("%s fetched %d times", id, n)
		}
	}
	if len(f.fetches) != 5 {
		t.Errorf("fetched %v, want 5 distinct ids", f.fetches)
	}

	// A second call reuses the fetched advisories.
	if _, err := s.Advisories(t.Context(), deps[:1]); err != nil {
		t.Fatal(err)
	}
	if f.fetches["GO-2024-2687"] != 1 {
		t.Errorf("advisory refetched: %v", f.fetches)
	}
}

func TestAdvisoriesActions(t *testing.T) {
	f := newFake(t)
	f.results["GitHub Actions|actions/download-artifact||"] = result{ids: []string{"GHSA-cxww-7g56-2vh6"}}
	f.results["GitHub Actions|tj-actions/changed-files||"] = result{ids: []string{"GHSA-mcph", "GHSA-mrrh", "GHSA-la", "GHSA-vers", "GHSA-bad"}}
	f.results["GitHub Actions|github/codeql-action||"] = result{ids: []string{"GHSA-codeql"}}
	f.vulns["GHSA-cxww-7g56-2vh6"] = `{"id":"GHSA-cxww-7g56-2vh6","summary":"Arbitrary File Write",
		"references":[{"type":"PACKAGE","url":"https://github.com/actions/download-artifact"},{"type":"ADVISORY","url":"https://github.com/advisories/x"}],
		"affected":[{"package":{"name":"actions/download-artifact","ecosystem":"GitHub Actions"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"4.0.0"},{"fixed":"4.1.3"}]}]}]}`
	f.vulns["GHSA-mcph"] = actionVuln("GHSA-mcph", "tj-actions/changed-files", eco(`{"introduced":"0"}`, `{"fixed":"41"}`))
	f.vulns["GHSA-mrrh"] = actionVuln("GHSA-mrrh", "tj-actions/changed-files", eco(`{"fixed":"46.0.1"}`, `{"introduced":"0"}`))
	f.vulns["GHSA-la"] = actionVuln("GHSA-la", "tj-actions/changed-files", eco(`{"introduced":"30.0.0"}`, `{"last_affected":"35.0.0"}`))
	f.vulns["GHSA-vers"] = `{"id":"GHSA-vers","affected":[{"package":{"name":"tj-actions/changed-files","ecosystem":"GitHub Actions"},"versions":["35.1.0"]}]}`
	// Unparsable range versions make the range unusable.
	f.vulns["GHSA-bad"] = actionVuln("GHSA-bad", "tj-actions/changed-files", eco(`{"introduced":"0"}`, `{"fixed":"47.0.0-rc1"}`), `{"type":"GIT","events":[{"introduced":"0"}]}`)
	f.vulns["GHSA-codeql"] = actionVuln("GHSA-codeql", "github/codeql-action", eco(`{"introduced":"3.0.0"}`, `{"fixed":"3.1.0"}`, `{"limit":"4.0.0"}`))
	s := f.start(nil)

	deps := []dep.Dep{
		actionDep("actions/download-artifact", "4.1.2"),                                                                    // 0: affected, no v
		actionDep("actions/download-artifact", "v4.1.3"),                                                                   // 1: fixed
		actionDep("actions/download-artifact", "v4"),                                                                       // 2: floating, line has a fix
		actionDep("actions/download-artifact", "v3.0.0"),                                                                   // 3: before introduced
		actionDep("tj-actions/changed-files", "v35.0.0"),                                                                   // 4
		actionDep("tj-actions/changed-files", "v35"),                                                                       // 5: floating, whole line affected
		actionDep("tj-actions/changed-files", "v35.1.0"),                                                                   // 6: explicit version
		actionDep("github/codeql-action/init", "v3.0.2"),                                                                   // 7: subpath dropped
		{Manager: "github-actions", Name: "actions/checkout", Source: "https://gitea.com/actions/checkout", Version: "v4"}, // 8: not github.com
		actionDep("actions/checkout", "main"),                                                                              // 9: not a version
		{Manager: "github-actions", Name: "solo", Source: "https://github.com/solo", Version: "v1"},                        // 10: no repo
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := make([][]string, len(got))
	for i, g := range got {
		gotIDs[i] = ids(g)
	}
	wantIDs := [][]string{
		{"GHSA-cxww-7g56-2vh6"}, {}, {}, {},
		{"GHSA-la", "GHSA-mcph", "GHSA-mrrh"},
		{"GHSA-mcph", "GHSA-mrrh"},
		{"GHSA-mcph", "GHSA-mrrh", "GHSA-vers"},
		{"GHSA-codeql"},
		{}, {}, {},
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("ids = %v, want %v", gotIDs, wantIDs)
	}
	fixed := func(i int, id string) []string {
		for _, v := range got[i] {
			if v.Advisory.ID == id {
				return v.Fixed
			}
		}
		return nil
	}
	for _, c := range []struct {
		dep  int
		id   string
		want []string
	}{
		{0, "GHSA-cxww-7g56-2vh6", []string{"4.1.3"}},
		{4, "GHSA-mcph", []string{"v41.0.0"}},
		{4, "GHSA-mrrh", []string{"v46.0.1"}},
		{4, "GHSA-la", nil},
		{5, "GHSA-mrrh", []string{"v46"}},
		{7, "GHSA-codeql", []string{"v3.1.0"}},
	} {
		if f := fixed(c.dep, c.id); !reflect.DeepEqual(f, c.want) {
			t.Errorf("dep %d %s: Fixed = %q, want %q", c.dep, c.id, f, c.want)
		}
	}
	if a := got[0][0].Advisory; a.URL != "https://github.com/advisories/x" || a.Summary != "Arbitrary File Write" {
		t.Errorf("advisory = %+v", a)
	}
	for _, q := range f.batches[0] {
		if q.Version != "" {
			t.Errorf("GitHub Actions query %+v carries a version", q)
		}
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 3 {
		t.Errorf("batches = %+v, want one batch with 3 packages", f.batches)
	}
}

func TestAdvisoriesBatchingAndPagination(t *testing.T) {
	f := newFake(t)
	var deps []dep.Dep
	for i := range 5 {
		name := fmt.Sprintf("example.com/m%d", i)
		deps = append(deps, goDep(name, "v1.0.0"))
		id := fmt.Sprintf("GO-%d", i)
		f.vulns[id] = goVuln(id, nil, name, "1.0.1")
		f.results["Go|"+name+"|v1.0.0|"] = result{ids: []string{id}}
	}
	// m1 has two more pages, m3 one more; the shared id is fetched once.
	f.results["Go|example.com/m1|v1.0.0|"] = result{ids: []string{"GO-1"}, token: "p2"}
	f.results["Go|example.com/m1|v1.0.0|p2"] = result{ids: []string{"GO-SHARED"}, token: "p3"}
	f.results["Go|example.com/m1|v1.0.0|p3"] = result{ids: []string{"GO-1b"}}
	f.results["Go|example.com/m3|v1.0.0|"] = result{ids: []string{"GO-3"}, token: "x"}
	f.results["Go|example.com/m3|v1.0.0|x"] = result{ids: []string{"GO-SHARED"}}
	f.vulns["GO-SHARED"] = `{"id":"GO-SHARED","affected":[{"package":{"name":"example.com/m1","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"fixed":"1.0.2"}]}]},{"package":{"name":"example.com/m3","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"fixed":"1.0.3"}]}]}]}`
	f.vulns["GO-1b"] = goVuln("GO-1b", nil, "example.com/m1", "1.0.4")
	s := f.start(make(chan struct{}, 1))
	s.batch = 2

	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	var tokens []string
	for _, b := range f.batches {
		sizes = append(sizes, len(b))
		for _, q := range b {
			if q.PageToken != "" {
				tokens = append(tokens, q.Package.Name+"@"+q.PageToken)
			}
		}
	}
	if want := []int{2, 2, 1, 2, 1}; !reflect.DeepEqual(sizes, want) {
		t.Errorf("batch sizes = %v, want %v", sizes, want)
	}
	if want := []string{"example.com/m1@p2", "example.com/m3@x", "example.com/m1@p3"}; !reflect.DeepEqual(tokens, want) {
		t.Errorf("page tokens = %v, want %v", tokens, want)
	}
	if want := []string{"GO-1", "GO-1b", "GO-SHARED"}; !reflect.DeepEqual(ids(got[1]), want) {
		t.Errorf("m1 = %v, want %v", ids(got[1]), want)
	}
	if want := []string{"GO-3", "GO-SHARED"}; !reflect.DeepEqual(ids(got[3]), want) {
		t.Errorf("m3 = %v, want %v", ids(got[3]), want)
	}
	if got[3][1].Fixed[0] != "v1.0.3" {
		t.Errorf("m3 GO-SHARED Fixed = %v, want v1.0.3 (per-package fixes)", got[3][1].Fixed)
	}
	if f.fetches["GO-SHARED"] != 1 || len(f.fetches) != 7 {
		t.Errorf("fetches = %v", f.fetches)
	}
}

func TestAdvisoriesNothingToQuery(t *testing.T) {
	f := newFake(t)
	s := f.start(nil)
	got, err := s.Advisories(t.Context(), []dep.Dep{{Manager: "custom", Name: "x", Version: "1"}, goDep("", "v1.0.0")})
	if err != nil || len(got) != 2 || got[0] != nil || got[1] != nil {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(f.batches) != 0 {
		t.Errorf("unexpected batches %v", f.batches)
	}
}

func TestAdvisoriesErrors(t *testing.T) {
	deps := []dep.Dep{goDep("example.com/m", "v1.0.0")}
	for _, c := range []struct {
		name  string
		setup func(f *fakeOSV)
		want  string
	}{
		{"querybatch status", func(f *fakeOSV) { f.status["/v1/querybatch"] = http.StatusInternalServerError }, "/v1/querybatch: HTTP 500"},
		{"vuln missing", func(f *fakeOSV) { f.results["Go|example.com/m|v1.0.0|"] = result{ids: []string{"GO-A", "GO-B"}} }, "/v1/vulns/GO-A: HTTP 404\nGET "},
		{"vuln malformed", func(f *fakeOSV) {
			f.results["Go|example.com/m|v1.0.0|"] = result{ids: []string{"GO-A"}}
			f.vulns["GO-A"] = `{"id":`
		}, "/v1/vulns/GO-A: unexpected EOF"},
		{"repeated token", func(f *fakeOSV) {
			f.results["Go|example.com/m|v1.0.0|"] = result{token: "t"}
			f.results["Go|example.com/m|v1.0.0|t"] = result{token: "t"}
		}, "querybatch: example.com/m: repeated page token"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t)
			c.setup(f)
			_, err := f.start(nil).Advisories(t.Context(), deps)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want containing %q", err, c.want)
			}
		})
	}
}

func TestAdvisoriesResultMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	_, err := newSource(srv.Client(), nil, srv.URL).Advisories(t.Context(), []dep.Dep{goDep("example.com/m", "v1.0.0")})
	if err == nil || !strings.Contains(err.Error(), "got 0 results for 1 queries") {
		t.Errorf("err = %v", err)
	}
}

func TestAdvisoriesTransportError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	_, err := newSource(srv.Client(), nil, srv.URL).Advisories(t.Context(), []dep.Dep{goDep("example.com/m", "v1.0.0")})
	if err == nil || !strings.Contains(err.Error(), "connect") {
		t.Errorf("err = %v", err)
	}
	_, err = newSource(srv.Client(), nil, "://bad").Advisories(t.Context(), []dep.Dep{goDep("example.com/m", "v1.0.0")})
	if err == nil {
		t.Error("want error for invalid base URL")
	}
}

func TestAdvisoriesCanceled(t *testing.T) {
	f := newFake(t)
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // full: acquire must honor ctx
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, s := range []*Source{f.start(sem), f.start(nil)} {
		if _, err := s.Advisories(ctx, []dep.Dep{goDep("example.com/m", "v1.0.0")}); err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Errorf("err = %v, want context canceled", err)
		}
	}
}

func TestNew(t *testing.T) {
	s, ok := New(http.DefaultClient, nil).(*Source)
	if !ok || s.baseURL != defaultBaseURL || s.batch != maxBatch {
		t.Errorf("New = %+v", s)
	}
}

func TestAdvisorySummaryTruncated(t *testing.T) {
	v := vuln{ID: "X/1", Details: strings.Repeat("a", 200)}
	a := v.advisory()
	if len(a.Summary) != 120 || !strings.HasSuffix(a.Summary, "...") || a.URL != "https://osv.dev/vulnerability/X%2F1" {
		t.Errorf("advisory = %+v", a)
	}
}

func TestAdvisoriesNpm(t *testing.T) {
	f := newFake(t)
	f.results["npm|lodash|4.17.20|"] = result{ids: []string{"GHSA-35jh-r3h4-6jhm", "CVE-ALIAS"}}
	f.vulns["GHSA-35jh-r3h4-6jhm"] = `{"id":"GHSA-35jh-r3h4-6jhm","summary":"Command injection","aliases":["CVE-ALIAS"],
		"affected":[{"package":{"name":"lodash","ecosystem":"npm"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"4.17.21"}]}]},
		            {"package":{"name":"lodash-es","ecosystem":"npm"},"ranges":[{"type":"SEMVER","events":[{"fixed":"9.9.9"}]}]}]}`
	f.vulns["CVE-ALIAS"] = `{"id":"CVE-ALIAS","affected":[{"package":{"name":"lodash","ecosystem":"npm"},"ranges":[{"type":"ECOSYSTEM","events":[{"fixed":"v4.17.21"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		{Manager: "npm", DepType: "dependencies", Name: "lodash", Version: "4.17.20"},
		{Manager: "npm", DepType: "dependencies", Name: "unversioned"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]datasource.Vuln{
		{{Advisory: dep.Advisory{ID: "GHSA-35jh-r3h4-6jhm", Summary: "Command injection", URL: "https://osv.dev/vulnerability/GHSA-35jh-r3h4-6jhm"}, Fixed: []string{"4.17.21"}}},
		nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 1 || f.batches[0][0].Package.Ecosystem != "npm" {
		t.Errorf("batches = %+v", f.batches)
	}
}
