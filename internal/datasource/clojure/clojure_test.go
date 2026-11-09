package clojure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// server serves Clojars below /clojars/ and Maven Central below /central/ from testdata
// (404 otherwise) and records the requested paths.
type server struct {
	*httptest.Server
	mu   sync.Mutex
	hits []string
}

func newServer(t *testing.T) *server {
	t.Helper()
	files := map[string]string{
		"/clojars/ring/ring-core/maven-metadata.xml":          "testdata/ring-core-maven-metadata.xml",
		"/central/org/clojure/tools.cli/maven-metadata.xml":   "testdata/tools.cli-maven-metadata.xml",
		"/private/org/clojure/tools.cli/maven-metadata.xml":   "testdata/tools.cli-maven-metadata.xml",
		"/clojars/ring/ring-core/1.15.6/ring-core-1.15.6.pom": "",
	}
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits = append(s.hits, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		f, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Last-Modified", "Tue, 15 Apr 2025 10:00:00 GMT")
		if f == "" {
			_, _ = w.Write([]byte(`<project><scm><url>https://github.com/ring-clojure/ring</url></scm></project>`))
			return
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func newSource(s *server) *Source {
	env := map[string]string{"DEPS_CLOJARS_URL": s.URL + "/clojars", "DEPS_MAVEN_CENTRAL_URL": s.URL + "/central"}
	getenv := func(k string) string { return env[k] }
	return New(mavends.New(s.Client(), nil, getenv), getenv)
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

// TestReleasesDefaults looks a Clojars artifact (ring's, from Clojars' real metadata) and a
// Maven Central one (org.clojure/tools.cli) up without repositories of their own: both
// default repositories are asked.
func TestReleasesDefaults(t *testing.T) {
	s := newServer(t)
	src := newSource(s)
	ctx := context.Background()
	rels, err := src.Releases(ctx, dep.Dep{Name: "ring:ring-core"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if vs := versions(rels); len(vs) != 131 || !slices.Contains(vs, "1.15.6") {
		t.Errorf("ring-core: %d versions %v", len(vs), vs)
	}
	rels, err = src.Releases(ctx, dep.Dep{Name: "org.clojure:tools.cli"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if vs := versions(rels); len(vs) != 26 || !slices.Contains(vs, "1.1.230") {
		t.Errorf("tools.cli: %d versions %v", len(vs), vs)
	}
	for _, want := range []string{"GET /clojars/ring/ring-core/maven-metadata.xml", "GET /central/ring/ring-core/maven-metadata.xml",
		"GET /clojars/org/clojure/tools.cli/maven-metadata.xml", "GET /central/org/clojure/tools.cli/maven-metadata.xml"} {
		if !slices.Contains(s.hits, want) {
			t.Errorf("missing request %s in %v", want, s.hits)
		}
	}
	if _, err := src.Releases(ctx, dep.Dep{Name: "no:such"}, ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing artifact: %v", err)
	}
}

// TestReleasesOwnRepositories uses the dep's repositories alone (the manager adds the defaults
// it wants).
func TestReleasesOwnRepositories(t *testing.T) {
	s := newServer(t)
	src := newSource(s)
	rels, err := src.Releases(context.Background(), dep.Dep{Name: "org.clojure:tools.cli", Source: s.URL + "/private/"}, "")
	if err != nil || len(rels) != 26 {
		t.Fatalf("releases %d, %v", len(rels), err)
	}
	if slices.ContainsFunc(s.hits, func(h string) bool { return !strings.HasPrefix(h, "GET /private/") }) {
		t.Errorf("requests outside the dep's repository: %v", s.hits)
	}
}

func TestReleaseTimeSourceRepoDigest(t *testing.T) {
	s := newServer(t)
	src := newSource(s)
	ctx := context.Background()
	d := dep.Dep{Name: "ring:ring-core"}
	if _, err := src.Releases(ctx, d, ""); err != nil {
		t.Fatal(err)
	}
	tm, err := src.ReleaseTime(ctx, d, "1.15.6")
	if err != nil || tm.IsZero() {
		t.Errorf("release time %v, %v", tm, err)
	}
	repo, err := src.SourceRepo(ctx, d, "1.15.6")
	if err != nil || repo != "https://github.com/ring-clojure/ring" {
		t.Errorf("source repo %q, %v", repo, err)
	}
	if _, err := src.Digest(ctx, d, "1.15.6"); err == nil {
		t.Error("digest: want unsupported")
	}
}

func TestDefaultURLs(t *testing.T) {
	src := New(mavends.New(http.DefaultClient, nil, func(string) string { return "" }), func(string) string { return "" })
	if got := src.withRepos(dep.Dep{}).Source; got != Clojars+" "+mavends.Central {
		t.Errorf("defaults %q", got)
	}
	if got := src.withRepos(dep.Dep{Source: "https://example.com/m2/"}).Source; got != "https://example.com/m2/" {
		t.Errorf("own repositories %q", got)
	}
}

func TestClojarsOverrideInOwnRepositories(t *testing.T) {
	getenv := func(k string) string {
		if k == "DEPS_CLOJARS_URL" {
			return "https://mirror.example.com/clojars"
		}
		return ""
	}
	src := New(mavends.New(http.DefaultClient, nil, getenv), getenv)
	got := src.withRepos(dep.Dep{Source: mavends.Central + " " + Clojars + " https://example.com/m2/"}).Source
	if want := mavends.Central + " https://mirror.example.com/clojars/ https://example.com/m2/"; got != want {
		t.Errorf("Source %q, want %q", got, want)
	}
}
