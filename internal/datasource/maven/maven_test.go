package mavends

import (
	"context"
	"errors"
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

// headHits counts the POM HEADs the repository served.
func (r *repo) headHits() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for req, hits := range r.hits {
		if strings.HasPrefix(req, http.MethodHead+" ") {
			n += hits
		}
	}
	return n
}

// metadataWith renders a maven-metadata.xml listing versions, with the lastUpdated element
// when it is not empty.
func metadataWith(lastUpdated string, versions ...string) string {
	var b strings.Builder
	b.WriteString("<metadata><versioning><versions>")
	for _, v := range versions {
		b.WriteString("<version>" + v + "</version>")
	}
	b.WriteString("</versions>")
	if lastUpdated != "" {
		b.WriteString("<lastUpdated>" + lastUpdated + "</lastUpdated>")
	}
	b.WriteString("</versioning></metadata>")
	return b.String()
}

// minAgeLookups does what the engine does for minimumReleaseAge (resolver.releasedBefore):
// it asks for the time of every candidate unless the datasource already bounds its releases
// at or before cutoff.
func minAgeLookups(t *testing.T, s *Source, d dep.Dep, versions []string, cutoff time.Time) {
	t.Helper()
	if bound := s.ReleasedBy(d); !bound.IsZero() && !bound.After(cutoff) {
		return
	}
	for _, v := range versions {
		if _, err := s.ReleaseTime(context.Background(), d, v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReleasedBy(t *testing.T) {
	versions := []string{"1", "2", "3"}
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		lastUpdated string
		want        time.Time
		heads       int // POM HEADs the minimumReleaseAge lookups need
	}{
		{"idle", "20250317094500", time.Date(2025, 3, 17, 9, 45, 0, 0, time.UTC), 0},
		{"recent", "20261102180748", time.Date(2026, 11, 2, 18, 7, 48, 0, time.UTC), 3},
		{"absent", "", time.Time{}, 3},
		{"other layout", "2025-03-17T09:45:00Z", time.Time{}, 3},
		{"placeholder", "19700101000000", time.Time{}, 3},
		{"not a date", "20251345000000", time.Time{}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRepo(t)
			r.files["/m2/g/a/maven-metadata.xml"] = metadataWith(tt.lastUpdated, versions...)
			for _, v := range versions {
				r.times["/m2/g/a/"+v+"/a-"+v+".pom"] = "Mon, 17 Mar 2025 09:45:00 GMT"
			}
			srv := r.serve()
			s := New(srv.Client(), nil, noenv)
			d := dep.Dep{Name: "g:a", Source: srv.URL + "/m2/"}
			if _, err := s.Releases(context.Background(), d, ""); err != nil {
				t.Fatal(err)
			}
			var bound datasource.ReleaseBound = s
			if got := bound.ReleasedBy(d); !got.Equal(tt.want) {
				t.Errorf("ReleasedBy = %v, want %v", got, tt.want)
			}
			minAgeLookups(t, s, d, versions, cutoff)
			if n := r.headHits(); n != tt.heads {
				t.Errorf("POM HEADs = %d, want %d", n, tt.heads)
			}
		})
	}
}

func TestReleasedByRepositories(t *testing.T) {
	r := newRepo(t)
	r.files["/dated/g/a/maven-metadata.xml"] = metadataWith("20250317094500", "1")
	r.files["/undated/g/a/maven-metadata.xml"] = metadataWith("", "1", "2")
	r.codes["/broken/g/a/maven-metadata.xml"] = http.StatusInternalServerError
	srv := r.serve()
	s := New(srv.Client(), nil, noenv)
	dated := time.Date(2025, 3, 17, 9, 45, 0, 0, time.UTC)
	tests := []struct {
		name  string
		repos string
		want  time.Time
	}{
		// A repository without the artifact says nothing about its releases; one that has it
		// without a lastUpdated, or that failed, could hold a newer release than the bound.
		{"missing artifact", "dated missing", dated},
		{"undated repository", "dated undated", time.Time{}},
		{"failed repository", "dated broken", time.Time{}},
		{"nowhere", "missing", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var src []string
			for name := range strings.FieldsSeq(tt.repos) {
				src = append(src, srv.URL+"/"+name+"/")
			}
			// The dep file keeps the repositories of each case apart.
			d := dep.Dep{Name: "g:a", File: tt.name, Source: strings.Join(src, " ")}
			_, err := s.Releases(context.Background(), d, "")
			if err != nil && !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			if got := s.ReleasedBy(d); !got.Equal(tt.want) {
				t.Errorf("ReleasedBy = %v, want %v", got, tt.want)
			}
		})
	}
	// Without a Releases lookup nothing is known of the artifact.
	if got := s.ReleasedBy(dep.Dep{Name: "g:b", Source: srv.URL + "/dated/"}); !got.IsZero() {
		t.Errorf("ReleasedBy without a lookup = %v", got)
	}
}
