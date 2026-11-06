package dartversion

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// serve answers the objects list of the dart-archive bucket with the responses in testdata
// (fetched 2026-11-06; the dev channel has two pages), without their `items`.
func serve(t *testing.T, hits *atomic.Int32, fail string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		q := r.URL.Query()
		if r.URL.Path != "/storage/v1/b/dart-archive/o" || q.Get("delimiter") != "/" || q.Get("alt") != "json" {
			http.NotFound(w, r)
			return
		}
		file := map[string]string{"channels/stable/release/": "stable", "channels/beta/release/": "beta", "channels/dev/release/": "dev"}[q.Get("prefix")]
		if file == "dev" && q.Get("pageToken") == "ChtjaGFubmVscy9kZXYvcmVsZWFzZS80MzM4NC8=" {
			file = "dev-2"
		}
		if file == "" || file == fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		b, err := os.ReadFile("testdata/" + file + ".json")
		if err != nil {
			t.Error(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "DEPS_DART_ARCHIVE_URL" {
			return url
		}
		return ""
	}
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits, "")
	s := New(srv.Client(), make(chan struct{}, 2), env(srv.URL+"/"))
	rs, err := s.Releases(t.Context(), dep.Dep{Name: "dart"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var vs []string
	for _, r := range rs {
		vs = append(vs, r.Version)
	}
	if len(vs) != 1239 {
		t.Errorf("releases = %d, want 1239 (182 stable, 1057 beta and dev)", len(vs))
	}
	for _, want := range []string{"1.11.0", "3.5.4", "3.9.0-333.4.beta", "3.9.0-95.0.dev"} {
		if !slices.Contains(vs, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, bad := range []string{"latest", "45692", "3.5.4-0.0.dev"} {
		if slices.Contains(vs, bad) {
			t.Errorf("listed %s", bad)
		}
	}
	if !slices.IsSortedFunc(vs, func(a, b string) int { return semver.MustParse(a).Compare(semver.MustParse(b)) }) {
		t.Errorf("not sorted: %s … %s", vs[0], vs[len(vs)-1])
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "dart"}, ""); err != nil || hits.Load() != 4 {
		t.Errorf("requests = %d, want 4 (one per page, once per run), %v", hits.Load(), err)
	}
	if repo, _ := s.SourceRepo(t.Context(), dep.Dep{}, ""); repo != Repo {
		t.Errorf("SourceRepo = %q", repo)
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "dart"}, "3.5.4"); err == nil {
		t.Error("Digest succeeded")
	}
}

func TestReleasesError(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits, "dev-2")
	if _, err := New(srv.Client(), nil, env(srv.URL)).Releases(t.Context(), dep.Dep{}, ""); err == nil {
		t.Error("a failed page did not fail the list")
	}
	if New(nil, nil, func(string) string { return "" }).base != DefaultURL {
		t.Error("default URL")
	}
}
