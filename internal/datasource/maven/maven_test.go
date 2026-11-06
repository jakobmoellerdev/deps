package mavends

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// The fixtures are real maven-metadata.xml documents (fetched 2026-11-04):
//   - testdata/guava-maven-metadata.xml: https://repo.maven.apache.org/maven2/com/google/guava/guava/maven-metadata.xml
//   - testdata/kotlin-jvm-plugin-maven-metadata.xml: https://plugins.gradle.org/m2/org/jetbrains/kotlin/jvm/org.jetbrains.kotlin.jvm.gradle.plugin/maven-metadata.xml
//     (declares encoding US-ASCII and has a top-level <version> outside <versions>).

// repo is a fake Maven repository: path -> body for GET, plus Last-Modified for POM HEADs.
type repo struct {
	t     *testing.T
	files map[string]string
	times map[string]string // path -> Last-Modified
	codes map[string]int    // path -> forced status

	mu   sync.Mutex
	hits map[string]int // "METHOD path"
}

func (r *repo) serve() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits[req.Method+" "+req.URL.Path]++
		r.mu.Unlock()
		if !strings.HasPrefix(req.Header.Get("User-Agent"), "deps/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if c, ok := r.codes[req.URL.Path]; ok {
			w.WriteHeader(c)
			return
		}
		if lm, ok := r.times[req.URL.Path]; ok && req.Method == http.MethodHead {
			w.Header().Set("Last-Modified", lm)
			return
		}
		body, ok := r.files[req.URL.Path]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	r.t.Cleanup(srv.Close)
	return srv
}

