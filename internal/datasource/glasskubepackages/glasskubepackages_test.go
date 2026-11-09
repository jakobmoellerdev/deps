package glasskubepackages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// serve answers like Glasskube's package repository: /packages/<name>/versions.yaml and
// /packages/<name>/<version>/package.yaml from testdata (fetched 2026-11-09 from
// https://packages.dl.glasskube.dev/packages), /dl/... with a 307 redirect to /packages/...
// as packages.dl.glasskube.dev does, /other/ with an empty repository (404), /broken/ with
// HTTP 500 and malformed or empty documents, and 404 for anything else. hits counts requests
// by path.
func serve(t *testing.T) (*httptest.Server, map[string]int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if rest, ok := strings.CutPrefix(r.URL.Path, "/dl/"); ok {
			http.Redirect(w, r, "/packages/"+rest, http.StatusTemporaryRedirect)
			return
		}
		file := map[string]string{
			"/packages/cert-manager/versions.yaml":            "testdata/cert-manager-versions.yaml",
			"/packages/cert-manager/v1.17.0+2/package.yaml":   "testdata/cert-manager-package.yaml",
			"/packages/cloudnative-pg/versions.yaml":          "testdata/cloudnative-pg-versions.yaml",
			"/packages/cloudnative-pg/v1.25.0+1/package.yaml": "testdata/cloudnative-pg-package.yaml",
			"/private/cloudnative-pg/versions.yaml":           "testdata/cloudnative-pg-versions.yaml",
			"/private/cloudnative-pg/v1.25.0+1/package.yaml":  "testdata/cloudnative-pg-package.yaml",
			"/broken/website-only/versions.yaml":              "testdata/cloudnative-pg-versions.yaml",
			"/broken/manifest-broken/versions.yaml":           "testdata/cloudnative-pg-versions.yaml",
			"/broken/website-only/v1.25.0+1/package.yaml":     "",
			"/broken/manifest-broken/v1.25.0+1/package.yaml":  "",
			"/broken/no-latest/versions.yaml":                 "",
			"/broken/down/versions.yaml":                      "",
			"/broken/malformed/versions.yaml":                 "",
			"/broken/empty/versions.yaml":                     "",
		}
		path, known := file[r.URL.Path]
		switch {
		case path != "":
			b, err := os.ReadFile(path)
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		case !known:
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/website-only/v1.25.0+1/package.yaml"):
			w.Write([]byte("name: x\nreferences:\n  - label: website\n    url: https://codeberg.org/o/r\n  - label: Website\n    url: https://example.org\n"))
		case strings.HasSuffix(r.URL.Path, "/manifest-broken/v1.25.0+1/package.yaml"):
			w.Write([]byte("references: [unclosed\n"))
		case strings.Contains(r.URL.Path, "/no-latest/"):
			w.Write([]byte("versions:\n  - version: v1.0.0+1\n  - version: \"\"\n"))
		case strings.Contains(r.URL.Path, "/down/"):
			http.Error(w, "internal server error", http.StatusInternalServerError)
		case strings.Contains(r.URL.Path, "/malformed/"):
			w.Write([]byte("versions: {version: [\n"))
		case strings.Contains(r.URL.Path, "/empty/"):
		}
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func versions(rs []datasource.Release) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return strings.Join(out, " ")
}

