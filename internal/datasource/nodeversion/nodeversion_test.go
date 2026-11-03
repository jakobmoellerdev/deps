package nodeversion

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// index is a trimmed nodejs.org/dist/index.json: newest first, with a prerelease and a file
// list per entry.
const index = `[
{"version":"v27.0.0-alpha.0.0.0","date":"2026-10-29","files":["linux-x64"],"lts":false},
{"version":"v27.1.0","date":"2026-10-20","files":["linux-x64"],"lts":false},
{"version":"v27.0.0","date":"2026-10-01","files":["linux-x64"],"lts":false},
{"version":"v26.13.0","date":"2026-09-28","files":["linux-x64"],"lts":"Iron"},
{"version":"v26.5.0","date":"bad","files":[],"lts":"Iron"},
{"version":"","date":"2026-01-01"}
]`

func serve(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/mirror/index.json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "DEPS_NODE_DIST_URL" {
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
	srv := serve(t, index, &hits)
	s := New(srv.Client(), make(chan struct{}, 1), env(srv.URL+"/mirror/"))
	d := dep.Dep{Name: "node"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"v27.0.0-alpha.0.0.0", "v27.1.0", "v27.0.0", "v26.13.0", "v26.5.0", "27.1", "27", "27.0", "26.13", "26", "26.5"}
	if got := versions(rs); !slices.Equal(got, want) {
		t.Errorf("releases = %v, want %v", got, want)
	}
	if _, err := s.Releases(t.Context(), d, ""); err != nil || hits.Load() != 1 {
		t.Errorf("second call: err %v, %d requests", err, hits.Load())
	}
	for v, want := range map[string]string{
		"v27.1.0": "2026-10-20", "27.1.0": "2026-10-20", "27": "2026-10-01", "27.0": "2026-10-01",
		"26": "2026-09-28", "26.5.0": "", "26.5": "", "1.0.0": "",
	} {
		got, err := s.ReleaseTime(t.Context(), d, v)
		if err != nil {
			t.Fatal(err)
		}
		if f := got.Format(time.DateOnly); want == "" && !got.IsZero() || want != "" && f != want {
			t.Errorf("ReleaseTime(%s) = %v, want %q", v, got, want)
		}
	}
	if _, err := s.Digest(t.Context(), d, "v27.1.0"); err == nil {
		t.Error("Digest: want error")
	}
}

// The published list serves files that write the version without "v" (netlify NODE_VERSION),
// with "v", and partial setup-node values: each keeps its style and precision.
func TestSchemesKeepStyle(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, index, &hits)
	rs, err := New(srv.Client(), nil, env(srv.URL+"/mirror")).Releases(t.Context(), dep.Dep{}, "")
	if err != nil {
		t.Fatal(err)
	}
	cands := versions(rs)
	for _, c := range []struct{ scheme, cur, want string }{
		{"semver", "26.5.0", "27.1.0"},
		{"semver", "v26.5.0", "v27.1.0"},
		{"semver", "27.0.0", "27.1.0"},
		{"tag", "26", "27"},
		{"tag", "26.5", "27.1"},
	} {
		s, _ := versioning.Get(c.scheme)
		got, ok := s.Best(c.cur, cands, nil, nil)
		r := s.Rank(c.cur, cands, nil, nil)
		if !ok || got != c.want || r.Len() == 0 || r.At(0).Version != c.want {
			t.Errorf("%s %s: Best = %q %v, Rank[0] = %+v, want %q", c.scheme, c.cur, got, ok, r, c.want)
		}
	}
	// A tie between the published 27.0.0 and the derived "27" resolves to the published one.
	s, _ := versioning.Get("semver")
	allow := func(v string, _ dep.UpdateType) (bool, string) { return !strings.Contains(v, "27.1"), "" }
	if got, _ := s.Best("26.5.0", cands, allow, nil); got != "27.0.0" {
		t.Errorf("Best with 27.1 rejected = %q, want 27.0.0", got)
	}
	if r := s.Rank("26.5.0", cands, allow, nil); r.At(0).Version != "27.0.0" {
		t.Errorf("Rank[0] with 27.1 rejected = %q, want 27.0.0", r.At(0).Version)
	}
}

func TestErrors(t *testing.T) {
	for name, body := range map[string]string{
		"not an array": `{"version":"v1.0.0"}`,
		"bad element":  `[{"version":1}]`,
		"empty":        `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			srv := serve(t, body, &hits)
			s := New(srv.Client(), nil, env(srv.URL+"/mirror"))
			if rs, err := s.Releases(t.Context(), dep.Dep{}, ""); err == nil || rs != nil {
				t.Errorf("Releases = %v, %v; want error", rs, err)
			}
			if _, err := s.ReleaseTime(t.Context(), dep.Dep{}, "1.0.0"); err == nil {
				t.Error("ReleaseTime: want the fetch error")
			}
		})
	}
	var hits atomic.Int32
	srv := serve(t, index, &hits)
	if _, err := New(srv.Client(), nil, env(srv.URL+"/other")).Releases(t.Context(), dep.Dep{}, ""); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("404: %v", err)
	}
	if _, err := New(srv.Client(), nil, env("http://[::1]:namedport")).Releases(t.Context(), dep.Dep{}, ""); err == nil {
		t.Error("bad URL: want error")
	}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New(srv.Client(), sem, env(srv.URL+"/mirror")).Releases(ctx, dep.Dep{}, ""); err == nil {
		t.Error("canceled while waiting for the semaphore: want error")
	}
	if s := New(nil, nil, env("")); s.url != DefaultDistURL+"/index.json" {
		t.Errorf("default url %q", s.url)
	}
}
