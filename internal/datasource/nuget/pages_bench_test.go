package nuget

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// pagedFeed serves a feed whose registration index of "big.lib" has pages that are not
// inlined, as nuget.org serves every package with more than 128 versions (Serilog has 602 in
// ten pages). Each page holds the versions <major>.<n>.0. reqs counts requests, bytes the
// bodies written.
func pagedFeed(tb testing.TB, majors, perPage int) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	tb.Helper()
	var reqs, bytes atomic.Int64
	var srv *httptest.Server
	page := func(major int) string {
		var b strings.Builder
		b.WriteString(`{"items":[`)
		for n := range perPage {
			if n > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"catalogEntry":{"version":"%d.%d.0","listed":true,"published":"2024-01-0%dT00:00:00Z","projectUrl":"https://github.com/example/big.lib"}}`, major, n, major%9+1)
		}
		b.WriteString(`]}`)
		return b.String()
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		write := func(s string) {
			bytes.Add(int64(len(s)))
			_, _ = w.Write([]byte(s))
		}
		switch {
		case r.URL.Path == "/v3/index.json":
			write(fmt.Sprintf(`{"version":"3.0.0","resources":[{"@id":"%s/reg/","@type":"RegistrationsBaseUrl/3.6.0"},{"@id":"%s/flat/","@type":"PackageBaseAddress/3.0.0"}]}`, srv.URL, srv.URL))
		case r.URL.Path == "/reg/big.lib/index.json":
			var b strings.Builder
			b.WriteString(`{"items":[`)
			for m := range majors {
				if m > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"@id":"%s/reg/big.lib/page/%d.json","lower":"%d.0.0","upper":"%d.%d.0","count":%d}`, srv.URL, m, m, m, perPage-1, perPage)
			}
			b.WriteString(`]}`)
			write(b.String())
		case strings.HasPrefix(r.URL.Path, "/reg/big.lib/page/"):
			m := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/reg/big.lib/page/"), ".json")
			n := 0
			fmt.Sscanf(m, "%d", &n)
			write(page(n))
		default:
			http.NotFound(w, r)
		}
	}))
	tb.Cleanup(srv.Close)
	return srv, &reqs, &bytes
}

func pagedSource(srv *httptest.Server) *Source {
	return New(srv.Client(), nil, func(k string) string {
		if k == "DEPS_NUGET_URL" {
			return srv.URL + "/v3/index.json"
		}
		return ""
	})
}

// BenchmarkPagedRegistration reports the requests and response bytes one listing of a package
// with ten registration pages costs for a dep that is on 7.0.0.
func BenchmarkPagedRegistration(b *testing.B) {
	srv, reqs, bytes := pagedFeed(b, 10, 64)
	d := dep.Dep{Name: "Big.Lib", Version: "7.0.0"}
	ctx := context.Background()
	n := 0
	reqs.Store(0)
	bytes.Store(0)
	for b.Loop() {
		s := pagedSource(srv)
		s.Plan([]dep.Dep{d})
		if _, err := s.Releases(ctx, d, ""); err != nil {
			b.Fatal(err)
		}
		n++
	}
	b.ReportMetric(float64(reqs.Load())/float64(n), "reqs/op")
	b.ReportMetric(float64(bytes.Load())/float64(n), "respbytes/op")
}

// TestPagedRegistrationFloor pins the listing: the versions a dep on 7.0.0 can update to are
// the same as without a floor, the pages that end below it are not fetched, a dep without a
// usable version still gets every page, and a second call is served from the memo.
func TestPagedRegistrationFloor(t *testing.T) {
	srv, reqs, _ := pagedFeed(t, 10, 8)
	full := pagedSource(srv)
	all, err := full.Releases(t.Context(), dep.Dep{Name: "Big.Lib"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 80 {
		t.Fatalf("unbounded listing = %d versions, want 80", len(all))
	}
	if n := reqs.Load(); n != 12 {
		t.Errorf("unbounded listing cost %d requests, want 12 (index, registration, 10 pages)", n)
	}

	reqs.Store(0)
	s := pagedSource(srv)
	d := dep.Dep{Name: "Big.Lib", Version: "7.0.0"}
	s.Plan([]dep.Dep{d})
	got, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := reqs.Load(); n != 5 {
		t.Errorf("bounded listing cost %d requests, want 5 (index, registration, pages 7-9)", n)
	}
	var want []datasource.Release
	for _, r := range all {
		if scheme.Compare(r.Version, "7.0.0") >= 0 {
			want = append(want, r)
		}
	}
	if !slices.Equal(versionsOf(got), versionsOf(want)) {
		t.Errorf("bounded listing = %v, want %v", versionsOf(got), versionsOf(want))
	}
	tm, err := s.ReleaseTime(t.Context(), d, "9.3.0")
	if err != nil || tm.IsZero() {
		t.Errorf("ReleaseTime(9.3.0) = %v, %v", tm, err)
	}
	// The dep's own version is on a page that is kept, so its release time still answers.
	if tm, err := s.ReleaseTime(t.Context(), d, "7.0.0"); err != nil || tm.IsZero() {
		t.Errorf("ReleaseTime(7.0.0) = %v, %v", tm, err)
	}

	reqs.Store(0)
	again, err := s.Releases(t.Context(), d, "")
	if err != nil || !slices.Equal(versionsOf(again), versionsOf(got)) {
		t.Errorf("second Releases = %v, %v", versionsOf(again), err)
	}
	if n := reqs.Load(); n != 0 {
		t.Errorf("second listing cost %d requests, want 0", n)
	}

	// A dep whose version is no NuGet version keeps the whole listing.
	reqs.Store(0)
	s2 := pagedSource(srv)
	ranged := dep.Dep{Name: "Big.Lib", Version: "[7.0.0,8.0.0)"}
	s2.Plan([]dep.Dep{ranged})
	un, err := s2.Releases(t.Context(), ranged, "")
	if err != nil || !slices.Equal(versionsOf(un), versionsOf(all)) {
		t.Errorf("unbounded listing of a ranged dep = %d versions, %v", len(un), err)
	}
	if n := reqs.Load(); n != 12 {
		t.Errorf("ranged dep cost %d requests, want 12", n)
	}
}

// TestPlanFloorIsLowestVersion pins that the lowest planned version wins, so a monorepo whose
// manifests pin different versions of a package still shares one listing.
func TestPlanFloorIsLowestVersion(t *testing.T) {
	srv, reqs, _ := pagedFeed(t, 10, 8)
	s := pagedSource(srv)
	low := dep.Dep{Name: "Big.Lib", Version: "3.1.0"}
	high := dep.Dep{Name: "big.lib", Version: "8.0.0"}
	s.Plan([]dep.Dep{high, low})
	a, err := s.Releases(t.Context(), high, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Releases(t.Context(), low, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(versionsOf(a), versionsOf(b)) {
		t.Errorf("listings differ: %v vs %v", versionsOf(a), versionsOf(b))
	}
	if got := versionsOf(a)[0]; got != "3.0.0" {
		t.Errorf("listing starts at %s, want 3.0.0 (the page of the lowest planned version)", got)
	}
	if n := reqs.Load(); n != 9 {
		t.Errorf("listing cost %d requests, want 9 (index, registration, pages 3-9)", n)
	}
}
