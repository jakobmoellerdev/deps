package elmpackage

import (
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

// serve answers /packages/elm/json/releases.json and /packages/elm/browser/releases.json with
// the registry's responses in testdata (fetched 2026-11-05), an unknown package with HTTP 500
// as the registry does, and /packages/bad/json/releases.json with a non-object.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/packages/elm/json/releases.json", "/packages/elm/browser/releases.json":
			b, err := os.ReadFile("testdata/elm-" + strings.Split(r.URL.Path, "/")[3] + ".json")
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		case "/packages/bad/json/releases.json":
			w.Write([]byte(`["1.0.0"]`))
		case "/packages/empty/json/releases.json":
			w.Write([]byte(`{"latest": 1}`))
		default:
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "DEPS_ELM_PACKAGE_URL" {
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
	d := dep.Dep{Name: "elm/json"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(rs), []string{"1.0.0", "1.1.0", "1.1.1", "1.1.2", "1.1.3", "1.1.4"}; !slices.Equal(got, want) {
		t.Errorf("releases = %v, want %v", got, want)
	}
	tm, err := s.ReleaseTime(t.Context(), d, "1.1.3")
	if err != nil || !tm.Equal(time.Unix(1550244958, 0)) {
		t.Errorf("release time = %v, %v", tm, err)
	}
	if tm, err := s.ReleaseTime(t.Context(), d, "9.9.9"); err != nil || !tm.IsZero() {
		t.Errorf("unknown release time = %v, %v", tm, err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "elm/browser"}, ""); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("requests = %d, want one per package", n)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL))
	for name, want := range map[string]string{
		"elm/nope":   "HTTP 500",
		"bad/json":   "GET " + srv.URL + "/packages/bad/json/releases.json: ",
		"empty/json": "no releases",
		"elm":        `invalid Elm package name "elm"`,
		"a/b/c":      `invalid Elm package name "a/b/c"`,
	} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: name}, "1.0.0"); err == nil {
			t.Errorf("%s: release time without error", name)
		}
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "elm/json"}, "1.0.0"); err == nil {
		t.Error("digest without error")
	}
}

func TestDefaultURLAndSourceRepo(t *testing.T) {
	s := New(http.DefaultClient, nil, func(string) string { return "" })
	if s.base != DefaultURL {
		t.Errorf("base = %q", s.base)
	}
	if r, err := s.SourceRepo(t.Context(), dep.Dep{Name: "elm/json"}, "1.1.3"); err != nil || r != "https://github.com/elm/json" {
		t.Errorf("source repo = %q, %v", r, err)
	}
	if r, err := s.SourceRepo(t.Context(), dep.Dep{Name: "json"}, ""); err != nil || r != "" {
		t.Errorf("invalid name source repo = %q, %v", r, err)
	}
}