func TestReleases(t *testing.T) {
	srv, hits := serve(t)
	s := New(srv.Client(), make(chan struct{}, 1))
	// The repository answers with a redirect, as the default one does.
	d := dep.Dep{Name: "cert-manager", Source: srv.URL + "/dl/"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	const want = "v1.13.3+1 v1.14.2+1 v1.14.5+1 v1.15.0+1 v1.15.1+1 v1.15.2+1 v1.15.3+1 v1.16.1+1 v1.16.2+1 v1.16.3+1 v1.17.0+1 v1.17.0+2 v1.17.1+1 v1.17.2+1 v1.18.0+1 v1.18.1+1 v1.18.2+1 v1.19.0+1 v1.19.1+1"
	if got := versions(rs); got != want {
		t.Errorf("releases = %s\nwant %s", got, want)
	}
	if got := s.Latest(d); got != "v1.17.0+2" {
		t.Errorf("Latest = %q", got)
	}
	// cert-manager's manifest references only ArtifactHub.
	if repo, err := s.SourceRepo(t.Context(), d, "v1.19.1+1"); err != nil || repo != "" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if _, err := s.Releases(t.Context(), d, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SourceRepo(t.Context(), d, "v1.18.0+1"); err != nil {
		t.Fatal(err)
	}
	if hits["/dl/cert-manager/versions.yaml"] != 1 || hits["/dl/cert-manager/v1.17.0+2/package.yaml"] != 1 {
		t.Errorf("hits = %v, want each document once", hits)
	}
	if _, err := s.Digest(t.Context(), d, "v1.17.0+2"); err == nil {
		t.Error("Digest succeeded")
	}
}

// The first repository that has the package wins; its package.yaml names the source
// repository (Renovate's sourceUrl: the reference labeled GitHub).
func TestRepositoriesAndSourceRepo(t *testing.T) {
	srv, hits := serve(t)
	s := New(srv.Client(), nil)
	d := dep.Dep{Name: "cloudnative-pg", Source: srv.URL + "/other " + srv.URL + "/private/ " + srv.URL + "/packages"}
	if got := s.Latest(d); got != "" {
		t.Errorf("Latest before Releases = %q", got)
	}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 11 || rs[0].Version != "v1.23.1+1" || rs[10].Version != "v1.27.1+1" {
		t.Errorf("releases = %s", versions(rs))
	}
	if got := s.Latest(d); got != "v1.25.0+1" {
		t.Errorf("Latest = %q", got)
	}
	if repo, err := s.SourceRepo(t.Context(), d, "v1.27.1+1"); err != nil || repo != "https://github.com/cloudnative-pg/cloudnative-pg" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if hits["/other/cloudnative-pg/versions.yaml"] != 1 || hits["/private/cloudnative-pg/v1.25.0+1/package.yaml"] != 1 || hits["/packages/cloudnative-pg/versions.yaml"] != 0 {
		t.Errorf("hits = %v", hits)
	}
	if got := s.Latest(dep.Dep{Name: "cloudnative-pg", Source: srv.URL + "/packages"}); got != "" {
		t.Errorf("Latest of a repository not asked = %q", got)
	}

	// Without a GitHub reference, the first Website reference on a forge.
	if repo, err := s.SourceRepo(t.Context(), dep.Dep{Name: "website-only", Source: srv.URL + "/broken"}, ""); err != nil || repo != "https://codeberg.org/o/r" {
		t.Errorf("website-only: SourceRepo = %q, %v", repo, err)
	}
	// A versions.yaml without latestVersion names no manifest.
	nl := dep.Dep{Name: "no-latest", Source: srv.URL + "/broken"}
	if rs, err := s.Releases(t.Context(), nl, ""); err != nil || versions(rs) != "v1.0.0+1" {
		t.Errorf("no-latest releases = %v, %v", rs, err)
	}
	if repo, err := s.SourceRepo(t.Context(), nl, ""); err != nil || repo != "" || s.Latest(nl) != "" {
		t.Errorf("no-latest: SourceRepo = %q, %v; Latest = %q", repo, err, s.Latest(nl))
	}
}

func TestErrors(t *testing.T) {
	srv, _ := serve(t)
	s := New(srv.Client(), nil)
	for _, tt := range []struct {
		d    dep.Dep
		want string
	}{
		{dep.Dep{Name: "missing", Source: srv.URL + "/packages " + srv.URL + "/other"}, "glasskube package missing not found in " + srv.URL + "/packages " + srv.URL + "/other"},
		{dep.Dep{Name: "down", Source: srv.URL + "/broken"}, "HTTP 500"},
		{dep.Dep{Name: "malformed", Source: srv.URL + "/broken"}, "/broken/malformed/versions.yaml: yaml:"},
		{dep.Dep{Name: "empty", Source: srv.URL + "/broken"}, "/broken/empty/versions.yaml: no versions"},
		{dep.Dep{Name: "a/b"}, `invalid glasskube package name "a/b"`},
		{dep.Dep{}, `invalid glasskube package name ""`},
		{dep.Dep{Name: "x", Source: "https://user:secret@127.0.0.1:1/p"}, "connect"},
	} {
		_, err := s.Releases(t.Context(), tt.d, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.d.Name, err, tt.want)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: err leaks the password: %v", tt.d.Name, err)
		}
		if s.Latest(tt.d) != "" {
			t.Errorf("%s: Latest of a failed lookup", tt.d.Name)
		}
		if _, err := s.SourceRepo(t.Context(), tt.d, ""); err == nil {
			t.Errorf("%s: SourceRepo without error", tt.d.Name)
		}
	}
	if _, err := s.SourceRepo(t.Context(), dep.Dep{Name: "manifest-broken", Source: srv.URL + "/broken"}, ""); err == nil {
		t.Error("malformed package.yaml: SourceRepo without error")
	}
}
