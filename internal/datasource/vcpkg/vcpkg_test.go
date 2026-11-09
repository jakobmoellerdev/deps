package vcpkg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestRawBase(t *testing.T) {
	for repo, want := range map[string]string{
		"https://github.com/microsoft/vcpkg": "https://raw.githubusercontent.com/microsoft/vcpkg/c",
		"https://github.com/a/reg.git":       "https://raw.githubusercontent.com/a/reg/c",
		"https://git.example.com/a/reg/":     "https://git.example.com/a/reg/raw/c",
	} {
		if got := RawBase(repo, "c"); got != want {
			t.Errorf("RawBase(%s) = %s, want %s", repo, got, want)
		}
	}
	if FileURL("b/", "fmt") != "b/versions/f-/fmt.json" || FileURL("b", "") != "" {
		t.Error(FileURL("b/", "fmt"))
	}
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/c/versions/f-/fmt.json": // excerpt of microsoft/vcpkg versions/f-/fmt.json
			_, _ = w.Write([]byte(`{"versions": [
  {"git-tree": "a", "version": "11.1.4", "port-version": 1},
  {"git-tree": "b", "version": "11.1.4"},
  {"git-tree": "c", "version-semver": "11.0.2"},
  {"git-tree": "d", "version-date": "2024-01-15", "port-version": 0},
  {"git-tree": "e", "version-string": "vs-2022"},
  {"git-tree": "f", "version": "11.1.4"},
  {"git-tree": "g"}
]}`))
		case "/c/versions/d-/date.json":
			_, _ = w.Write([]byte(`{"versions": [{"version-date": "2024-02-01"}, {"version-date": "2024-01-15", "port-version": 2}, {"version": "1.0"}]}`))
		case "/c/versions/b-/bad.json":
			_, _ = w.Write([]byte(`{`))
		case "/c/versions/e-/err.json":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := New(srv.Client(), nil)
	d := dep.Dep{Name: "fmt", Source: srv.URL + "/c"}
	for range 2 {
		rels, err := s.Releases(t.Context(), d, "")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rels {
			got = append(got, r.Version)
		}
		if g := strings.Join(got, " "); g != "11.1.4#1 11.1.4 11.0.2" {
			t.Errorf("releases %s", g)
		}
	}
	if rels, err := s.Releases(t.Context(), dep.Dep{Name: "date", Source: srv.URL + "/c"}, ""); err != nil || len(rels) != 2 || rels[1].Version != "2024-01-15#2" {
		t.Errorf("date: %v %v", rels, err)
	}
	if hits.Load() != 2 {
		t.Errorf("%d requests, want 2", hits.Load())
	}
	for name, want := range map[string]string{
		"missing": "vcpkg port missing not found in the registry",
		"bad":     "unexpected EOF",
		"err":     "HTTP 500",
	} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name, Source: srv.URL + "/c"}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "fmt"}, ""); err == nil {
		t.Error("no source: no error")
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "fmt", Source: "http://\x00"}, ""); err == nil {
		t.Error("invalid URL: no error")
	}
	if _, err := s.Digest(t.Context(), d, "1"); err == nil {
		t.Error("digest: no error")
	}
}

// TestSourceRepo: the homepage of the port's manifest at the registry commit is the source
// repository when it is on a forge (microsoft/vcpkg ports/fmt/vcpkg.json, ports/curl/vcpkg.json).
func TestSourceRepo(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/c/ports/fmt/vcpkg.json":
			_, _ = w.Write([]byte(`{"name": "fmt", "version": "12.2.0", "port-version": 1, "homepage": "https://github.com/fmtlib/fmt", "license": "MIT"}`))
		case "/c/ports/curl/vcpkg.json":
			_, _ = w.Write([]byte(`{"name": "curl", "version": "8.23.0", "homepage": "https://curl.se/"}`))
		case "/c/ports/nohome/vcpkg.json":
			_, _ = w.Write([]byte(`{"name": "nohome", "version": "1"}`))
		case "/c/ports/bad/vcpkg.json":
			_, _ = w.Write([]byte(`{`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := New(srv.Client(), nil)
	for name, want := range map[string]string{"fmt": "https://github.com/fmtlib/fmt", "curl": "", "nohome": "", "missing": ""} {
		for range 2 {
			if got, err := s.SourceRepo(t.Context(), dep.Dep{Name: name, Source: srv.URL + "/c/"}, "1"); err != nil || got != want {
				t.Errorf("%s: %q, %v; want %q", name, got, err, want)
			}
		}
	}
	if hits.Load() != 4 {
		t.Errorf("%d requests, want 4", hits.Load())
	}
	if _, err := s.SourceRepo(t.Context(), dep.Dep{Name: "bad", Source: srv.URL + "/c"}, "1"); err == nil {
		t.Error("malformed manifest: no error")
	}
	if got, err := s.SourceRepo(t.Context(), dep.Dep{Name: "fmt"}, "1"); err != nil || got != "" {
		t.Errorf("no source: %q, %v", got, err)
	}
}
