package juliaregistry

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
)

// testdata/<path>/{Package,Versions}.toml are the General registry's files of JSON, DataFrames
// (1.6.0 is yanked) and CUTENSOR_jll (builds `+0`, `+1`), verbatim (fetched 2026-11-05).
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		p, ok := strings.CutPrefix(r.URL.Path, "/General/")
		switch {
		case r.URL.Path == "/private/P/Priv/Package.toml" || r.URL.Path == "/private/P/Priv/Versions.toml":
			if u, pw, ok := r.BasicAuth(); !ok || u != "bot" || pw != "s3cret" {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			if strings.HasSuffix(r.URL.Path, "Package.toml") {
				w.Write([]byte("name = \"Priv\"\nuuid = \"11111111-0000-0000-0000-000000000000\"\n"))
			} else {
				w.Write([]byte("[\"0.1.0\"]\ngit-tree-sha1 = \"x\"\n"))
			}
			return
		case p == "B/Broken/Package.toml" || p == "B/Broken/Versions.toml":
			w.Write([]byte("[x"))
			return
		case p == "D/Down/Package.toml" || p == "D/Down/Versions.toml":
			http.Error(w, "down", http.StatusBadGateway)
			return
		case p == "W/Wrong/Package.toml":
			w.Write([]byte("name = \"Other\"\n"))
			return
		case p == "W/Wrong/Versions.toml":
			w.Write(nil)
			return
		case !ok:
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/" + p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func source(srv *httptest.Server, base string) *Source {
	return New(srv.Client(), nil, func(k string) string {
		if k == "DEPS_JULIA_REGISTRY_URL" {
			return base
		}
		return ""
	})
}

func TestPath(t *testing.T) {
	for name, want := range map[string]string{"JSON": "J/JSON", "libsvm": "L/libsvm", "CUTENSOR_jll": "jll/C/CUTENSOR_jll", "libpng_jll": "jll/L/libpng_jll", "Ölm": "Ö/Ölm"} {
		if got := Path(name); got != want {
			t.Errorf("Path(%s) = %s, want %s", name, got, want)
		}
	}
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := source(srv, srv.URL+"/General/")
	d := dep.Dep{Name: "DataFrames", Source: "A93C6F00-e57d-5684-b7b6-d8193f3e46c0"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 69 || slices.Contains(rels, datasource.Release{Version: "1.6.0"}) || !slices.Contains(rels, datasource.Release{Version: "1.8.1"}) {
		t.Errorf("releases = %d %v", len(rels), rels)
	}
	if _, err := s.Releases(t.Context(), d, ""); err != nil || hits.Load() != 2 {
		t.Errorf("second lookup: %v, hits %d", err, hits.Load())
	}
	repo, err := s.SourceRepo(t.Context(), d, "1.8.1")
	if err != nil || repo != "https://github.com/JuliaData/DataFrames.jl.git" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	jll, err := s.Releases(t.Context(), dep.Dep{Name: "CUTENSOR_jll"}, "")
	if err != nil || !slices.Contains(jll, datasource.Release{Version: "2.8.1+1"}) {
		t.Errorf("CUTENSOR_jll = %v, %v", jll, err)
	}
	if _, err := s.Digest(t.Context(), d, "1.8.1"); err == nil {
		t.Error("Digest")
	}
}

func TestSkipsAndErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := source(srv, srv.URL+"/General")
	var skip *dep.SkipError
	_, err := s.Releases(t.Context(), dep.Dep{Name: "Private", Source: "x"}, "")
	if !errors.As(err, &skip) || skip.Reason != "package Private is not in the registry "+srv.URL+"/General" {
		t.Errorf("unknown package = %v", err)
	}
	_, err = s.Releases(t.Context(), dep.Dep{Name: "JSON", Source: "00000000-de6a-54ab-a142-c8b1cf79cde6"}, "")
	if !errors.As(err, &skip) || !strings.Contains(skip.Reason, "has UUID 682c06a0-de6a-54ab-a142-c8b1cf79cde6, not 00000000") {
		t.Errorf("other UUID = %v", err)
	}
	if _, err := s.SourceRepo(t.Context(), dep.Dep{Name: "Private"}, ""); err == nil {
		t.Error("SourceRepo of an unknown package")
	}
	for name, msg := range map[string]string{"Broken": "malformed TOML", "Down": "HTTP 502", "Wrong": `Package.toml names "Other", not "Wrong"`} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s = %v, want %s", name, err, msg)
		}
	}
	u := strings.Replace(srv.URL, "http://", "http://bot:s3cret@", 1) + "/private"
	p := source(srv, u)
	if rels, err := p.Releases(t.Context(), dep.Dep{Name: "Priv"}, ""); err != nil || len(rels) != 1 {
		t.Errorf("private = %v, %v", rels, err)
	}
	_, err = p.Releases(t.Context(), dep.Dep{Name: "Nope"}, "")
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "bot:xxxxx@") {
		t.Errorf("credentials in %v", err)
	}
	bad := New(srv.Client(), nil, func(string) string { return "http://[::1" })
	if _, err := bad.Releases(t.Context(), dep.Dep{Name: "JSON"}, ""); err == nil || strings.Contains(err.Error(), "[::1") {
		t.Errorf("invalid base = %v", err)
	}
	if New(srv.Client(), nil, func(string) string { return "" }).base != DefaultURL {
		t.Error("default base")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New(srv.Client(), make(chan struct{}), func(string) string { return srv.URL }).Releases(ctx, dep.Dep{Name: "JSON"}, ""); err == nil {
		t.Error("canceled while waiting for a slot")
	}
}
