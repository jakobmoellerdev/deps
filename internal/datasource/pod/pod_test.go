package pod

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
)

// serve answers the trunk CDN's shard files in testdata (fetched 2026-11-09) below /cdn, a
// private CDN below /private that has none of them, HTTP 500 below /broken, and the GitHub
// contents API of the spec repository org/specs, which shards at the top (no Specs/).
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch p := r.URL.Path; {
		case strings.HasPrefix(p, "/cdn/"):
			b, err := os.ReadFile("testdata/" + strings.TrimPrefix(p, "/cdn/"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		case strings.HasPrefix(p, "/broken/"):
			http.Error(w, "boom", http.StatusInternalServerError)
		case p == "/repos/org/specs/contents/d/a/2/Alamofire":
			_, _ = w.Write([]byte(`[{"name":"5.0.0-private","type":"dir"},{"name":"5.1.0-private","type":"dir"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(srv *httptest.Server) *Source {
	gh := githubapi.New(srv.Client(), nil, nil, githubapi.WithAPI(srv.URL, srv.URL+"/graphql"))
	return New(srv.Client(), make(chan struct{}, 1), gh, func(k string) string {
		if k == "DEPS_POD_URL" {
			return srv.URL + "/cdn/"
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

func TestShard(t *testing.T) {
	for name, want := range map[string][3]string{"Alamofire": {"d", "a", "2"}, "Firebase": {"0", "3", "5"}} {
		if got := shard(name); got != want {
			t.Errorf("shard(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv)
	rs, err := s.Releases(t.Context(), dep.Dep{Name: "Alamofire"}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := versions(rs)
	if len(got) < 90 || got[0] != "1.1.3" || !slices.Contains(got, "5.11.2") || !slices.Contains(got, "2.0.0-beta.1") {
		t.Errorf("Alamofire releases = %v", got)
	}
	// A subspec is looked up as its pod; trunk's names (and none) share one shard request.
	for _, src := range []string{"", "trunk", "https://cdn.cocoapods.org/", "https://github.com/CocoaPods/Specs.git"} {
		rs, err := s.Releases(t.Context(), dep.Dep{Name: "Alamofire/Core", Source: src}, "")
		if err != nil || len(rs) != len(got) {
			t.Errorf("source %q: %d releases, %v", src, len(rs), err)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("requests = %d, want one per shard", n)
	}
	rs, err = s.Releases(t.Context(), dep.Dep{Name: "Firebase/Analytics"}, "")
	if err != nil || !slices.Contains(versions(rs), "10.18.0") {
		t.Errorf("Firebase releases = %v, %v", versions(rs), err)
	}
}

func TestSourceOrder(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv)
	// The private CDN has no shard file: the pod comes from the next source.
	d := dep.Dep{Name: "Alamofire", Source: srv.URL + "/private/ trunk"}
	if rs, err := s.Releases(t.Context(), d, ""); err != nil || len(rs) < 90 {
		t.Errorf("fallback to trunk: %d releases, %v", len(rs), err)
	}
	// A GitHub spec repository answers first, with its own versions.
	d.Source = "https://github.com/org/specs.git " + srv.URL + "/cdn"
	if rs, err := s.Releases(t.Context(), d, ""); err != nil || !slices.Equal(versions(rs), []string{"5.0.0-private", "5.1.0-private"}) {
		t.Errorf("github spec repo = %v, %v", versions(rs), err)
	}
	d.Source = "git@github.com:org/specs.git"
	if rs, err := s.Releases(t.Context(), d, ""); err != nil || len(rs) != 2 {
		t.Errorf("scp-style github spec repo = %v, %v", versions(rs), err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "Firebase", Source: "https://github.com/org/specs.git"}, ""); err == nil || err.Error() != "pod Firebase not found in https://github.com/org/specs.git" {
		t.Errorf("missing in github spec repo: %v", err)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv)
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "NoSuchPodAtAll"}, ""); err == nil || !strings.HasPrefix(err.Error(), "pod NoSuchPodAtAll not found in ") {
		t.Errorf("unknown pod: %v", err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "Alamofire", Source: srv.URL + "/broken/ trunk"}, ""); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("broken source: %v", err)
	}
	for _, src := range []string{"https://gitlab.com/org/specs.git", "git@gitlab.com:org/specs.git", "ssh://git@github.com/org/specs.git"} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: "Alamofire", Source: src}, ""); err == nil || !strings.HasPrefix(err.Error(), "unsupported spec repository ") {
			t.Errorf("source %s: %v", src, err)
		}
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "Alamofire", Source: "https://github.com/org/specs/tree/main"}, ""); err == nil {
		t.Error("github URL that is no repository: want error")
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "Alamofire"}, "5.9.1"); err == nil {
		t.Error("digest: want error")
	}
}

func TestDefaultURL(t *testing.T) {
	s := New(http.DefaultClient, nil, nil, func(string) string { return "" })
	if s.base != DefaultURL {
		t.Errorf("base = %s", s.base)
	}
}
