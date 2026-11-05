package rubygems

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// testdata/nokogiri.json holds nine entries of https://rubygems.org/api/v1/versions/nokogiri.json
// verbatim (fetched 2026-11-05): 1.18.10, 1.18.9 and the prerelease 1.18.0.rc1, each as ruby,
// java and x86_64-linux-gnu builds; testdata/nokogiri.info the same versions' lines of
// https://rubygems.org/info/nokogiri.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	versions, err := os.ReadFile("testdata/nokogiri.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.ReadFile("testdata/nokogiri.info")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/v1/versions/nokogiri.json", "/legacy/api/v1/versions/nokogiri.json":
			w.Write(versions)
		case "/private/info/nokogiri":
			if u, p, ok := r.BasicAuth(); !ok || u != "bot" || p != "s3cret" {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			w.Write(info)
		case "/compact/info/nokogiri":
			w.Write(info)
		case "/api/v1/gems/nokogiri.json":
			fmt.Fprint(w, `{"name":"nokogiri","source_code_uri":"https://github.com/sparklemotion/nokogiri","homepage_uri":"https://nokogiri.org"}`)
		case "/api/v1/gems/homepage.json":
			fmt.Fprint(w, `{"name":"homepage","source_code_uri":null,"homepage_uri":"https://github.com/o/homepage"}`)
		case "/api/v1/gems/site.json":
			fmt.Fprint(w, `{"name":"site","homepage_uri":"https://site.example"}`)
		case "/api/v1/versions/broken.json":
			fmt.Fprint(w, `{"error":"x"}`)
		case "/api/v1/versions/failing.json":
			http.Error(w, "down", http.StatusInternalServerError)
		case "/compact/info/odd":
			fmt.Fprint(w, "no separator\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(srv *httptest.Server, env map[string]string) *Source {
	if env == nil {
		env = map[string]string{}
	}
	env["DEPS_RUBYGEMS_URL"] = srv.URL + "/"
	return New(srv.Client(), nil, func(k string) string { return env[k] })
}

func versionsOf(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestReleasesRubygemsOrg(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, nil)
	// "" and Gemfile's `source "https://rubygems.org"` are both rubygems.org (DEPS_RUBYGEMS_URL).
	for _, d := range []dep.Dep{{Name: "nokogiri"}, {Name: "nokogiri", Source: "https://rubygems.org/"}} {
		rels, err := s.Releases(t.Context(), d, "")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := versionsOf(rels), []string{"1.18.10", "1.18.9", "1.18.0.rc1"}; !slices.Equal(got, want) {
			t.Errorf("Releases(%q) = %v, want %v", d.Source, got, want)
		}
	}
	// The earliest build of each version.
	tm, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "nokogiri"}, "1.18.10")
	if want := time.Date(2025, 9, 15, 1, 53, 37, 668e6, time.UTC); err != nil || !tm.Equal(want) {
		t.Errorf("ReleaseTime = %v, %v, want %v", tm, err, want)
	}
	if tm, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "nokogiri"}, "9.9.9"); err != nil || !tm.IsZero() {
		t.Errorf("ReleaseTime(unknown) = %v, %v", tm, err)
	}
	if n := hits.Load(); n != 2 { // once per server key ("" and the rubygems.org URL)
		t.Errorf("hits = %d, want 2", n)
	}
}

func TestReleasesCustomSources(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	env := map[string]string{bundleKey(strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[0]): "bot:s3cret"}
	s := newSource(srv, env)
	want := []string{"1.18.0.rc1", "1.18.9", "1.18.10"} // compact index order, platform builds merged
	for _, src := range []string{srv.URL + "/private", srv.URL + "/compact/", srv.URL + "/nothing " + srv.URL + "/compact"} {
		rels, err := s.Releases(t.Context(), dep.Dep{Name: "nokogiri", Source: src}, "")
		if err != nil || !slices.Equal(versionsOf(rels), want) {
			t.Errorf("%s: Releases = %v, %v, want %v", src, versionsOf(rels), err, want)
		}
		if tm, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "nokogiri", Source: src}, "1.18.10"); err != nil || !tm.IsZero() {
			t.Errorf("%s: ReleaseTime = %v, %v (the compact index has no times)", src, tm, err)
		}
	}
	// Without a compact index entry, the versions API of the server.
	rels, err := s.Releases(t.Context(), dep.Dep{Name: "nokogiri", Source: srv.URL + "/legacy"}, "")
	if err != nil || len(rels) != 3 {
		t.Errorf("versions API fallback = %v, %v", versionsOf(rels), err)
	}
	// rubygems.org-only metadata is not asked of other servers.
	if repo, err := s.SourceRepo(t.Context(), dep.Dep{Name: "nokogiri", Source: srv.URL + "/compact"}, "1.18.10"); err != nil || repo != "" {
		t.Errorf("SourceRepo(custom) = %q, %v", repo, err)
	}
}

func TestReleasesErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, nil)
	for _, tt := range []struct {
		d    dep.Dep
		want string
	}{
		{dep.Dep{Name: "missing"}, "gem missing not found"},
		{dep.Dep{Name: "missing", Source: srv.URL + "/a " + srv.URL + "/b"}, "gem missing not found"},
		{dep.Dep{Name: "broken"}, "malformed response"},
		{dep.Dep{Name: "failing"}, "HTTP 500"},
		{dep.Dep{Name: "odd", Source: srv.URL + "/compact"}, "not a compact index"},
		{dep.Dep{Name: "nokogiri", Source: srv.URL + "/private"}, "HTTP 401"}, // no BUNDLE_<HOST> credentials
		{dep.Dep{Name: "x", Source: "ftp://gems.example"}, "rubygems: invalid source URL for gem x"},
	} {
		_, err := s.Releases(t.Context(), tt.d, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %q: err = %v, want %q", tt.d.Name, tt.d.Source, err, tt.want)
		}
	}
	// Userinfo is redacted in errors (the compact index 404s, the versions API fails).
	_, err := s.Releases(t.Context(), dep.Dep{Name: "failing", Source: strings.Replace(srv.URL, "http://", "http://u:pw@", 1)}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || strings.Contains(err.Error(), "pw") {
		t.Errorf("err = %v", err)
	}
}

func TestSourceRepo(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, nil)
	for name, want := range map[string]string{
		"nokogiri": "https://github.com/sparklemotion/nokogiri", "homepage": "https://github.com/o/homepage", "site": "", "missing": "",
	} {
		if got, err := s.SourceRepo(t.Context(), dep.Dep{Name: name}, "1.0.0"); err != nil || got != want {
			t.Errorf("SourceRepo(%s) = %q, %v, want %q", name, got, err, want)
		}
	}
	n := hits.Load()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { _, _ = s.SourceRepo(t.Context(), dep.Dep{Name: "nokogiri"}, "1.18.9") })
	}
	wg.Wait()
	if hits.Load() != n {
		t.Error("SourceRepo is not memoized per gem")
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "nokogiri"}, "1.18.10"); err == nil {
		t.Error("Digest supported")
	}
}

func TestBundleKey(t *testing.T) {
	if got := bundleKey("gems.example-corp.com"); got != "BUNDLE_GEMS__EXAMPLE___CORP__COM" {
		t.Errorf("bundleKey = %s", got)
	}
}

func TestReleasesSharedAcrossCallers(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			if _, err := s.Releases(t.Context(), dep.Dep{Name: "nokogiri"}, ""); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("hits = %d, want 1", n)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "missing"}, ""); !strings.Contains(fmt.Sprint(err), "not found") || errors.Is(err, errNotFound) {
		t.Errorf("not found error = %v", err)
	}
}
