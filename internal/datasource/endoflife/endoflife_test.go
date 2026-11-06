package endoflife

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
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// serve answers /<product>.json with the API's responses in testdata (fetched 2026-11-06),
// /legacy.json with numeric cycles and boolean fields, /bad.json with a non-array and an
// unknown product with HTTP 404 as the API does.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/nodejs.json", "/ubuntu.json", "/python.json":
			b, err := os.ReadFile("testdata" + r.URL.Path)
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		case "/legacy.json":
			w.Write([]byte(`[{"cycle":3,"latest":"3.4","eol":false,"discontinued":true},{"cycle":2,"eol":false,"releaseDate":"2020-01-02"},{"cycle":{}},{"cycle":1,"eol":true}]`))
		case "/bad.json":
			w.Write([]byte(`{"cycle":"1"}`))
		case "/empty.json":
			w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(url string) *Source {
	s := New(http.DefaultClient, nil, func(k string) string {
		if k == "DEPS_ENDOFLIFE_DATE_URL" {
			return url + "/"
		}
		return ""
	})
	s.now = func() time.Time { return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) }
	return s
}

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	s := newSource(serve(t, &hits).URL)
	ctx := context.Background()
	node := dep.Dep{Name: "nodejs"}
	rels, err := s.Releases(ctx, node, "")
	if err != nil {
		t.Fatal(err)
	}
	// 25 reached its end of life on 2026-06-01, 22 is supported until 2027-04-30.
	got := versions(rels)
	if !slices.Contains(got, "26.13.0") || !slices.Contains(got, "24.22.2") || !slices.Contains(got, "22.23.4") || slices.Contains(got, "25.9.0") {
		t.Fatalf("nodejs = %v", got)
	}
	if _, err := s.Releases(ctx, node, ""); err != nil || hits.Load() != 1 {
		t.Fatalf("second lookup: %v, hits %d", err, hits.Load())
	}
	if tm, _ := s.ReleaseTime(ctx, node, "26.13.0"); !tm.Equal(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ReleaseTime(26.13.0) = %v", tm)
	}
	for v, want := range map[string]string{
		"25.9.0":  "nodejs 25 reached its end of life on 2026-06-01",
		"v25.2.1": "nodejs 25 reached its end of life on 2026-06-01",
		"23.1.0":  "nodejs 23 reached its end of life on 2025-06-01",
		"24.1.0":  "",
		"99.0.0":  "",
	} {
		if got := s.Deprecation(node, v); got != want {
			t.Errorf("Deprecation(%s) = %q, want %q", v, got, want)
		}
	}

	ubuntu := dep.Dep{Name: "ubuntu"}
	rels, err = s.Releases(ctx, ubuntu, "")
	if err != nil || !slices.Contains(versions(rels), "24.04.5") || slices.Contains(versions(rels), "4.10") {
		t.Fatalf("ubuntu = %v, %v", versions(rels), err)
	}
	if got := s.Deprecation(ubuntu, "20.04"); !strings.HasPrefix(got, "ubuntu 20.04 reached its end of life on ") {
		t.Errorf("Deprecation(ubuntu 20.04) = %q", got)
	}
	if got := s.Deprecation(dep.Dep{Name: "nope"}, "1"); got != "" {
		t.Errorf("Deprecation(unknown) = %q", got)
	}

	legacy := dep.Dep{Name: "legacy"}
	rels, err = s.Releases(ctx, legacy, "")
	if err != nil || !slices.Equal(versions(rels), []string{"2"}) {
		t.Fatalf("legacy = %v, %v", versions(rels), err)
	}
	if tm, _ := s.ReleaseTime(ctx, legacy, "2"); !tm.Equal(time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ReleaseTime(2) = %v", tm)
	}
	if got := s.Deprecation(legacy, "3.4"); got != "legacy 3 was discontinued" {
		t.Errorf("Deprecation(3.4) = %q", got)
	}
	if got := s.Deprecation(legacy, "1.0"); got != "legacy 1 reached its end of life" {
		t.Errorf("Deprecation(1.0) = %q", got)
	}
}

func TestRegistryURL(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource("http://127.0.0.1:1")
	rels, err := s.Releases(context.Background(), dep.Dep{Name: "python", Source: srv.URL + "/"}, "")
	if err != nil || !slices.Contains(versions(rels), "3.14.10") {
		t.Fatalf("python = %v, %v", versions(rels), err)
	}
	// registryUrl is whitespace-separated: the first URL is the API base.
	rels, err = s.Releases(context.Background(), dep.Dep{Name: "python", Source: srv.URL + " http://127.0.0.1:1"}, "")
	if err != nil || !slices.Contains(versions(rels), "3.14.10") {
		t.Fatalf("python (two registry URLs) = %v, %v", versions(rels), err)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	s := newSource(serve(t, &hits).URL)
	ctx := context.Background()
	_, err := s.Releases(ctx, dep.Dep{Name: "nope"}, "")
	if skip, ok := errors.AsType[*dep.SkipError](err); !ok || skip.Reason != `unknown endoflife.date product "nope"` {
		t.Errorf("unknown product: %v", err)
	}
	for name, want := range map[string]string{"bad": "bad.json:", "empty": "no cycles", "a/b": "invalid endoflife.date product", "": "invalid"} {
		if _, err := s.Releases(ctx, dep.Dep{Name: name}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", name, err, want)
		}
		if _, err := s.ReleaseTime(ctx, dep.Dep{Name: name}, "1"); err == nil {
			t.Errorf("ReleaseTime(%q): no error", name)
		}
	}
	if _, err := s.Digest(ctx, dep.Dep{Name: "nodejs"}, "1"); err == nil {
		t.Error("Digest: no error")
	}
}
