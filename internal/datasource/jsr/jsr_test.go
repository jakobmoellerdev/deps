package jsr

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// serve answers /@std/assert/meta.json and /@oak/oak/meta.json with jsr.io's responses in
// testdata (fetched 2026-11-09), /@acme/old/meta.json with a package published to before JSR
// recorded times, and unknown packages with HTTP 404 as jsr.io does.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/@std/assert/meta.json", "/@oak/oak/meta.json":
			b, err := os.ReadFile("testdata/" + strings.ReplaceAll(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/meta.json"), "/@"), "/", "-") + ".json")
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		case "/@acme/old/meta.json":
			w.Write([]byte(`{"scope":"acme","name":"old","latest":"0.2.0","versions":{"0.2.0":{},"0.1.0":{},"0.10.0-rc.1":{},"0.3.0":{"yanked":true}}}`))
		case "/@acme/empty/meta.json":
			w.Write([]byte(`{"scope":"acme","name":"empty","versions":{"1.0.0":{"yanked":true}}}`))
		default:
			http.Error(w, "Not Found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "JSR_URL" {
			return url
		}
		return ""
	}
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
	srv := serve(t, &hits)
	s := New(srv.Client(), make(chan struct{}, 1), env(srv.URL+"/"))
	d := dep.Dep{Name: "@std/assert"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	vs := versions(rs)
	if len(vs) != 61 || vs[0] != "0.196.0" || vs[len(vs)-1] != "1.0.19" || strings.Contains(" "+strings.Join(vs, " ")+" ", " 0.225.0 ") {
		t.Errorf("versions = %d %v…%v (0.225.0 is yanked)", len(vs), vs[0], vs[len(vs)-1])
	}
	tm, err := s.ReleaseTime(t.Context(), d, "1.0.19")
	if want := time.Date(2026, 2, 20, 7, 56, 26, 774064000, time.UTC); err != nil || !tm.Equal(want) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if got := s.Latest(d); got != "1.0.19" {
		t.Errorf("Latest = %q", got)
	}
	if got := s.Deprecation(d, "0.225.0"); got != "version 0.225.0 yanked" {
		t.Errorf("Deprecation(0.225.0) = %q", got)
	}
	if got := s.Deprecation(d, "1.0.19"); got != "" {
		t.Errorf("Deprecation(1.0.19) = %q", got)
	}
	if repo, err := s.SourceRepo(t.Context(), d, "1.0.19"); err != nil || repo != "" {
		t.Errorf("SourceRepo without githubRepository = %q, %v", repo, err)
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hits.Load())
	}
	oak := dep.Dep{Name: "@oak/oak"}
	if repo, err := s.SourceRepo(t.Context(), oak, ""); err != nil || repo != "https://github.com/oakserver/oak" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if _, err := s.Digest(t.Context(), d, "1.0.19"); err == nil {
		t.Error("Digest succeeded")
	}
}

func TestUntimed(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL))
	d := dep.Dep{Name: "@acme/old"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(versions(rs), " "); got != "0.1.0 0.2.0 0.10.0-rc.1" {
		t.Errorf("versions = %s", got)
	}
	if tm, _ := s.ReleaseTime(t.Context(), d, "0.2.0"); !tm.Equal(untimed) {
		t.Errorf("ReleaseTime without createdAt = %v, want %v", tm, untimed)
	}
	if tm, _ := s.ReleaseTime(t.Context(), d, "9.9.9"); !tm.IsZero() {
		t.Errorf("ReleaseTime of an unlisted version = %v", tm)
	}
	if s.Latest(dep.Dep{Name: "@acme/unfetched"}) != "" || s.Deprecation(dep.Dep{Name: "@acme/unfetched"}, "1.0.0") != "" {
		t.Error("Latest/Deprecation answered without a lookup")
	}
}

func TestCompare(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.1", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "x", -1},
		{"x", "1.0.0", 1},
		{"x", "y", -1},
	} {
		if got := compare(tt.a, tt.b); got != tt.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL))
	for _, name := range []string{"@acme/missing", "@acme/empty", "", "std/assert", "@std/Assert", "@std/assert/x"} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err == nil {
			t.Errorf("Releases(%q) succeeded", name)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: name}, "1.0.0"); err == nil {
			t.Errorf("ReleaseTime(%q) succeeded", name)
		}
		if _, err := s.SourceRepo(t.Context(), dep.Dep{Name: name}, "1.0.0"); err == nil {
			t.Errorf("SourceRepo(%q) succeeded", name)
		}
	}
	if New(nil, nil, func(string) string { return "" }).base != DefaultURL {
		t.Error("default registry")
	}
}
