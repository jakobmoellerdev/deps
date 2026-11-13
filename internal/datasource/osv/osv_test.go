package osv

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
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
		var req struct {
			Queries []query `json:"queries"`
		}
		if err := json.UnmarshalRead(r.Body, &req); err != nil {
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
		{Manager: "gomod", DepType: "go", Name: "go", Version: "1.26rc1"}, // OSV cannot match a prerelease
		goDep("golang.org/x/net", "v0.17.0"),
		{Manager: "docker", Name: "golang", Version: "1.22"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]datasource.Vuln{
		{
			{Advisory: dep.Advisory{ID: "GO-2024-2687", Summary: "summary of GO-2024-2687", URL: "https://example.com/GO-2024-2687", Aliases: []string{"CVE-2023-45288", "GHSA-4v7x-pqxf-cx7m"}}, Fixed: []string{"v0.23.0"}},
			{Advisory: dep.Advisory{ID: "GO-2024-3333", Summary: "summary of GO-2024-3333", URL: "https://example.com/GO-2024-3333", Aliases: []string{}}, Fixed: []string{"v0.33.0", "v1.2.0"}},
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

// GO-2024-2687 (stdlib and golang.org/x/net) and GO-2024-2825 (toolchain), trimmed; the fixed
// versions per release line as OSV lists them for the standard library and the go command.
func TestAdvisoriesGoToolchain(t *testing.T) {
	f := newFake(t)
	f.results["Go|stdlib|1.22.0|"] = result{ids: []string{"GO-2024-2687", "GO-2024-2598"}}
	f.results["Go|toolchain|1.22.0|"] = result{ids: []string{"GO-2024-2825", "GO-2026-6179"}}
	f.results["Go|stdlib|1.21.0|"] = result{ids: []string{"GO-2024-2598"}}
	f.vulns["GO-2024-2687"] = `{"id":"GO-2024-2687","summary":"HTTP/2 CONTINUATION flood in net/http","aliases":["CVE-2023-45288","GHSA-4v7x-pqxf-cx7m"],
		"affected":[{"package":{"name":"stdlib","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.21.9"},{"introduced":"1.22.0-0"},{"fixed":"1.22.2"}]}]},
		            {"package":{"name":"golang.org/x/net","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"0.23.0"}]}]}]}`
	f.vulns["GO-2024-2598"] = `{"id":"GO-2024-2598","summary":"Verify panics on certificates with an unknown public key algorithm in crypto/x509",
		"affected":[{"package":{"name":"stdlib","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.21.0-0"},{"fixed":"1.21.8"},{"introduced":"1.22.0-0"},{"fixed":"1.22.1"}]}]}]}`
	f.vulns["GO-2024-2825"] = `{"id":"GO-2024-2825","summary":"Arbitrary code execution during build on Darwin in cmd/go",
		"affected":[{"package":{"name":"toolchain","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.21.10"},{"introduced":"1.22.0-0"},{"fixed":"1.22.3"}]}]}]}`
	// A prerelease fix is not a version the directive can name.
	f.vulns["GO-2026-6179"] = `{"id":"GO-2026-6179","summary":"s",
		"affected":[{"package":{"name":"toolchain","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.25.13"},{"introduced":"1.27.0-rc.1"},{"fixed":"1.27.0-rc.3"}]}]},
		            {"package":{"name":"golang.org/x/mod","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"0.30.0"}]}]}]}`
	s := f.start(nil)

	deps := []dep.Dep{
		{Manager: "gomod", DepType: "toolchain", Name: "toolchain", Version: "1.22.0"},
		{Manager: "gomod", DepType: "go", Name: "go", Version: "1.22"}, // its line from 1.22.0: fixes cut to 1.22
		{Manager: "gomod", DepType: "go", Name: "go", Version: "1.21.0"},
		{Manager: "gomod", DepType: "go", Name: "go", Version: "v1.22.0"},                                 // not a directive value
		{Manager: "github-actions", DepType: "action", Name: "toolchain", Version: "1.22.0", Source: "x"}, // not gomod
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	adv := func(id, summary string, aliases ...string) dep.Advisory {
		return dep.Advisory{ID: id, Summary: summary, URL: "https://osv.dev/vulnerability/" + id, Aliases: aliases}
	}
	x509 := adv("GO-2024-2598", "Verify panics on certificates with an unknown public key algorithm in crypto/x509")
	want := [][]datasource.Vuln{
		{
			{Advisory: x509, Fixed: []string{"1.21.8", "1.22.1"}},
			{Advisory: adv("GO-2024-2687", "HTTP/2 CONTINUATION flood in net/http", "CVE-2023-45288", "GHSA-4v7x-pqxf-cx7m"), Fixed: []string{"1.21.9", "1.22.2"}},
			{Advisory: adv("GO-2024-2825", "Arbitrary code execution during build on Darwin in cmd/go"), Fixed: []string{"1.21.10", "1.22.3"}},
			{Advisory: adv("GO-2026-6179", "s"), Fixed: []string{"1.25.13"}},
		},
		{
			{Advisory: x509, Fixed: []string{"1.21", "1.22"}},
			{Advisory: adv("GO-2024-2687", "HTTP/2 CONTINUATION flood in net/http", "CVE-2023-45288", "GHSA-4v7x-pqxf-cx7m"), Fixed: []string{"1.21", "1.22"}},
			{Advisory: adv("GO-2024-2825", "Arbitrary code execution during build on Darwin in cmd/go"), Fixed: []string{"1.21", "1.22"}},
			{Advisory: adv("GO-2026-6179", "s"), Fixed: []string{"1.25"}},
		},
		{{Advisory: x509, Fixed: []string{"1.21.8", "1.22.1"}}},
		nil,
		nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	// The 2-part directive shares the queries of 1.22.0.
	if len(f.batches) != 1 || len(f.batches[0]) != 4 {
		t.Errorf("batches = %+v, want one batch with 4 distinct queries", f.batches)
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
	// An unparsable event is skipped: the rest of the range still decides (dep 4, 5 and 6 are
	// affected from "introduced: 0"), and no fix is rendered from it.
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
		{"GHSA-bad", "GHSA-la", "GHSA-mcph", "GHSA-mrrh"},
		{"GHSA-bad", "GHSA-mcph", "GHSA-mrrh"},
		{"GHSA-bad", "GHSA-mcph", "GHSA-mrrh", "GHSA-vers"},
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
		{4, "GHSA-bad", nil},
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
	// The chunks of a round run concurrently, so they reach the server in any order; rounds
	// (page tokens) run one after the other.
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
	slices.Sort(sizes[:3])
	if want := []int{1, 2, 2, 2, 1}; !reflect.DeepEqual(sizes, want) {
		t.Errorf("batch sizes = %v, want %v (first round sorted)", sizes, want)
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
		}, "/v1/vulns/GO-A: jsontext: unexpected EOF"},
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

// TestAdvisoriesPartialFetchFailure checks that one advisory that cannot be fetched fails the
// lookup: the deps it affects would otherwise be reported as clean although deps knows an
// advisory is missing.
func TestAdvisoriesPartialFetchFailure(t *testing.T) {
	f := newFake(t)
	f.results["Go|example.com/a|v1.0.0|"] = result{ids: []string{"GO-A"}}
	f.results["Go|example.com/b|v1.0.0|"] = result{ids: []string{"GO-B"}}
	f.results["Go|example.com/c|v1.0.0|"] = result{ids: []string{"GO-B", "GO-C"}}
	f.vulns["GO-A"] = goVuln("GO-A", nil, "example.com/a", "1.0.1")
	f.vulns["GO-B"] = goVuln("GO-B", nil, "example.com/b", "1.0.1")
	f.vulns["GO-C"] = goVuln("GO-C", nil, "example.com/c", "1.0.1")
	f.status["/v1/vulns/GO-B"] = http.StatusInternalServerError
	deps := []dep.Dep{goDep("example.com/a", "v1.0.0"), goDep("example.com/b", "v1.0.0"), goDep("example.com/c", "v1.0.0")}
	got, err := f.start(nil).Advisories(t.Context(), deps)
	if err == nil || !strings.Contains(err.Error(), "advisories not fetched (GO-B)") || !strings.Contains(err.Error(), "/v1/vulns/GO-B: HTTP 500") {
		t.Fatalf("err = %v, want the unfetched advisory named", err)
	}
	if got != nil {
		t.Errorf("got %+v, want no results", got)
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

// TestQueryBatchRetried checks the querybatch POST is marked idempotent, so the shared client
// retries it and re-sends the body.
func TestQueryBatchRetried(t *testing.T) {
	f := newFake(t)
	f.results["Go|example.com/m|v1.0.0|"] = result{ids: []string{"GO-A"}}
	f.vulns["GO-A"] = `{"id":"GO-A","affected":[{"package":{"ecosystem":"Go","name":"example.com/m"},"versions":["v1.0.0"]}]}`
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && posts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	got, err := newSource(httpx.NewClient(), nil, srv.URL).Advisories(t.Context(), []dep.Dep{goDep("example.com/m", "v1.0.0")})
	if err != nil || len(got) != 1 {
		t.Fatalf("Advisories = %+v, %v", got, err)
	}
	if posts.Load() != 2 || len(f.batches) != 1 || len(f.batches[0]) != 1 {
		t.Errorf("posts = %d, decoded batches = %+v; want 2 posts, 1 full batch", posts.Load(), f.batches)
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
	if !ok || s.baseURL != defaultBaseURL || s.batch != batchSize {
		t.Errorf("New = %+v", s)
	}
}

// TestAdvisoriesFetchesDuringBatches checks that the advisories of a returned batch are fetched
// while another batch is still running, instead of after the last one.
func TestAdvisoriesFetchesDuringBatches(t *testing.T) {
	f := newFake(t)
	for _, name := range []string{"example.com/fast", "example.com/slow"} {
		id := "GO-" + strings.TrimPrefix(name, "example.com/")
		f.results["Go|"+name+"|v1.0.0|"] = result{ids: []string{id}}
		f.vulns[id] = goVuln(id, nil, name, "1.0.1")
	}
	fetched := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/vulns/GO-fast" {
			defer close(fetched)
		}
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(b))
			if bytes.Contains(b, []byte("example.com/slow")) {
				select {
				case <-fetched:
				case <-time.After(5 * time.Second):
					t.Error("GO-fast was not fetched while the slow batch was running")
				}
			}
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	s := newSource(srv.Client(), make(chan struct{}, 4), srv.URL)
	s.batch = 1
	got, err := s.Advisories(t.Context(), []dep.Dep{goDep("example.com/fast", "v1.0.0"), goDep("example.com/slow", "v1.0.0")})
	if err != nil || len(got[0]) != 1 || len(got[1]) != 1 {
		t.Fatalf("Advisories = %+v, %v", got, err)
	}
}

// TestAdvisoriesFetchersFillSem checks that advisory fetches use every slot of the semaphore
// (OSV's own pool), not a fixed number of fetchers.
func TestAdvisoriesFetchersFillSem(t *testing.T) {
	const n = maxFetchers + 4
	f := newFake(t)
	var ids []string
	for i := range n {
		id := fmt.Sprintf("GO-%02d", i)
		ids = append(ids, id)
		f.vulns[id] = goVuln(id, nil, "example.com/m", "1.0.1")
	}
	f.results["Go|example.com/m|v1.0.0|"] = result{ids: ids}
	var inflight atomic.Int32
	all := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if inflight.Add(1) == n {
				close(all)
			}
			select {
			case <-all:
			case <-time.After(5 * time.Second):
				t.Errorf("%d of %d advisory fetches in flight, want all", inflight.Load(), n)
			}
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	got, err := newSource(srv.Client(), make(chan struct{}, n), srv.URL).Advisories(t.Context(), []dep.Dep{goDep("example.com/m", "v1.0.0")})
	if err != nil || len(got[0]) != n {
		t.Fatalf("Advisories = %d vulns, %v", len(got[0]), err)
	}
}

func TestAdvisorySummaryTruncated(t *testing.T) {
	v := vuln{ID: "X/1", Details: strings.Repeat("a", 200)}
	a := v.advisory()
	if len(a.Summary) != 120 || !strings.HasSuffix(a.Summary, "...") || a.URL != "https://osv.dev/vulnerability/X%2F1" {
		t.Errorf("advisory = %+v", a)
	}
	// A summary cut mid-rune would reach PR bodies and `deps plan` as invalid UTF-8: the
	// 117th byte of this one is inside the "ü" that starts at byte 116.
	v = vuln{ID: "X", Details: strings.Repeat("a", 116) + "ü" + strings.Repeat("b", 100)}
	a = v.advisory()
	if !utf8.ValidString(a.Summary) {
		t.Errorf("Summary = %q, not valid UTF-8", a.Summary)
	}
	if want := strings.Repeat("a", 116) + "..."; a.Summary != want {
		t.Errorf("Summary = %q, want %q", a.Summary, want)
	}
}

func TestAdvisoriesNpm(t *testing.T) {
	f := newFake(t)
	f.results["npm|lodash|4.17.20|"] = result{ids: []string{"GHSA-35jh-r3h4-6jhm", "CVE-ALIAS"}}
	f.vulns["GHSA-35jh-r3h4-6jhm"] = `{"id":"GHSA-35jh-r3h4-6jhm","summary":"Command injection","aliases":["CVE-ALIAS"],
		"affected":[{"package":{"name":"lodash","ecosystem":"npm"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"4.17.21"}]}]},
		            {"package":{"name":"lodash-es","ecosystem":"npm"},"ranges":[{"type":"SEMVER","events":[{"fixed":"9.9.9"}]}]}]}`
	f.vulns["CVE-ALIAS"] = `{"id":"CVE-ALIAS","affected":[{"package":{"name":"lodash","ecosystem":"npm"},"ranges":[{"type":"ECOSYSTEM","events":[{"fixed":"v4.17.21"}]}]}]}`
	// An advisory of the npm package named node must not land on the engines.node pin.
	f.results["npm|node|20.11.0|"] = result{ids: []string{"GHSA-node-pkg"}}
	f.vulns["GHSA-node-pkg"] = `{"id":"GHSA-node-pkg","summary":"npm package node",
		"affected":[{"package":{"name":"node","ecosystem":"npm"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"99.0.0"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		{Manager: "npm", Datasource: "npm", DepType: "dependencies", Name: "lodash", Version: "4.17.20"},
		{Manager: "npm", Datasource: "npm", DepType: "dependencies", Name: "unversioned"},
		// The locked version is queried, not the range floor: the same query as above.
		{Manager: "npm", Datasource: "npm", DepType: "dependencies", Name: "lodash", Version: "4.0.0", Locked: "4.17.20"},
		// The node engine is not the npm package named node, and git dependencies are not npm packages.
		{Manager: "npm", Datasource: "node-version", DepType: "engines", Name: "node", Version: "20.11.0"},
		{Manager: "npm", Datasource: "git-tags", DepType: "dependencies", Name: "lodash", Version: "4.17.20"},
		// A deno npm: specifier is an npm package; jsr: ones have no OSV ecosystem.
		{Manager: "deno", Datasource: "npm", DepType: "imports", Name: "lodash", Version: "4.0.0", Locked: "4.17.20"},
		{Manager: "deno", Datasource: "jsr", DepType: "imports", Name: "@std/path", Version: "1.0.0"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	lodash := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-35jh-r3h4-6jhm", Summary: "Command injection", URL: "https://osv.dev/vulnerability/GHSA-35jh-r3h4-6jhm", Aliases: []string{"CVE-ALIAS"}}, Fixed: []string{"4.17.21"}}}
	want := [][]datasource.Vuln{lodash, nil, lodash, nil, nil, lodash, nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 1 || f.batches[0][0].Package.Ecosystem != "npm" {
		t.Errorf("batches = %+v", f.batches)
	}
}

// A fixed version another range of the same advisory still covers is no fix for the dep.
func TestFixedOutsideEveryRange(t *testing.T) {
	f := newFake(t)
	npmVuln := func(id, name string, ranges ...string) string {
		return fmt.Sprintf(`{"id":%q,"summary":"s","affected":[{"package":{"name":%q,"ecosystem":"npm"},"ranges":[%s]}]}`,
			id, name, strings.Join(ranges, ","))
	}
	sem := func(events ...string) string {
		return `{"type":"SEMVER","events":[` + strings.Join(events, ",") + `]}`
	}
	// Overlapping ranges: 1.5.0 fixes the first one but is inside the second, 2.0.0 is out of both.
	f.results["npm|overlap|1.4.5|"] = result{ids: []string{"GHSA-overlap"}}
	f.vulns["GHSA-overlap"] = npmVuln("GHSA-overlap", "overlap",
		sem(`{"introduced":"0"}`, `{"fixed":"1.5.0"}`),
		sem(`{"introduced":"1.4.0"}`, `{"fixed":"2.0.0"}`))
	// The second range ends with last_affected, so the advisory names no fix at all.
	f.results["npm|tail|1.4.5|"] = result{ids: []string{"GHSA-tail"}}
	f.vulns["GHSA-tail"] = npmVuln("GHSA-tail", "tail",
		sem(`{"introduced":"0"}`, `{"fixed":"1.5.0"}`),
		sem(`{"introduced":"1.5.0"}`, `{"last_affected":"1.9.0"}`))
	// Disjoint ranges keep every fix: neither is inside the other.
	f.results["npm|lines|1.4.5|"] = result{ids: []string{"GHSA-lines"}}
	f.vulns["GHSA-lines"] = npmVuln("GHSA-lines", "lines",
		sem(`{"introduced":"1.0.0"}`, `{"fixed":"1.5.0"}`),
		sem(`{"introduced":"2.0.0"}`, `{"fixed":"2.1.0"}`))
	// Hex orders its versions with the "hex" scheme, which has no version "0": the
	// "introduced: 0" limit is below every version all the same.
	f.results["Hex|overlap|1.4.5|"] = result{ids: []string{"HEX-overlap"}}
	f.vulns["HEX-overlap"] = `{"id":"HEX-overlap","summary":"s","affected":[{"package":{"name":"overlap","ecosystem":"Hex"},"ranges":[
		{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"1.5.0"}]},
		{"type":"ECOSYSTEM","events":[{"introduced":"1.4.0"},{"fixed":"2.0.0"}]}]}]}`
	s := f.start(nil)

	deps := []dep.Dep{
		{Manager: "npm", Datasource: "npm", DepType: "dependencies", Name: "overlap", Version: "1.4.5"},
		{Manager: "npm", Datasource: "npm", DepType: "dependencies", Name: "tail", Version: "1.4.5"},
		{Manager: "npm", Datasource: "npm", DepType: "dependencies", Name: "lines", Version: "1.4.5"},
		{Manager: "mix", Datasource: "hex", DepType: "deps", Name: "overlap", Version: "1.4.5"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"2.0.0"}, nil, {"1.5.0", "2.1.0"}, {"2.0.0"}}
	for i, w := range want {
		if len(got[i]) != 1 {
			t.Fatalf("dep %d: got %+v, want one advisory", i, got[i])
		}
		if !reflect.DeepEqual(got[i][0].Fixed, w) {
			t.Errorf("dep %d %s: Fixed = %q, want %q", i, got[i][0].Advisory.ID, got[i][0].Fixed, w)
		}
	}
}

// RUSTSEC-2020-0071 (time, GHSA-wcg3-cvx6-7396), trimmed to its crates.io range.
func TestAdvisoriesCrates(t *testing.T) {
	f := newFake(t)
	f.results["crates.io|time|0.1.40|"] = result{ids: []string{"GHSA-wcg3-cvx6-7396", "RUSTSEC-2020-0071"}}
	f.results["crates.io|time|0.1.0|"] = result{ids: []string{"RUSTSEC-2020-0071"}}
	f.results["crates.io|time|0.1.43|"] = result{ids: []string{"RUSTSEC-2020-0071"}}
	f.vulns["RUSTSEC-2020-0071"] = `{"id":"RUSTSEC-2020-0071","summary":"Potential segfault in the time crate","aliases":["GHSA-wcg3-cvx6-7396"],
		"affected":[{"package":{"name":"time","ecosystem":"crates.io"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0.0.0-0"},{"fixed":"0.2.23"}]}]}]}`
	f.vulns["GHSA-wcg3-cvx6-7396"] = `{"id":"GHSA-wcg3-cvx6-7396","aliases":["RUSTSEC-2020-0071"],
		"affected":[{"package":{"name":"time","ecosystem":"crates.io"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0.2.23"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		{Manager: "cargo", Datasource: "crate", DepType: "dependencies", Name: "time", Version: "0.1.40"},
		{Manager: "cargo", Datasource: "crate", DepType: "dev-dependencies", Name: "time", Version: "0.1"},
		{Manager: "cargo", Datasource: "git-tags", DepType: "dependencies", Name: "time", Version: "v0.1.0"},
		{Manager: "cargo", Datasource: "crate", DepType: "dependencies", Name: "time", Version: "0.1", Locked: "0.1.43"},  // Cargo.lock's version
		{Manager: "cargo", Datasource: "crate", DepType: "dependencies", Name: "time", Version: "0.1.40", Source: "corp"}, // not crates.io
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	adv := dep.Advisory{ID: "RUSTSEC-2020-0071", Summary: "Potential segfault in the time crate", URL: "https://osv.dev/vulnerability/RUSTSEC-2020-0071", Aliases: []string{"GHSA-wcg3-cvx6-7396"}}
	want := [][]datasource.Vuln{{{Advisory: adv, Fixed: []string{"0.2.23"}}}, {{Advisory: adv, Fixed: []string{"0.2"}}}, nil, {{Advisory: adv, Fixed: []string{"0.2.23"}}}, nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 3 || f.batches[0][1].Version != "0.1.0" || f.batches[0][2].Version != "0.1.43" {
		t.Errorf("batches = %+v", f.batches)
	}
}

func TestAdvisoriesPyPI(t *testing.T) {
	f := newFake(t)
	f.results["PyPI|django|4.2.0|"] = result{ids: []string{"GHSA-qrw5-5h28-6cmg", "PYSEC-2023-100"}}
	f.vulns["GHSA-qrw5-5h28-6cmg"] = `{"id":"GHSA-qrw5-5h28-6cmg","summary":"Denial of service","aliases":["PYSEC-2023-100"],
		"affected":[{"package":{"name":"Django","ecosystem":"PyPI"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"4.2"},{"fixed":"4.2.2"}]}]},
		            {"package":{"name":"django-other","ecosystem":"PyPI"},"ranges":[{"type":"ECOSYSTEM","events":[{"fixed":"9.9.9"}]}]}]}`
	f.vulns["PYSEC-2023-100"] = `{"id":"PYSEC-2023-100","affected":[{"package":{"name":"django","ecosystem":"PyPI"},"ranges":[{"type":"ECOSYSTEM","events":[{"fixed":"4.2.2"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		{Manager: "pip_requirements", Datasource: "pypi", Name: "Django", Version: "4.2.0"},
		{Manager: "poetry", Datasource: "pypi", Name: "unversioned"},
		// The locked version is queried, not the range's anchor: the same query as above.
		{Manager: "pep621", Datasource: "pypi", Name: "django", Version: "4.1", Range: ">=4.1,<5", Locked: "4.2.0"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	django := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-qrw5-5h28-6cmg", Summary: "Denial of service", URL: "https://osv.dev/vulnerability/GHSA-qrw5-5h28-6cmg", Aliases: []string{"PYSEC-2023-100"}}, Fixed: []string{"4.2.2"}}}
	want := [][]datasource.Vuln{django, nil, django}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 1 || f.batches[0][0].Package != (pkg{Name: "django", Ecosystem: "PyPI"}) {
		t.Errorf("batches = %+v", f.batches)
	}
}

func TestAdvisoriesMaven(t *testing.T) {
	f := newFake(t)
	const name = "com.fasterxml.jackson.core:jackson-databind"
	f.results["Maven|"+name+"|2.9.10|"] = result{ids: []string{"GHSA-57j2-w4cx-62h2"}}
	f.vulns["GHSA-57j2-w4cx-62h2"] = `{"id":"GHSA-57j2-w4cx-62h2","summary":"Deeply nested json in jackson-databind",
		"affected":[{"package":{"name":"` + name + `","ecosystem":"Maven"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"2.12.7.1"}]}]},
		            {"package":{"name":"` + name + `","ecosystem":"Maven"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"2.13.0"},{"fixed":"2.13.4.1"}]}]}]}`
	// A Gradle plugin id is queried as its marker artifact; GHSA-cv78-v957-jx34 as OSV serves
	// it (affected entries, versions lists cut; fetched 2026-11-06) names the implementation
	// artifact and the marker.
	const marker = "com.gradle.plugin-publish:com.gradle.plugin-publish.gradle.plugin"
	f.results["Maven|"+marker+"|0.10.1|"] = result{ids: []string{"GHSA-cv78-v957-jx34"}}
	f.vulns["GHSA-cv78-v957-jx34"] = `{"id":"GHSA-cv78-v957-jx34","summary":"Exposure of Sensitive Information in Gradle publish plugin",
		"affected":[{"package":{"name":"com.gradle.publish:plugin-publish-plugin","ecosystem":"Maven","purl":"pkg:maven/com.gradle.publish/plugin-publish-plugin"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0.11.0"}]}],"versions":["0.10.0","0.10.1"]},
		            {"package":{"name":"` + marker + `","ecosystem":"Maven","purl":"pkg:maven/com.gradle.plugin-publish/com.gradle.plugin-publish.gradle.plugin"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0.11.0"}]}],"versions":["0.10.0","0.10.1"]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		{Manager: "maven", Datasource: "maven", DepType: "compile", Name: name, Version: "2.9.10"},
		{Manager: "gradle", Datasource: "maven", DepType: "plugin", Name: "com.gradle.plugin-publish", Version: "0.10.1"},
		{Manager: "maven", Datasource: "maven", Name: "g:unversioned"},
		{Manager: "maven", Datasource: "maven", Version: "1.0"},
		{Manager: "sbt", Datasource: "sbt-package", DepType: "compile", Name: name, Version: "2.9.10"},
		{Manager: "sbt", Datasource: "sbt-plugin", DepType: "plugin", Name: "com.github.sbt:sbt-git", Version: "2.0.0"},
		{Manager: "leiningen", Datasource: "clojure", DepType: "dependencies", Name: name, Version: "2.9.10"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	jackson := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-57j2-w4cx-62h2", Summary: "Deeply nested json in jackson-databind", URL: "https://osv.dev/vulnerability/GHSA-57j2-w4cx-62h2"}, Fixed: []string{"2.12.7.1", "2.13.4.1"}}}
	publish := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-cv78-v957-jx34", Summary: "Exposure of Sensitive Information in Gradle publish plugin", URL: "https://osv.dev/vulnerability/GHSA-cv78-v957-jx34"}, Fixed: []string{"0.11.0"}}}
	want := [][]datasource.Vuln{jackson, publish, nil, nil, jackson, nil, jackson}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 2 || f.batches[0][1].Package != (pkg{Name: marker, Ecosystem: "Maven"}) {
		t.Errorf("batches = %+v", f.batches)
	}
}

func TestAdvisoriesRubyGems(t *testing.T) {
	f := newFake(t)
	f.results["RubyGems|rack|2.2.8|"] = result{ids: []string{"GHSA-22f2-v57c-j9cx", "CVE-2024-26146"}}
	f.vulns["GHSA-22f2-v57c-j9cx"] = `{"id":"GHSA-22f2-v57c-j9cx","summary":"Rack has possible DoS Vulnerability with Range Header","aliases":["CVE-2024-26146"],
		"affected":[{"package":{"name":"rack","ecosystem":"RubyGems"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"2.0.9.4"}]}]},
		            {"package":{"name":"rack","ecosystem":"RubyGems"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"2.2.0"},{"fixed":"2.2.8.1"}]}]},
		            {"package":{"name":"Rack","ecosystem":"RubyGems"},"ranges":[{"type":"ECOSYSTEM","events":[{"fixed":"9.9.9"}]}]}]}`
	f.vulns["CVE-2024-26146"] = `{"id":"CVE-2024-26146","aliases":["GHSA-22f2-v57c-j9cx"],"affected":[{"package":{"name":"rack","ecosystem":"RubyGems"},"ranges":[{"type":"ECOSYSTEM","events":[{"fixed":"2.2.8.1"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		// The locked version is queried, not the requirement's floor.
		{Manager: "bundler", Datasource: "rubygems", DepType: "dependencies", Name: "rack", Version: "2.2", Range: "~> 2.2", Locked: "2.2.8"},
		{Manager: "bundler", Datasource: "rubygems", DepType: "runtime", Name: "rack", Version: "2.2.8", Range: ">= 2.2.8"},
		{Manager: "bundler", Datasource: "rubygems", Name: "unversioned", Range: ">= 0"},
		{Manager: "bundler", Datasource: "ruby-version", DepType: "ruby", Name: "ruby", Version: "3.3.0"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	rack := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-22f2-v57c-j9cx", Summary: "Rack has possible DoS Vulnerability with Range Header", URL: "https://osv.dev/vulnerability/GHSA-22f2-v57c-j9cx", Aliases: []string{"CVE-2024-26146"}}, Fixed: []string{"2.0.9.4", "2.2.8.1"}}}
	if want := [][]datasource.Vuln{rack, rack, nil, nil}; !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 1 || f.batches[0][0].Package != (pkg{Name: "rack", Ecosystem: "RubyGems"}) {
		t.Errorf("batches = %+v", f.batches)
	}
}

func TestAdvisoriesPackagist(t *testing.T) {
	f := newFake(t)
	f.results["Packagist|guzzlehttp/guzzle|7.4.4|"] = result{ids: []string{"GHSA-25mq-v84q-4j7r"}}
	// GHSA-25mq-v84q-4j7r as OSV serves it (affected entries; fetched 2026-11-05), plus an entry
	// of another package.
	f.vulns["GHSA-25mq-v84q-4j7r"] = `{"id":"GHSA-25mq-v84q-4j7r","summary":"CURLOPT_HTTPAUTH option not cleared on change of origin","aliases":["CVE-2022-31090"],
		"affected":[{"package":{"name":"guzzlehttp/guzzle","ecosystem":"Packagist","purl":"pkg:composer/guzzlehttp/guzzle"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"6.5.8"}]}]},
		            {"package":{"name":"GuzzleHttp/Guzzle","ecosystem":"Packagist"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"7.0.0"},{"fixed":"7.4.5"}]}]},
		            {"package":{"name":"guzzlehttp/psr7","ecosystem":"Packagist"},"ranges":[{"type":"ECOSYSTEM","events":[{"fixed":"9.9.9"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		// The locked version is queried without its "v", the name lower-cased.
		{Manager: "composer", Datasource: "packagist", DepType: "require", Name: "GuzzleHttp/Guzzle", Version: "7.4", Range: "^7.4", Locked: "v7.4.4"},
		{Manager: "composer", Datasource: "packagist", DepType: "require", Name: "guzzlehttp/guzzle", Version: "7.4.4", Range: "7.4.4"},
		{Manager: "composer", Datasource: "packagist", Name: "unversioned", Range: "*"},
		{Manager: "composer", Datasource: "git-tags", Name: "acme/fork", Version: "7.4.4", Source: "https://github.com/acme/fork"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	guzzle := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-25mq-v84q-4j7r", Summary: "CURLOPT_HTTPAUTH option not cleared on change of origin", URL: "https://osv.dev/vulnerability/GHSA-25mq-v84q-4j7r", Aliases: []string{"CVE-2022-31090"}}, Fixed: []string{"6.5.8", "7.4.5"}}}
	if want := [][]datasource.Vuln{guzzle, guzzle, nil, nil}; !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 1 || f.batches[0][0].Package != (pkg{Name: "guzzlehttp/guzzle", Ecosystem: "Packagist"}) {
		t.Errorf("batches = %+v", f.batches)
	}
}

// GHSA-4rgh-jx4f-qfcq as api.osv.dev serves it (2026-11-06).
func TestAdvisoriesPub(t *testing.T) {
	f := newFake(t)
	f.results["Pub|http|0.12.2|"] = result{ids: []string{"GHSA-4rgh-jx4f-qfcq"}}
	f.vulns["GHSA-4rgh-jx4f-qfcq"] = `{"id":"GHSA-4rgh-jx4f-qfcq","summary":"http before 0.13.3 vulnerable to header injection","aliases":["CVE-2020-35669"],
		"affected":[{"package":{"name":"http","ecosystem":"Pub"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0.13.3"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		{Manager: "pub", Datasource: "dart", DepType: "dependencies", Name: "http", Version: "0.12.0", Range: "^0.12.0", Locked: "0.12.2"},
		{Manager: "pub", Datasource: "dart", DepType: "dependencies", Name: "http", Version: "0.12.2", Range: "0.12.2"},
		{Manager: "pub", Datasource: "dart", Name: "unlocked", Range: "<2.0.0"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	vuln := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-4rgh-jx4f-qfcq", Summary: "http before 0.13.3 vulnerable to header injection", URL: "https://osv.dev/vulnerability/GHSA-4rgh-jx4f-qfcq", Aliases: []string{"CVE-2020-35669"}}, Fixed: []string{"0.13.3"}}}
	if want := [][]datasource.Vuln{vuln, vuln, nil}; !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 1 || f.batches[0][0].Package != (pkg{Name: "http", Ecosystem: "Pub"}) {
		t.Errorf("batches = %+v", f.batches)
	}
}

// GHSA-5crp-9r3c-p9vr as api.osv.dev serves it (2026-11-05), affected list cut to the package;
// OSV matches NuGet ids case-insensitively, so a lower-case query returns it too.
func TestAdvisoriesNuGet(t *testing.T) {
	f := newFake(t)
	f.results["NuGet|newtonsoft.json|12.0.3|"] = result{ids: []string{"GHSA-5crp-9r3c-p9vr"}}
	f.vulns["GHSA-5crp-9r3c-p9vr"] = `{"id":"GHSA-5crp-9r3c-p9vr","summary":"Improper Handling of Exceptional Conditions in Newtonsoft.Json","aliases":["CVE-2024-21907"],
		"affected":[{"package":{"name":"Newtonsoft.Json","ecosystem":"NuGet","purl":"pkg:nuget/Newtonsoft.Json"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"13.0.1"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		{Manager: "nuget", Datasource: "nuget", DepType: "nuget", Name: "newtonsoft.json", Version: "12.0.3"},
		{Manager: "nuget", Datasource: "nuget", DepType: "nuget", Name: "Floating", Version: "1", Range: "1.*"},
		{Manager: "nuget", Datasource: "dotnet-version", DepType: "dotnet-sdk", Name: "dotnet-sdk", Version: "8.0.100"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	nj := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-5crp-9r3c-p9vr", Summary: "Improper Handling of Exceptional Conditions in Newtonsoft.Json", URL: "https://osv.dev/vulnerability/GHSA-5crp-9r3c-p9vr", Aliases: []string{"CVE-2024-21907"}}, Fixed: []string{"13.0.1"}}}
	if want := [][]datasource.Vuln{nj, nil, nil}; !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 1 || f.batches[0][0].Package != (pkg{Name: "newtonsoft.json", Ecosystem: "NuGet"}) {
		t.Errorf("batches = %+v", f.batches)
	}
}

// GHSA-2q6v-32mr-8p8x as api.osv.dev serves it (2026-11-06), affected entries only.
func TestAdvisoriesHex(t *testing.T) {
	f := newFake(t)
	f.results["Hex|plug|1.3.0|"] = result{ids: []string{"GHSA-2q6v-32mr-8p8x"}}
	f.vulns["GHSA-2q6v-32mr-8p8x"] = `{"id":"GHSA-2q6v-32mr-8p8x","summary":"Null Byte Injection in Plug.Static","aliases":["CVE-2017-1000052"],
		"affected":[{"package":{"name":"plug","ecosystem":"Hex"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.0.4"}]}]},
		            {"package":{"name":"plug","ecosystem":"Hex"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.1.0"},{"fixed":"1.1.7"}]}]},
		            {"package":{"name":"plug","ecosystem":"Hex"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.2.0"},{"fixed":"1.2.3"}]}]},
		            {"package":{"name":"plug","ecosystem":"Hex"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.3.0"},{"fixed":"1.3.2"}]}]}]}`
	s := f.start(nil)
	deps := []dep.Dep{
		// The locked version is queried, else the requirement's anchor.
		{Manager: "mix", Datasource: "hex", DepType: "prod", Name: "plug", Version: "1.0.0", Range: "~> 1.0", Locked: "1.3.0"},
		{Manager: "gleam", Datasource: "hex", DepType: "dependencies", Name: "plug", Version: "1.3.0", Range: "== 1.3.0"},
		{Manager: "mix", Datasource: "hex", DepType: "prod", Name: "secret:acme", Version: "1.3.0", Range: "== 1.3.0"},
		{Manager: "mix", Datasource: "git-tags", Name: "plug", Version: "v1.3.0", Source: "https://github.com/elixir-plug/plug"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	plug := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-2q6v-32mr-8p8x", Summary: "Null Byte Injection in Plug.Static", URL: "https://osv.dev/vulnerability/GHSA-2q6v-32mr-8p8x", Aliases: []string{"CVE-2017-1000052"}}, Fixed: []string{"1.0.4", "1.1.7", "1.2.3", "1.3.2"}}}
	if want := [][]datasource.Vuln{plug, plug, nil, nil}; !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 1 || f.batches[0][0].Package != (pkg{Name: "plug", Ecosystem: "Hex"}) {
		t.Errorf("batches = %+v", f.batches)
	}
}

func TestAdvisoriesTerraform(t *testing.T) {
	f := newFake(t)
	f.results["Go|github.com/hashicorp/terraform|1.5.0|"] = result{ids: []string{"GHSA-h626-pv66-hhm7", "GO-2023-2055"}}
	f.results["Go|github.com/hashicorp/terraform-provider-aws|1.13.0|"] = result{ids: []string{"GHSA-r48h-jr2j-9g78"}}
	// Advisory documents as OSV serves them (2026-11-06), trimmed to the fields deps reads.
	f.vulns["GHSA-h626-pv66-hhm7"] = `{"id":"GHSA-h626-pv66-hhm7","summary":"Terraform allows arbitrary file write during the ` + "`init`" + ` operation","aliases":["CVE-2023-4782","GO-2023-2055"],
		"affected":[{"package":{"name":"github.com/hashicorp/terraform","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.0.8"},{"fixed":"1.5.7"}]}]}]}`
	f.vulns["GO-2023-2055"] = `{"id":"GO-2023-2055","summary":"Terraform allows arbitrary file write during the ` + "`init`" + ` operation in github.com/hashicorp/terraform","aliases":["CVE-2023-4782","GHSA-h626-pv66-hhm7"],
		"affected":[{"package":{"name":"github.com/hashicorp/terraform","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"1.0.8"},{"fixed":"1.5.7"}]}]}]}`
	f.vulns["GHSA-r48h-jr2j-9g78"] = `{"id":"GHSA-r48h-jr2j-9g78","summary":" HashiCorp Terraform Amazon Web Services (AWS) uses an insecure PRNG ","aliases":["CVE-2018-9057"],
		"affected":[{"package":{"name":"github.com/hashicorp/terraform-provider-aws","ecosystem":"Go"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.14.0"}]}]}]}`
	s := f.start(nil)
	const tr = "https://registry.terraform.io"
	deps := []dep.Dep{
		// The locked version of a provider is queried, else an exact constraint's.
		{Manager: "terraform", Datasource: "terraform-provider", Name: "hashicorp/aws", Source: tr, Version: "1.0", Range: "~> 1.0", Locked: "1.13.0"},
		{Manager: "terraform", Datasource: "terraform-provider", Name: "Hashicorp/AWS", Source: "https://registry.opentofu.org", Version: "1.13.0", Range: "= 1.13.0"},
		{Manager: "terraform", Datasource: "terraform-provider", Name: "hashicorp/aws", Source: tr, Version: "1.0", Range: "~> 1.0"},
		{Manager: "terraform", Datasource: "terraform-provider", Name: "acme/aws", Source: "https://app.terraform.io", Version: "1.13.0", Range: "1.13.0"},
		{Manager: "terraform-version", Datasource: "github-releases", Name: "hashicorp/terraform", Version: "1.5.0"},
		{Manager: "terraform", Datasource: "github-releases", Name: "hashicorp/terraform", Version: "v1.5.0", Range: "v1.5.0"},
		{Manager: "terraform", Datasource: "github-releases", Name: "hashicorp/terraform", Version: "1.5", Range: ">= 1.5"},
		{Manager: "terraform-version", Datasource: "github-releases", Name: "hashicorp/terraform", Version: "1.5"},
		{Manager: "terraform", Datasource: "github-releases", Name: "hashicorp/terraform", Version: "1.5.0", Range: "~> 1.5 x"},
	}
	got, err := s.Advisories(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	aws := []datasource.Vuln{{Advisory: dep.Advisory{ID: "GHSA-r48h-jr2j-9g78", Summary: " HashiCorp Terraform Amazon Web Services (AWS) uses an insecure PRNG ", URL: "https://osv.dev/vulnerability/GHSA-r48h-jr2j-9g78", Aliases: []string{"CVE-2018-9057"}}, Fixed: []string{"1.14.0"}}}
	tfAdv := dep.Advisory{ID: "GO-2023-2055", Summary: "Terraform allows arbitrary file write during the `init` operation in github.com/hashicorp/terraform", URL: "https://osv.dev/vulnerability/GO-2023-2055", Aliases: []string{"CVE-2023-4782", "GHSA-h626-pv66-hhm7"}}
	want := [][]datasource.Vuln{aws, aws, nil, nil,
		{{Advisory: tfAdv, Fixed: []string{"1.5.7"}}},
		{{Advisory: tfAdv, Fixed: []string{"v1.5.7"}}},
		nil, nil, nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if len(f.batches) != 1 || len(f.batches[0]) != 2 {
		t.Errorf("batches = %+v", f.batches)
	}
}
