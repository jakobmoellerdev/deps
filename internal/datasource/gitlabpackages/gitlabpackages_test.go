package gitlabpackages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// serve answers the packages API of project gitlab-org/cli with gitlab.com's two pages of
// package_name=glab (testdata, fetched 2026-11-06; 104 generic packages), a fuzzy match
// (a "glab-nightly" package and a Conan package named via conan_package_name) on project 42,
// HTTP 404 for unknown projects and a non-array for project "bad". It records the auth
// headers it saw.
func serve(t *testing.T, hits *atomic.Int32, headers *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		headers.Store(r.Header.Get("Authorization") + "|" + r.Header.Get("JOB-TOKEN"))
		q := r.URL.Query()
		if q.Get("per_page") != "100" {
			http.Error(w, "per_page", http.StatusBadRequest)
			return
		}
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/gitlab-org%2Fcli/packages":
			if q.Get("package_name") != "glab" {
				w.Write([]byte(`[]`))
				return
			}
			b, err := os.ReadFile("testdata/glab-page" + q.Get("page") + ".json")
			if err != nil {
				t.Error(err)
			}
			if q.Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
			} else {
				w.Header().Set("X-Next-Page", "")
			}
			w.Write(b)
		case "/api/v4/projects/42/packages":
			w.Write([]byte(`[
				{"name":"lib","version":"1.0.0","created_at":"2024-01-01T00:00:00Z"},
				{"name":"lib-nightly","version":"9.9.9","created_at":"2024-02-01T00:00:00Z"},
				{"name":"lib/2.0.0@user/stable","conan_package_name":"lib","version":"2.0.0","created_at":"2024-03-01T00:00:00Z"},
				{"name":"lib","version":"1.0.0","created_at":"2024-04-01T00:00:00Z"},
				{"name":"lib","version":"","created_at":"2024-04-01T00:00:00Z"}
			]`))
		case "/api/v4/projects/bad/packages":
			w.Write([]byte(`{"message":"x"}`))
		case "/api/v4/projects/next/packages":
			w.Header().Set("X-Next-Page", "two")
			w.Write([]byte(`[]`))
		default:
			http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleasesGlab(t *testing.T) {
	var hits atomic.Int32
	var headers atomic.Value
	srv := serve(t, &hits, &headers)
	s := New(srv.Client(), nil, nil, srv.URL+"/")
	d := dep.Dep{Datasource: Name, Name: "gitlab-org/cli:glab"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	vs := versions(rels)
	if len(vs) != 104 || vs[0] != "1.44.1" || vs[len(vs)-1] != "1.128.0" {
		t.Fatalf("got %d releases %v … %v", len(vs), vs[0], vs[len(vs)-1])
	}
	tm, err := s.ReleaseTime(t.Context(), d, "1.44.1")
	if err != nil || !tm.Equal(time.Date(2024, 7, 22, 17, 38, 34, 548e6, time.UTC)) {
		t.Fatalf("ReleaseTime = %v, %v", tm, err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("hits = %d, want 2 (two pages, memoized)", n)
	}
	if _, err := s.Digest(t.Context(), d, "1.44.1"); err == nil {
		t.Fatal("Digest: want error")
	}
}

func TestReleasesFilterAndRegistries(t *testing.T) {
	var hits atomic.Int32
	var headers atomic.Value
	srv := serve(t, &hits, &headers)
	host := strings.TrimPrefix(srv.URL, "http://")
	host, _, _ = strings.Cut(host, ":")
	auth := httpx.NewAuth(platform.Info{}, "", func(string) string { return "" }, httpx.WithToken(host, "oauth2", "pat"))
	s := New(srv.Client(), nil, auth, "https://gitlab.invalid")
	// The first registry lacks the project (404); the second, given with /api/v4, has it.
	d := dep.Dep{Datasource: Name, Name: "42:lib", Source: srv.URL + "/missing " + srv.URL + "/api/v4/"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(versions(rels), " "); got != "1.0.0 2.0.0" {
		t.Fatalf("versions = %q", got)
	}
	if h := headers.Load(); h != "Bearer pat|" {
		t.Fatalf("auth headers = %q", h)
	}

	jobAuth := httpx.NewAuth(platform.Info{}, "", func(string) string { return "" }, httpx.WithToken(host, "gitlab-ci-token", "job"))
	if _, err := New(srv.Client(), nil, jobAuth, srv.URL).Releases(t.Context(), dep.Dep{Name: "42:lib"}, ""); err != nil {
		t.Fatal(err)
	}
	if h := headers.Load(); h != "|job" {
		t.Fatalf("job auth headers = %q", h)
	}
}

// TestReleasesHelperAfter404 checks that without a static token a 404 (GitLab's answer for a
// private project) asks the git credential helper once and retries with its token.
func TestReleasesHelperAfter404(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer helper-token" {
			http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
			return
		}
		w.Write([]byte(`[{"name":"lib","version":"1.0.0","created_at":"2024-01-01T00:00:00Z"}]`))
	}))
	t.Cleanup(srv.Close)
	var calls atomic.Int32
	helper := httpx.WithHelper(func(_ context.Context, host string) (string, string, bool) {
		calls.Add(1)
		return "me", "helper-token", host == strings.TrimPrefix(srv.URL, "http://")
	})
	auth := httpx.NewAuth(platform.Info{}, "", func(string) string { return "" }, helper)
	rels, err := New(srv.Client(), nil, auth, srv.URL).Releases(t.Context(), dep.Dep{Name: "private:lib"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(versions(rels), " "); got != "1.0.0" {
		t.Fatalf("versions = %q", got)
	}
	if c, h := calls.Load(), hits.Load(); c != 1 || h != 2 {
		t.Fatalf("helper calls = %d, hits = %d; want 1, 2", c, h)
	}
}

func TestReleasesErrors(t *testing.T) {
	var hits atomic.Int32
	var headers atomic.Value
	srv := serve(t, &hits, &headers)
	s := New(srv.Client(), nil, nil, srv.URL)
	for _, tt := range []struct{ name, want string }{
		{"glab", "want <project>:<package>"},
		{":glab", "want <project>:<package>"},
		{"gitlab-org/cli:", "want <project>:<package>"},
		{"gitlab-org/cli:nope", `no package "nope"`},
		{"missing:x", "HTTP 404"},
		{"bad:x", "GET "},
		{"next:x", "invalid X-Next-Page"},
	} {
		_, err := s.Releases(t.Context(), dep.Dep{Name: tt.name}, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: tt.name}, "1"); err == nil {
			t.Errorf("%s: ReleaseTime: want error", tt.name)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New(srv.Client(), make(chan struct{}, 1), nil, srv.URL).Releases(ctx, dep.Dep{Name: "a:b"}, ""); err == nil {
		t.Fatal("canceled: want error")
	}
}

func TestNewDefault(t *testing.T) {
	if got := New(nil, nil, nil, "").apiBase(""); got != "https://gitlab.com/api/v4" {
		t.Fatalf("apiBase = %q", got)
	}
}
