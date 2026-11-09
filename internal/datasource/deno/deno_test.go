package deno

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// serve answers the CDN paths of oak and std with cdn.deno.land's responses in testdata
// (fetched 2026-11-09; the directory listings of the version metadata cut to three entries),
// and unknown modules with HTTP 404 as the CDN does.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	files := map[string]string{
		"/oak/meta/versions.json":               "oak-versions.json",
		"/std/meta/versions.json":               "std-versions.json",
		"/oak/versions/v12.6.1/meta/meta.json":  "oak-v12.6.1-meta.json",
		"/oak/versions/v17.2.0/meta/meta.json":  "oak-v17.2.0-meta.json",
		"/local/meta/versions.json":             "",
		"/local/versions/1.0.0/meta/meta.json":  "",
		"/empty/meta/versions.json":             "",
		"/broken/versions/1.0.0/meta/meta.json": "",
		"/broken/meta/versions.json":            "",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		name, ok := files[r.URL.Path]
		switch {
		case !ok:
			http.Error(w, "Not Found", http.StatusNotFound)
		case r.URL.Path == "/local/meta/versions.json":
			w.Write([]byte(`{"latest":null,"versions":["1.0.0","0.9.0"]}`))
		case r.URL.Path == "/local/versions/1.0.0/meta/meta.json":
			w.Write([]byte(`{"uploaded_at":"not a time","upload_options":{"type":"gitlab","repository":"acme/local","ref":"1.0.0"}}`))
		case r.URL.Path == "/empty/meta/versions.json":
			w.Write([]byte(`{"latest":null,"versions":[]}`))
		case name == "":
			w.Write([]byte(`{`))
		default:
			b, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "DEPS_DENO_URL" {
			return url
		}
		return ""
	}
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), make(chan struct{}, 1), env(srv.URL+"/"))
	d := dep.Dep{Name: "https://deno.land/x/oak"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 103 || rs[0].Version != "v17.2.0" || rs[len(rs)-1].Version != "v1.0.0" {
		t.Errorf("versions = %d %v…%v", len(rs), rs[0], rs[len(rs)-1])
	}
	if got := s.Latest(d); got != "v17.2.0" {
		t.Errorf("Latest = %q", got)
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d, want 1 (times and repository are looked up lazily)", hits.Load())
	}
	tm, err := s.ReleaseTime(t.Context(), d, "v12.6.1")
	if want := time.Date(2023, 8, 30, 3, 31, 55, 87000000, time.UTC); err != nil || !tm.Equal(want) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if repo, err := s.SourceRepo(t.Context(), d, "v12.6.1"); err != nil || repo != "https://github.com/oakserver/oak" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if _, err := s.ReleaseTime(t.Context(), d, "v12.6.1"); err != nil || hits.Load() != 3 {
		t.Errorf("requests = %d, want 3 (metadata fetched once per version)", hits.Load())
	}
	std, err := s.Releases(t.Context(), dep.Dep{Name: "https://deno.land/std"}, "")
	if err != nil || len(std) == 0 || std[0].Version != "0.224.0" {
		t.Errorf("std versions = %v, %v", std, err)
	}
	if _, err := s.Digest(t.Context(), d, "v12.6.1"); err == nil {
		t.Error("Digest succeeded")
	}
}

func TestNonGitHubUpload(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL))
	d := dep.Dep{Name: "https://deno.land/x/local"}
	if got := s.Latest(d); got != "" {
		t.Errorf("Latest before lookup = %q", got)
	}
	if repo, err := s.SourceRepo(t.Context(), d, ""); err != nil || repo != "" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if got := s.Latest(d); got != "1.0.0" {
		t.Errorf("Latest without one listed = %q, want the newest", got)
	}
	if tm, err := s.ReleaseTime(t.Context(), d, "1.0.0"); err != nil || !tm.IsZero() {
		t.Errorf("ReleaseTime with an invalid uploaded_at = %v, %v", tm, err)
	}
}

func TestModule(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		ok         bool
	}{
		{"https://deno.land/x/oak", "oak", true},
		{"https://deno.land/x/deno_kv_oauth", "deno_kv_oauth", true},
		{"https://deno.land/std", "std", true},
		{"https://deno.land/x/", "", false},
		{"https://deno.land/x/Oak", "Oak", false},
		{"https://deno.land/oak", "", false},
		{"https://example.com/x/oak", "", false},
	} {
		if got, ok := Module(tt.name); got != tt.want || ok != tt.ok {
			t.Errorf("Module(%q) = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL))
	for _, name := range []string{"https://deno.land/x/missing", "https://deno.land/x/empty", "https://deno.land/x/broken", "", "oak"} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err == nil {
			t.Errorf("Releases(%q) succeeded", name)
		}
		if _, err := s.SourceRepo(t.Context(), dep.Dep{Name: name}, ""); err == nil {
			t.Errorf("SourceRepo(%q) succeeded", name)
		}
	}
	for _, tt := range []struct{ name, version string }{
		{"oak", "1.0.0"},
		{"https://deno.land/x/oak", ""},
		{"https://deno.land/x/oak", "v1/../x"},
		{"https://deno.land/x/broken", "1.0.0"},
		{"https://deno.land/x/missing", "1.0.0"},
	} {
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: tt.name}, tt.version); err == nil {
			t.Errorf("ReleaseTime(%q, %q) succeeded", tt.name, tt.version)
		}
	}
	if New(nil, nil, func(string) string { return "" }).base != DefaultURL {
		t.Error("default URL")
	}
}
