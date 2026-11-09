package javaversion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// testdata/jdk-page{0,1,2}.json are the three pages api.adoptium.net returned on 2026-11-06 for
// the JDK GA list (page_size=50; 50, 50 and 32 versions, page 3 is a 404).
func serve(t *testing.T, pages map[string]int, queries *[]string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v3/info/release_versions" || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		page := q.Get("page")
		q.Del("page")
		*queries = append(*queries, q.Encode())
		n, ok := pages[q.Get("image_type")]
		if p := int(page[0] - '0'); !ok || len(page) != 1 || p >= n {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/jdk-page" + page + ".json")
		if err != nil {
			t.Error(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(t *testing.T, pages map[string]int, queries *[]string, hits *atomic.Int32) *Source {
	srv := serve(t, pages, queries, hits)
	return New(srv.Client(), nil, func(k string) string {
		if k == "DEPS_JAVA_RELEASES_URL" {
			return srv.URL + "/v3/info/release_versions"
		}
		return ""
	})
}

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	var queries []string
	var hits atomic.Int32
	s := newSource(t, map[string]int{"jdk": 3}, &queries, &hits)
	ctx := context.Background()
	rs, err := s.Releases(ctx, dep.Dep{Name: JDK, Datasource: Name}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := versions(rs)
	// Newest first as listed (8.0.512+9 is the first entry of page 0), duplicates dropped,
	// then the derived lines.
	if got[0] != "8.0.512+9" || got[1] != "11.0.33+10" || got[2] != "27.0.1+1" {
		t.Errorf("first releases = %v", got[:3])
	}
	for _, v := range []string{"21.0.5+11.0.LTS", "17.0.7+7", "8.0.302+8", "27", "27.0", "21", "21.0", "8.0", "8"} {
		if !slices.Contains(got, v) {
			t.Errorf("releases lack %s: %v", v, got)
		}
	}
	seen := map[string]bool{}
	for _, v := range got {
		if seen[v] {
			t.Errorf("duplicate %s", v)
		}
		seen[v] = true
	}
	if i, j := slices.Index(got, "21"), slices.Index(got, "8.0.302+8"); i < j {
		t.Errorf("derived 21 at %d before published 8.0.302+8 at %d", i, j)
	}
	// Three pages: the third is short, so page 3 is not asked for.
	want := "image_type=jdk&page_size=50&project=jdk&release_type=ga&sort_method=DATE&sort_order=DESC"
	if len(queries) != 3 || queries[0] != want {
		t.Errorf("queries = %q, want 3 × %q", queries, want)
	}
	// java is the JDK: the list is shared.
	if _, err := s.Releases(ctx, dep.Dep{Name: "java"}, ""); err != nil || hits.Load() != 3 {
		t.Errorf("java: err %v, %d requests, want 3", err, hits.Load())
	}
	if _, err := s.Digest(ctx, dep.Dep{Name: JDK}, "21"); err == nil {
		t.Error("Digest: want unsupported")
	}
}

// A full last page is followed by a 404, which ends the list.
func TestReleasesEndOn404(t *testing.T) {
	var queries []string
	var hits atomic.Int32
	s := newSource(t, map[string]int{"jre": 2}, &queries, &hits)
	rs, err := s.Releases(context.Background(), dep.Dep{Name: JRE + "?os=linux&architecture=x64"}, "")
	if err != nil || len(rs) == 0 {
		t.Fatalf("releases = %d, err %v", len(rs), err)
	}
	if hits.Load() != 3 || !strings.Contains(queries[0], "architecture=x64") || !strings.Contains(queries[0], "os=linux") || !strings.Contains(queries[0], "image_type=jre") {
		t.Errorf("%d requests, queries %q", hits.Load(), queries)
	}
}

func TestReleasesErrors(t *testing.T) {
	var queries []string
	var hits atomic.Int32
	s := newSource(t, map[string]int{}, &queries, &hits)
	ctx := context.Background()
	for _, name := range []string{"java-jdk?vendor=x", "openjdk", "java?os=%zz", "java?os=a&os=b"} {
		if _, err := s.Releases(ctx, dep.Dep{Name: name}, ""); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests for invalid names", hits.Load())
	}
	// A 404 for the first page is an error, not an empty list.
	if _, err := s.Releases(ctx, dep.Dep{Name: JDK}, ""); err == nil || !errors.Is(err, httpx.ErrNotFound) || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("first page 404: err %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("image_type") {
		case "jre":
			w.Write([]byte(`{"versions":[]}`))
		case "jdk":
			w.Write([]byte(`{"versions":`))
		default:
			http.Error(w, "down", http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	s = New(srv.Client(), nil, func(string) string { return srv.URL })
	for name, want := range map[string]string{JRE: "no releases", JDK: "GET ", "java-jdk?os=x": "GET "} {
		if _, err := s.Releases(ctx, dep.Dep{Name: name}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", name, err, want)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	s = New(srv.Client(), make(chan struct{}), func(string) string { return srv.URL })
	if _, err := s.Releases(ctx, dep.Dep{Name: JDK}, ""); err == nil {
		t.Error("canceled: want an error")
	}
}
