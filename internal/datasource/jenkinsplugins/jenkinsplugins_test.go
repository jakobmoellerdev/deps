package jenkinsplugins

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// site serves the update site files in testdata (updates.jenkins.io's, fetched 2026-11-09 and
// trimmed to three plugins and their newest versions) below prefix, and counts the requests
// per path.
func site(t *testing.T, prefix string) (*httptest.Server, func(string) int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		file, ok := strings.CutPrefix(r.URL.Path, prefix+"/current/")
		if !ok || file != "plugin-versions.json" && file != "update-center.actual.json" {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Error(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, func(p string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[p]
	}
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "DEPS_JENKINS_PLUGINS_URL" {
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
	srv, hits := site(t, "")
	s := New(srv.Client(), make(chan struct{}, 1), env(srv.URL+"/"))
	git := dep.Dep{Datasource: Name, Name: "git"}
	agg := dep.Dep{Datasource: Name, Name: "workflow-aggregator"}
	s.Plan([]dep.Dep{git, agg})
	rs, err := s.Releases(t.Context(), git, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(rs), []string{"5.9.0", "5.10.0", "5.10.1", "5.11.0"}; !slices.Equal(got, want) {
		t.Errorf("git releases = %v, want %v", got, want)
	}
	rs, err = s.Releases(t.Context(), agg, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(rs), []string{"596.v8c21c963d92d", "600.vb_57cdd26fdd7", "608.v67378e9d3db_1"}; !slices.Equal(got, want) {
		t.Errorf("workflow-aggregator releases = %v, want %v", got, want)
	}
	tm, err := s.ReleaseTime(t.Context(), git, "5.10.1")
	if want := time.Date(2026, 3, 25, 20, 14, 0, 0, time.UTC); err != nil || !tm.Equal(want) {
		t.Errorf("release time = %v, %v; want %v", tm, err, want)
	}
	if tm, err := s.ReleaseTime(t.Context(), git, "9.9.9"); err != nil || !tm.IsZero() {
		t.Errorf("unknown release time = %v, %v", tm, err)
	}
	if n := hits(versionsPath); n != 1 {
		t.Errorf("plugin-versions.json requests = %d, want one per run", n)
	}
	if n := hits(infoPath); n != 0 {
		t.Errorf("update-center.actual.json requests = %d before a source repository was asked for", n)
	}

	// Not planned: scanned again for that plugin alone, once.
	cac := dep.Dep{Datasource: Name, Name: "configuration-as-code"}
	for range 2 {
		if rs, err := s.Releases(t.Context(), cac, ""); err != nil || len(rs) != 3 {
			t.Fatalf("late plugin releases = %v, %v", rs, err)
		}
	}
	if n := hits(versionsPath); n != 2 {
		t.Errorf("plugin-versions.json requests = %d, want 2 (one rescan for the late plugin)", n)
	}

	for _, d := range []dep.Dep{git, cac} {
		repo, err := s.SourceRepo(t.Context(), d, "")
		if err != nil || !strings.HasPrefix(repo, "https://github.com/jenkinsci/"+d.Name) {
			t.Errorf("%s source repo = %q, %v", d.Name, repo, err)
		}
	}
	if n := hits(infoPath); n != 1 {
		t.Errorf("update-center.actual.json requests = %d, want one per run", n)
	}
}

func TestUnplannedKeepsAll(t *testing.T) {
	srv, hits := site(t, "/mirror")
	s := New(srv.Client(), nil, env(srv.URL+"/mirror"))
	for _, name := range []string{"git", "workflow-aggregator", "configuration-as-code"} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if n := hits("/mirror" + versionsPath); n != 1 {
		t.Errorf("requests = %d, want one scan keeping every plugin", n)
	}
}

func TestNotFound(t *testing.T) {
	srv, _ := site(t, "/uc")
	s := New(srv.Client(), nil, env(srv.URL+"/uc"))
	s.Plan([]dep.Dep{{Name: "git"}, {Name: "findbugs"}})
	var skip *dep.SkipError
	_, err := s.Releases(t.Context(), dep.Dep{Name: "findbugs"}, "")
	if !errors.As(err, &skip) || skip.Reason != "plugin not found on "+srv.URL+"/uc" {
		t.Errorf("missing plugin: err = %v, want a skip", err)
	}
	if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "findbugs"}, "1"); !errors.As(err, &skip) {
		t.Errorf("missing plugin release time: err = %v", err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "late-and-missing"}, ""); !errors.As(err, &skip) {
		t.Errorf("missing late plugin: err = %v", err)
	}
	if repo, err := s.SourceRepo(t.Context(), dep.Dep{Name: "findbugs"}, "1"); err != nil || repo != "" {
		t.Errorf("missing plugin source repo = %q, %v", repo, err)
	}
}

func TestErrors(t *testing.T) {
	bodies := map[string]string{
		"/array/current/plugin-versions.json":        `[]`,
		"/noplugins/current/plugin-versions.json":    `{"generationTimestamp":"x"}`,
		"/badplugins/current/plugin-versions.json":   `{"plugins":[]}`,
		"/badversions/current/plugin-versions.json":  `{"plugins":{"git":[]}}`,
		"/badrelease/current/plugin-versions.json":   `{"plugins":{"git":{"1.0":[]}}}`,
		"/truncated/current/plugin-versions.json":    `{"plugins":{"git":{"1.0":{}`,
		"/badinfo/current/plugin-versions.json":      `{"plugins":{"git":{"1.0":{}}}}`,
		"/badinfo/current/update-center.actual.json": `{"plugins":{"git":[]}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	for site, want := range map[string]string{
		"/array":       "want an object",
		"/noplugins":   "no plugins object",
		"/badplugins":  "want an object",
		"/badversions": "want an object",
		"/badrelease":  "GET " + srv.URL + "/badrelease/current/plugin-versions.json: ",
		"/truncated":   "unexpected EOF",
		"/missing":     "HTTP 404",
	} {
		s := New(srv.Client(), nil, env(srv.URL+site))
		if _, err := s.Releases(t.Context(), dep.Dep{Name: "git"}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", site, err, want)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "git"}, "1.0"); err == nil { // the failed scan is shared
			t.Errorf("%s: release time without error", site)
		}
	}
	s := New(srv.Client(), nil, env(srv.URL+"/badinfo"))
	d := dep.Dep{Name: "git"}
	if rs, err := s.Releases(t.Context(), d, ""); err != nil || len(rs) != 1 {
		t.Fatalf("releases = %v, %v", rs, err)
	}
	if _, err := s.SourceRepo(t.Context(), d, "1.0"); err == nil {
		t.Error("source repo of a malformed update-center.actual.json without error")
	}
	if _, err := s.Digest(t.Context(), d, "1.0"); err == nil {
		t.Error("digest without error")
	}
}

func TestReleaseTime(t *testing.T) {
	for _, tt := range []struct {
		timestamp, buildDate string
		want                 time.Time
	}{
		{"2011-02-15T02:53:18.00Z", "Feb 15, 2011", time.Date(2011, 2, 15, 2, 53, 18, 0, time.UTC)},
		{"", "Feb 15, 2011", time.Date(2011, 2, 15, 0, 0, 0, 0, time.UTC)},
		{"garbage", "Mar 9, 2011", time.Date(2011, 3, 9, 0, 0, 0, 0, time.UTC)},
		{"", "", time.Time{}},
	} {
		got, ok := releaseTime(tt.timestamp, tt.buildDate)
		if !got.Equal(tt.want) || ok == tt.want.IsZero() {
			t.Errorf("releaseTime(%q, %q) = %v, %v; want %v", tt.timestamp, tt.buildDate, got, ok, tt.want)
		}
	}
}

func TestLateRescanError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"plugins":{"git":{"1.0":{}}}}`))
	}))
	t.Cleanup(srv.Close)
	s := New(srv.Client(), nil, env(srv.URL))
	s.Plan([]dep.Dep{{Name: "git"}})
	if rs, err := s.Releases(t.Context(), dep.Dep{Name: "git"}, ""); err != nil || len(rs) != 1 {
		t.Fatalf("releases = %v, %v", rs, err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "late"}, ""); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("late rescan: err = %v", err)
	}
}

func TestDefaultURL(t *testing.T) {
	if s := New(http.DefaultClient, nil, func(string) string { return "" }); s.base != DefaultURL {
		t.Errorf("base = %q", s.base)
	}
}