func newRepo(t *testing.T) *repo {
	return &repo{t: t, files: map[string]string{}, times: map[string]string{}, codes: map[string]int{}, hits: map[string]int{}}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noenv(string) string { return "" }

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleasesCentralOverride(t *testing.T) {
	r := newRepo(t)
	r.files["/maven2/com/google/guava/guava/maven-metadata.xml"] = fixture(t, "guava-maven-metadata.xml")
	srv := r.serve()
	s := New(srv.Client(), make(chan struct{}, 1), func(k string) string {
		if k == "DEPS_MAVEN_CENTRAL_URL" {
			return srv.URL + "/maven2" // no trailing slash
		}
		return ""
	})
	d := dep.Dep{Name: "com.google.guava:guava"}
	for range 2 {
		rels, err := s.Releases(context.Background(), d, "")
		if err != nil {
			t.Fatal(err)
		}
		vs := versions(rels)
		if len(vs) != 162 || vs[0] != "r03" || !slices.Contains(vs, "33.3.1-jre") || !slices.Contains(vs, "33.3.1-android") {
			t.Fatalf("versions = %d %v", len(vs), vs[:3])
		}
	}
	// An explicit Central in Source is redirected to the override too.
	d.Source = Central
	if _, err := s.Releases(context.Background(), d, ""); err != nil {
		t.Fatal(err)
	}
	if n := r.hits["GET /maven2/com/google/guava/guava/maven-metadata.xml"]; n != 1 {
		t.Errorf("metadata fetched %d times, want 1", n)
	}
}

func TestReleasesRepositories(t *testing.T) {
	r := newRepo(t)
	r.files["/a/org/example/lib/maven-metadata.xml"] = `<metadata><versioning><versions><version>1.0</version><version>1.1</version></versions></versioning></metadata>`
	r.files["/b/org/example/lib/maven-metadata.xml"] = `<metadata><versioning><versions><version>1.1</version><version> 2.0 </version><version></version></versions></versioning></metadata>`
	r.codes["/broken/org/example/lib/maven-metadata.xml"] = http.StatusUnauthorized
	srv := r.serve()
	s := New(srv.Client(), nil, noenv)
	ctx := context.Background()

	d := dep.Dep{Name: "org.example:lib", Source: srv.URL + "/a " + srv.URL + "/missing/\n" + srv.URL + "/broken " + srv.URL + "/b/"}
	rels, err := s.Releases(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versions(rels); !slices.Equal(got, []string{"1.0", "1.1", "2.0"}) {
		t.Errorf("merged = %v", got)
	}

	d.Source = srv.URL + "/missing " + srv.URL + "/missing2"
	if _, err := s.Releases(ctx, d, ""); err == nil || err.Error() != "artifact org.example:lib not found" {
		t.Errorf("not found err = %v", err)
	}
	d.Source = srv.URL + "/missing " + srv.URL + "/broken"
	if _, err := s.Releases(ctx, d, ""); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("broken err = %v", err)
	}
}

func TestReleasesGradlePluginMarker(t *testing.T) {
	r := newRepo(t)
	const path = "/m2/org/jetbrains/kotlin/jvm/org.jetbrains.kotlin.jvm.gradle.plugin/"
	r.files[path+"maven-metadata.xml"] = fixture(t, "kotlin-jvm-plugin-maven-metadata.xml")
	r.times[path+"2.0.0/org.jetbrains.kotlin.jvm.gradle.plugin-2.0.0.pom"] = "Tue, 21 May 2024 07:01:26 GMT"
	srv := r.serve()
	s := New(srv.Client(), nil, noenv)
	// A plugin id is looked up as its marker artifact; the explicit marker name is equivalent.
	d := dep.Dep{Name: "org.jetbrains.kotlin.jvm", Source: srv.URL + "/m2/"}
	rels, err := s.Releases(context.Background(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	vs := versions(rels)
	if len(vs) != 212 || vs[0] != "0.0.1-test-1" || !slices.Contains(vs, "2.0.0") {
		t.Fatalf("versions = %d %v", len(vs), vs[:2])
	}
	marker, err := s.Releases(context.Background(), dep.Dep{Name: "org.jetbrains.kotlin.jvm:org.jetbrains.kotlin.jvm.gradle.plugin", Source: d.Source}, "")
	if err != nil || !slices.Equal(versions(marker), vs) {
		t.Errorf("marker name: %d versions, %v", len(marker), err)
	}
	tm, err := s.ReleaseTime(context.Background(), d, "2.0.0")
	if err != nil || !tm.Equal(time.Date(2024, 5, 21, 7, 1, 26, 0, time.UTC)) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if _, err := s.ReleaseTime(context.Background(), d, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if n := r.hits["HEAD "+path+"2.0.0/org.jetbrains.kotlin.jvm.gradle.plugin-2.0.0.pom"]; n != 1 {
		t.Errorf("pom HEADs = %d, want 1", n)
	}
}

func TestReleaseTimeUnknown(t *testing.T) {
	r := newRepo(t)
	r.files["/a/g/a/maven-metadata.xml"] = `<metadata><versioning><versions><version>1</version><version>2</version><version>3</version></versions></versioning></metadata>`
	r.times["/a/g/a/2/a-2.pom"] = "yesterday"
	r.codes["/a/g/a/3/a-3.pom"] = http.StatusNotFound
	srv := r.serve()
	s := New(srv.Client(), nil, noenv)
	d := dep.Dep{Name: "g:a", Source: srv.URL + "/a"}
	for _, v := range []string{"1", "2", "3", "4"} { // no header, unparsable, 404, not listed
		if tm, err := s.ReleaseTime(context.Background(), d, v); err != nil || !tm.IsZero() {
			t.Errorf("%s: ReleaseTime = %v, %v", v, tm, err)
		}
	}
	if _, err := s.ReleaseTime(context.Background(), dep.Dep{Name: "a b"}, "1"); err == nil {
		t.Error("bad name: no error")
	}
}

func TestErrors(t *testing.T) {
	r := newRepo(t)
	r.files["/a/g/a/maven-metadata.xml"] = `<metadata><versioning>`
	srv := r.serve()
	s := New(srv.Client(), nil, noenv)
	ctx := context.Background()
	if _, err := s.Releases(ctx, dep.Dep{Name: "g:a", Source: srv.URL + "/a"}, ""); err == nil || !strings.Contains(err.Error(), "malformed metadata") {
		t.Errorf("malformed: %v", err)
	}
	for _, name := range []string{"", ":a", "g:", "g:a:b", "g/x:a", "g :a", "a b"} {
		if _, err := s.Releases(ctx, dep.Dep{Name: name}, ""); err == nil || !strings.Contains(err.Error(), "is neither group:artifact nor a plugin id") {
			t.Errorf("%q: %v", name, err)
		}
	}
	if _, err := s.Digest(ctx, dep.Dep{Name: "g:a"}, "1"); err == nil {
		t.Error("Digest: no error")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	full := New(srv.Client(), make(chan struct{}), noenv) // no free slot
	if _, err := full.Releases(cctx, dep.Dep{Name: "g:b", Source: srv.URL + "/a"}, ""); err == nil {
		t.Error("canceled: no error")
	}
}

func TestModule(t *testing.T) {
	for name, want := range map[string]string{
		"org.jetbrains.kotlin.jvm":                    "org.jetbrains.kotlin.jvm:org.jetbrains.kotlin.jvm.gradle.plugin",
		"com.fasterxml.jackson.core:jackson-databind": "com.fasterxml.jackson.core:jackson-databind",
	} {
		if got := Module(name); got != want {
			t.Errorf("Module(%q) = %q, want %q", name, got, want)
		}
	}
}
