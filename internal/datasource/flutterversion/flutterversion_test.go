package flutterversion

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// serve answers the Linux release list with the response in testdata (fetched 2026-11-06).
func serve(t *testing.T, hits *atomic.Int32, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/flutter_infra_release/releases/releases_linux.json" {
			http.NotFound(w, r)
			return
		}
		if body != "" {
			w.Write([]byte(body))
			return
		}
		b, err := os.ReadFile("testdata/releases_linux.json")
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
		if k == "FLUTTER_STORAGE_BASE_URL" {
			return url
		}
		return ""
	}
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits, "")
	s := New(srv.Client(), make(chan struct{}, 1), env(srv.URL+"/"))
	d := dep.Dep{Name: "flutter"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	var vs []string
	for _, r := range rs {
		vs = append(vs, r.Version)
	}
	if len(vs) != 687 {
		t.Errorf("releases = %d, want 687 (689 versions, 3.12.0 and others only listed as beta dropped)", len(vs))
	}
	for _, want := range []string{"3.13.3", "3.47.9", "3.50.0-0.4.pre", "v1.12.13+hotfix.9"} {
		if !slices.Contains(vs, want) {
			t.Errorf("missing %s", want)
		}
	}
	if !slices.IsSortedFunc(vs, func(a, b string) int { return semver.MustParse(a).Compare(semver.MustParse(b)) }) {
		t.Errorf("not sorted: %s … %s", vs[0], vs[len(vs)-1])
	}
	tm, err := s.ReleaseTime(t.Context(), d, "3.13.3")
	if want := time.Date(2023, 9, 7, 17, 19, 53, 810487000, time.UTC); err != nil || !tm.Equal(want) {
		t.Errorf("ReleaseTime(3.13.3) = %v, %v; want the earliest listing", tm, err)
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d", hits.Load())
	}
	if repo, _ := s.SourceRepo(t.Context(), d, ""); repo != Repo {
		t.Errorf("SourceRepo = %q", repo)
	}
	if _, err := s.Digest(t.Context(), d, "3.13.3"); err == nil {
		t.Error("Digest succeeded")
	}
}

func TestReleasesErrors(t *testing.T) {
	for _, body := range []string{`{"releases":[]}`, `[`} {
		var hits atomic.Int32
		srv := serve(t, &hits, body)
		s := New(srv.Client(), nil, env(srv.URL))
		if _, err := s.Releases(t.Context(), dep.Dep{}, ""); err == nil {
			t.Errorf("%s: no error", body)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{}, "3.13.3"); err == nil {
			t.Errorf("%s: ReleaseTime no error", body)
		}
	}
	if New(nil, nil, func(string) string { return "" }).url != DefaultURL+"/flutter_infra_release/releases/releases_linux.json" {
		t.Error("default URL")
	}
}
