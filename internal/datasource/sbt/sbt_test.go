package sbtds

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
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// server serves path -> body (404 otherwise) and records the requested paths.
type server struct {
	*httptest.Server
	mu   sync.Mutex
	hits []string
}

func newServer(t *testing.T, files map[string]string) *server {
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits = append(s.hits, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		body, ok := files[r.URL.Path]
		if strings.HasPrefix(r.URL.Path, "/broken/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Last-Modified", "Tue, 15 Apr 2025 10:00:00 GMT")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func newSources(t *testing.T, s *server) (Package, Plugin) {
	maven := mavends.New(s.Client(), nil, func(string) string { return "" })
	return New(maven, s.Client(), nil)
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestPackageReleases(t *testing.T) {
	s := newServer(t, map[string]string{
		"/m2/org/typelevel/cats-core_2.13/maven-metadata.xml":               fixture(t, "cats-core_2.13-maven-metadata.xml"),
		"/m2/org/typelevel/cats-core_2.13/2.13.0/cats-core_2.13-2.13.0.pom": "<project/>",
		"/ivy/org.typelevel/cats-core_2.13/":                                `<a href="../">../</a><a href="9.9.9/">9.9.9/</a><a href="2.13.0/">2.13.0/</a>`,
	})
	pkg, _ := newSources(t, s)
	d := dep.Dep{Manager: "sbt", Datasource: PackageName, Name: "org.typelevel:cats-core_2.13", Source: s.URL + "/m2/ ivy=" + s.URL + "/ivy"}
	rels, err := pkg.Releases(context.Background(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	vs := versions(rels)
	if !slices.Contains(vs, "2.13.0") || !slices.Contains(vs, "2.0.0") || vs[len(vs)-1] != "9.9.9" {
		t.Errorf("versions = %v", vs)
	}
	if strings.Count(strings.Join(vs, " "), "2.13.0") != 1 {
		t.Errorf("versions not merged: %v", vs)
	}
	at, err := pkg.ReleaseTime(context.Background(), d, "2.13.0")
	if err != nil || !at.Equal(time.Date(2025, 4, 15, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("ReleaseTime = %v, %v", at, err)
	}
	if _, err := pkg.Digest(context.Background(), d, "2.13.0"); err == nil || !strings.Contains(err.Error(), PackageName) {
		t.Errorf("Digest err = %v", err)
	}
	// Only Ivy repositories: no Maven lookup (which would default to Maven Central).
	ivyOnly := d
	ivyOnly.Source = "ivy=" + s.URL + "/ivy/"
	rels, err = pkg.Releases(context.Background(), ivyOnly, "")
	if err != nil || !slices.Equal(versions(rels), []string{"9.9.9", "2.13.0"}) {
		t.Errorf("ivy only = %v, %v", versions(rels), err)
	}
	if at, err := pkg.ReleaseTime(context.Background(), ivyOnly, "9.9.9"); err != nil || !at.IsZero() {
		t.Errorf("ivy ReleaseTime = %v, %v", at, err)
	}
	if r, err := pkg.SourceRepo(context.Background(), ivyOnly, "9.9.9"); err != nil || r != "" {
		t.Errorf("ivy SourceRepo = %q, %v", r, err)
	}
}

func TestPackageNotFoundAndErrors(t *testing.T) {
	s := newServer(t, map[string]string{"/m2/g/b/maven-metadata.xml": `<metadata><versioning><versions><version>1.0</version></versions></versioning></metadata>`})
	pkg, _ := newSources(t, s)
	ctx := context.Background()
	_, err := pkg.Releases(ctx, dep.Dep{Name: "g:a_2.13", Source: s.URL + "/m2/"}, "")
	if err == nil || !strings.Contains(err.Error(), "g:a_2.13 not found") {
		t.Errorf("err = %v", err)
	}
	// A failing repository fails the lookup only when no other one has the artifact.
	_, err = pkg.Releases(ctx, dep.Dep{Name: "g:a", Source: s.URL + "/m2/ ivy=" + s.URL + "/broken"}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %v", err)
	}
	rels, err := pkg.Releases(ctx, dep.Dep{Name: "g:b", Source: s.URL + "/m2/ ivy=" + s.URL + "/broken"}, "")
	if err != nil || !slices.Equal(versions(rels), []string{"1.0"}) {
		t.Errorf("g:b = %v, %v", versions(rels), err)
	}
	if _, err := pkg.Releases(ctx, dep.Dep{Name: "nocolon"}, ""); err == nil {
		t.Error("want error for a name without group")
	}
}

func TestPackagePlatformFallback(t *testing.T) {
	s := newServer(t, map[string]string{
		"/m2/org/scala-js/scalajs-java-securerandom_sjs1_2.13/maven-metadata.xml":                                  `<metadata><versioning><versions><version>1.0.0</version></versions></versioning></metadata>`,
		"/m2/org/scala-js/scalajs-java-securerandom_sjs1_2.13/1.0.0/scalajs-java-securerandom_sjs1_2.13-1.0.0.pom": "<project/>",
	})
	pkg, _ := newSources(t, s)
	ctx := context.Background()
	d := dep.Dep{Name: "org.scala-js:scalajs-java-securerandom_2.13", Source: s.URL + "/m2/"}
	rels, err := pkg.Releases(ctx, d, "")
	if err != nil || !slices.Equal(versions(rels), []string{"1.0.0"}) {
		t.Fatalf("releases = %v, %v", versions(rels), err)
	}
	if at, err := pkg.ReleaseTime(ctx, d, "1.0.0"); err != nil || at.IsZero() {
		t.Errorf("ReleaseTime = %v, %v", at, err)
	}
	if _, err := pkg.Releases(ctx, dep.Dep{Name: "g:none_3", Source: s.URL + "/m2/"}, ""); err == nil || !strings.Contains(err.Error(), "g:none_3 not found") {
		t.Errorf("err = %v", err)
	}
	if got := platformNames("g:a_2.12"); !slices.Equal(got, []string{"g:a_sjs1_2.12", "g:a_native0.5_2.12", "g:a_native0.4_2.12"}) {
		t.Errorf("platformNames = %v", got)
	}
	if platformNames("g:a") != nil || platformNames("g:a_1.0") != nil || platformNames("a_2.13") != nil {
		t.Error("platformNames of a non-Scala artifact")
	}
}

func TestListingFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer s.Close()
	maven := mavends.New(s.Client(), nil, func(string) string { return "" })
	pkg, _ := New(maven, s.Client(), nil)
	_, err := pkg.Releases(context.Background(), dep.Dep{Name: "g:a", Source: "ivy=" + s.URL}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("err = %v", err)
	}
}

func TestPluginReleases(t *testing.T) {
	s := newServer(t, map[string]string{
		"/m2/org/scalameta/sbt-scalafmt_2.12_1.0/maven-metadata.xml":                    fixture(t, "sbt-scalafmt_2.12_1.0-maven-metadata.xml"),
		"/m2/org/scalameta/sbt-scalafmt_2.12_1.0/2.6.2/sbt-scalafmt_2.12_1.0-2.6.2.pom": "<project/>",
		"/m2/com/github/sbt/sbt-ci-release_sbt2_3/maven-metadata.xml":                   `<metadata><versioning><versions><version>1.12.1</version></versions></versioning></metadata>`,
		"/ivy/com.typesafe.sbt/sbt-git/scala_2.12/sbt_1.0/":                             fixture(t, "sbt-git-ivy-listing.html"),
		"/ivy/com.typesafe.sbt/sbt-git/scala_2.10/sbt_0.13/":                            `<a href="0.8.5/">0.8.5/</a>`,
	})
	_, plugin := newSources(t, s)
	ctx := context.Background()
	src := "ivy=" + s.URL + "/ivy/ " + s.URL + "/m2/"
	cases := []struct {
		name, sbt string
		want      []string
	}{
		{"org.scalameta:sbt-scalafmt", "1.10.2", nil},
		{"com.typesafe.sbt:sbt-git", "", []string{"0.9.3", "1.0.0"}},
		{"com.typesafe.sbt:sbt-git", "0.13.18", []string{"0.8.5"}},
		{"com.github.sbt:sbt-ci-release", "2.0.0", []string{"1.12.1"}},
	}
	for _, c := range cases {
		rels, err := plugin.Releases(ctx, dep.Dep{Manager: "sbt", Name: c.name, Source: src, SbtVersion: c.sbt}, "")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		vs := versions(rels)
		if c.want == nil {
			if !slices.Contains(vs, "2.6.2") || !slices.Contains(vs, "2.5.0") {
				t.Errorf("%s = %v", c.name, vs)
			}
			continue
		}
		if !slices.Equal(vs, c.want) {
			t.Errorf("%s (sbt %q) = %v, want %v", c.name, c.sbt, vs, c.want)
		}
	}
	d := dep.Dep{Manager: "sbt", Name: "org.scalameta:sbt-scalafmt", Source: src}
	if at, err := plugin.ReleaseTime(ctx, d, "2.6.2"); err != nil || at.IsZero() {
		t.Errorf("ReleaseTime = %v, %v", at, err)
	}
	if at, err := plugin.ReleaseTime(ctx, dep.Dep{Name: "g:a", Source: "ivy=" + s.URL + "/ivy/"}, "1"); err != nil || !at.IsZero() {
		t.Errorf("ivy-only ReleaseTime = %v, %v", at, err)
	}
	if r, err := plugin.SourceRepo(ctx, dep.Dep{Name: "g:a", Source: "ivy=" + s.URL + "/ivy/"}, "1"); err != nil || r != "" {
		t.Errorf("ivy-only SourceRepo = %q, %v", r, err)
	}
	if _, err := plugin.Releases(ctx, dep.Dep{Name: "x"}, ""); err == nil {
		t.Error("want error for a name without group")
	}
	if _, err := plugin.Digest(ctx, d, "2.6.2"); err == nil || !strings.Contains(err.Error(), PluginName) {
		t.Errorf("Digest err = %v", err)
	}
}

func TestSubdirs(t *testing.T) {
	got := subdirs(fixture(t, "sbt-scalafmt-central-listing.html"))
	if len(got) < 30 || !slices.Contains(got, "2.6.2") || !slices.Contains(got, "2.0.0-RC5") || slices.Contains(got, "..") {
		t.Errorf("subdirs = %v", got)
	}
	if got := subdirs(fixture(t, "sbt-git-ivy-listing.html")); !slices.Equal(got, []string{"0.9.3", "1.0.0"}) {
		t.Errorf("ivy subdirs = %v", got)
	}
	if got := subdirs(`<a href="https://x/y/">x</a><a href=".hidden/">h</a><a href="ok/">`); !slices.Equal(got, []string{"ok"}) {
		t.Errorf("subdirs = %v", got)
	}
}
