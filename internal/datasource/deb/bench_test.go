package deb

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// slowRepo serves one Packages.gz per component, each after delay, and reports the highest
// number of requests that were in flight at the same time.
func slowRepo(tb testing.TB, comps []string, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	tb.Helper()
	var buf bytes.Buffer
	for i := range 200 {
		fmt.Fprintf(&buf, "Package: pkg%d\nVersion: 1.%d-1\nArchitecture: amd64\n\n", i, i)
	}
	index := func(comp string) []byte {
		var out bytes.Buffer
		w := gzip.NewWriter(&out)
		_, _ = w.Write(buf.Bytes())
		_, _ = w.Write([]byte("Package: curl\nVersion: 7.88.1-" + comp + "\n\n"))
		_ = w.Close()
		return out.Bytes()
	}
	bodies := map[string][]byte{}
	for _, c := range comps {
		bodies["/debian/dists/bookworm/"+c+"/binary-amd64/Packages.gz"] = index(c)
	}
	var live, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := live.Add(1); n > peak.Load() {
			peak.Store(n)
		}
		defer live.Add(-1)
		b, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		time.Sleep(delay)
		_, _ = w.Write(b)
	}))
	tb.Cleanup(srv.Close)
	return srv, &peak
}

// BenchmarkReleasesComponents times one dep whose registry has three components, each index
// served after a delay: sequential lookups cost their sum, concurrent ones the longest.
func BenchmarkReleasesComponents(b *testing.B) {
	comps := []string{"main", "contrib", "non-free"}
	srv, _ := slowRepo(b, comps, 15*time.Millisecond)
	d := dep.Dep{Datasource: Name, Name: "curl", Source: srv.URL + "/debian?suite=bookworm&components=" + strings.Join(comps, ",") + "&binaryArch=amd64"}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		s := New(srv.Client(), nil, nil)
		s.Plan([]dep.Dep{d})
		if _, err := s.Releases(ctx, d, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// TestReleasesComponentsConcurrent pins both halves: the indexes of a registry's components
// are scanned at the same time, and the merged version list keeps component order.
func TestReleasesComponentsConcurrent(t *testing.T) {
	comps := []string{"main", "contrib", "non-free"}
	srv, peak := slowRepo(t, comps, 20*time.Millisecond)
	d := dep.Dep{Datasource: Name, Name: "curl", Source: srv.URL + "/debian?suite=bookworm&components=" + strings.Join(comps, ",") + "&binaryArch=amd64"}
	s := New(srv.Client(), nil, nil)
	s.Plan([]dep.Dep{d})
	rels, err := s.Releases(context.Background(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"7.88.1-main", "7.88.1-contrib", "7.88.1-non-free"}
	if got := versions(rels); !slices.Equal(got, want) {
		t.Errorf("Releases = %q, want %q", got, want)
	}
	if n := peak.Load(); n < int32(len(comps)) {
		t.Errorf("peak concurrent index requests = %d, want %d", n, len(comps))
	}
}
