package pythonversion

import (
	"context"
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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// testdata/releases.json is https://www.python.org/api/v2/downloads/release/ (2026-11-06)
// trimmed to eight entries in the API's order: stable releases of three lines, an rc, an
// alpha and a Python install manager release.
func newSource(t *testing.T, body string, hits *atomic.Int32) *Source {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/release/" || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.Client(), nil, func(k string) string {
		if k == "DEPS_PYTHON_RELEASES_URL" {
			return srv.URL + "/release/"
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
	b, err := os.ReadFile("testdata/releases.json")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	s := newSource(t, string(b), &hits)
	ctx := context.Background()
	d := dep.Dep{Name: "python", Datasource: Name}
	rs, err := s.Releases(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2.7.18", "3.13.0", "3.13.9", "3.14.0", "3.14.0rc3", "3.14.10", "3.15.0a1", "2.7", "2", "3.13", "3", "3.14"}
	if got := versions(rs); !slices.Equal(got, want) {
		t.Fatalf("releases = %v, want %v", got, want)
	}
	for v, want := range map[string]string{
		"3.14.10": "2026-10-25T04:15:45Z", "3.14": "2025-10-07T14:13:54Z", "3": "2024-10-07T15:18:24Z",
		"3.15.0a1": "2025-10-14T14:10:13Z", "2.7": "2020-04-20T14:18:29Z", "9.9.9": "",
	} {
		got, err := s.ReleaseTime(ctx, d, v)
		if err != nil {
			t.Fatal(err)
		}
		if w, _ := time.Parse(time.RFC3339, want); !got.Equal(w) {
			t.Errorf("ReleaseTime(%s) = %v, want %s", v, got, want)
		}
	}
	if _, err := s.Releases(ctx, d, ""); err != nil || hits.Load() != 1 {
		t.Errorf("second call: err %v, %d requests, want 1", err, hits.Load())
	}

	// pep440 skips the alpha for a stable current version; tag keeps a partial's precision.
	cands := versions(rs)
	pep440, _ := versioning.Get("pep440")
	if got, _ := pep440.Best("3.13.0", cands, nil, nil); got != "3.14.10" {
		t.Errorf("pep440 best from 3.13.0 = %q", got)
	}
	if got, _ := pep440.Best("3.14.0rc3", cands, nil, nil); got != "3.15.0a1" {
		t.Errorf("pep440 best from 3.14.0rc3 = %q", got)
	}
	tag, _ := versioning.Get("tag")
	if got, _ := tag.Best("3.13", cands, nil, nil); got != "3.14" {
		t.Errorf("tag best from 3.13 = %q", got)
	}
	if _, err := s.Digest(ctx, d, "3.14.10"); err == nil {
		t.Error("Digest: want unsupported")
	}
}

func TestReleasesEdges(t *testing.T) {
	ctx := context.Background()
	var hits atomic.Int32
	s := newSource(t, `[{"name":"Python 3.12.0","is_published":false,"release_date":"2023-10-02T00:00:00Z"},
		{"name":"Python 3.12.1","is_published":true,"release_date":null},
		{"name":"Python 3.12.2","is_published":true,"release_date":"2024-02-06T00:00:00Z"}]`, &hits)
	rs, err := s.Releases(ctx, dep.Dep{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(rs), []string{"3.12.1", "3.12.2", "3.12", "3"}; !slices.Equal(got, want) {
		t.Errorf("releases = %v, want %v", got, want)
	}
	if got, _ := s.ReleaseTime(ctx, dep.Dep{}, "3.12"); !got.Equal(time.Date(2024, 2, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ReleaseTime(3.12) = %v", got)
	}
	if got, _ := s.ReleaseTime(ctx, dep.Dep{}, "3.12.1"); !got.IsZero() {
		t.Errorf("ReleaseTime(3.12.1) = %v, want zero", got)
	}
}

func TestReleasesErrors(t *testing.T) {
	ctx := context.Background()
	var hits atomic.Int32
	for body, want := range map[string]string{
		"[]": "no releases",
		`[{"name":"Python install manager 26.4","is_published":true}]`: "no releases",
		`{"detail":"x"}`: "malformed release list",
		`[{"name":1}]`:   "cannot unmarshal",
	} {
		s := newSource(t, body, &hits)
		_, err := s.Releases(ctx, dep.Dep{}, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", body, err, want)
		}
		if _, err2 := s.ReleaseTime(ctx, dep.Dep{}, "3.12"); err2 == nil {
			t.Errorf("%q: ReleaseTime err nil", body)
		}
	}
}
