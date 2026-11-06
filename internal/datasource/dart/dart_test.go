package dart

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

// serve answers /api/packages/http with pub.dev's response in testdata (fetched 2026-11-06),
// /hosted/api/packages/private with a listing holding a retracted version, and unknown
// packages with HTTP 404 as pub.dev does.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/api/packages/http":
			b, err := os.ReadFile("testdata/http.json")
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		case "/hosted/api/packages/private":
			w.Write([]byte(`{"name":"private","latest":{"pubspec":{"homepage":"https://github.com/acme/private"}},"versions":[
				{"version":"1.0.0","published":"2024-01-02T03:04:05.000Z"},{"version":"1.1.0","retracted":true},{"version":"1.2.0","retracted":null}]}`))
		case "/api/packages/empty":
			w.Write([]byte(`{"name":"empty","versions":[]}`))
		default:
			http.Error(w, `{"error":{"code":"NotFound"}}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "PUB_HOSTED_URL" {
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
	d := dep.Dep{Name: "http"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	vs := versions(rs)
	if len(vs) != 130 || vs[0] != "0.2.7+0" || vs[len(vs)-1] != "1.6.0" {
		t.Errorf("versions = %d %v…%v", len(vs), vs[0], vs[len(vs)-1])
	}
	tm, err := s.ReleaseTime(t.Context(), d, "1.6.0")
	if want := time.Date(2025, 11, 10, 18, 27, 56, 434747000, time.UTC); err != nil || !tm.Equal(want) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if repo, err := s.SourceRepo(t.Context(), d, "1.6.0"); err != nil || repo != "https://github.com/dart-lang/http/tree/master/pkgs/http" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hits.Load())
	}
	if _, err := s.Digest(t.Context(), d, "1.6.0"); err == nil {
		t.Error("Digest succeeded")
	}
}

func TestHostedRepository(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env("https://pub.invalid"))
	d := dep.Dep{Name: "private", Source: srv.URL + "/hosted/"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(versions(rs), " "); got != "1.0.0 1.2.0" {
		t.Errorf("versions = %s (retracted ones must be dropped)", got)
	}
	if tm, _ := s.ReleaseTime(t.Context(), d, "1.2.0"); !tm.IsZero() {
		t.Errorf("ReleaseTime without published = %v", tm)
	}
	if repo, _ := s.SourceRepo(t.Context(), d, ""); repo != "https://github.com/acme/private" {
		t.Errorf("SourceRepo = %q, want the homepage", repo)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL))
	for _, name := range []string{"missing", "empty", "", "a/b"} {
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
		t.Error("default repository")
	}
}
