package galaxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// server serves the recorded Galaxy responses (testdata, from galaxy.ansible.com) by request
// URI, counting requests; unknown URIs are 404.
type server struct {
	mu    sync.Mutex
	hits  map[string]int
	files map[string]string // request URI → testdata file or inline JSON
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.hits[r.RequestURI]++
	s.mu.Unlock()
	body, ok := s.files[r.RequestURI]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !strings.HasPrefix(body, "{") {
		b, err := os.ReadFile("testdata/" + body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		body = string(b)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

const (
	plugin = "/api/v3/plugin/ansible/content/published/collections/index/community/general/versions/"
	root   = `{"available_versions":{"v3":"v3/","v1":"v1/"}}`
)

func newServer(t *testing.T) (*server, string) {
	t.Helper()
	s := &server{hits: map[string]int{}, files: map[string]string{
		"/api/": root,
		"/api/v1/roles/?owner__username=geerlingguy&name=java":      "role-geerlingguy-java.json",
		"/api/v3/collections/community/general/":                    "collection-community-general.json",
		"/api/v3/collections/community/general/versions/?limit=100": "versions-community-general-1.json",
		plugin + "?limit=100&offset=100":                            "versions-community-general-2.json",
		plugin + "?limit=100&offset=200":                            "versions-community-general-3.json",
		"/api/v3/collections/community/general/versions/14.0.0/":    "version-community-general-14.0.0.json",
		"/api/v3/collections/old/thing/":                            `{"deprecated":true}`,
		"/api/v3/collections/old/thing/versions/?limit=100":         `{"links":{"next":null},"data":[{"version":"1.0.0","created_at":"2020-01-02T03:04:05Z"},{"version":"not-a-version"}]}`,
		"/api/v3/collections/empty/thing/":                          `{"deprecated":false}`,
		"/api/v3/collections/empty/thing/versions/?limit=100":       `{"links":{"next":null},"data":[]}`,
		"/hub/":                                  root,
		"/hub/v3/collections/community/general/": `{"deprecated":false}`,
		"/hub/v3/collections/community/general/versions/?limit=100":                                         `{"data":[{"version":"9.0.0","created_at":"2024-05-20T00:00:00Z"}]}`,
		"/api/automation-hub/content/published/":                                                            root,
		"/api/automation-hub/content/published/v3/collections/redhat/rhel_system_roles/":                    `{"deprecated":false}`,
		"/api/automation-hub/content/published/v3/collections/redhat/rhel_system_roles/versions/?limit=100": `{"data":[{"version":"1.80.0","created_at":"2024-05-20T00:00:00Z"}]}`,
		"/api/v1/roles/?owner__username=dup&name=role":                                                      `{"results":[{"github_user":"other","github_repo":"x","summary_fields":{"versions":[{"name":"9.9.9"}]}},{"github_user":"dup","github_repo":"ansible-role","summary_fields":{"versions":[{"name":"v1.0.0","created":"2019-01-01T00:00:00Z"},{"name":"master"}]}}]}`,
		"/api/v1/roles/?owner__username=missing&name=role":                                                  `{"count":0,"next":null,"previous":null,"results":[]}`,
		"/api/v1/roles/?owner__username=nover&name=role":                                                    `{"results":[{"github_user":"nover","summary_fields":{"versions":[{"name":"master"}]}}]}`,
	}}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts.URL
}

func env(base string) func(string) string {
	return func(k string) string {
		if k == "DEPS_GALAXY_URL" {
			return base + "/"
		}
		return ""
	}
}

func TestRoles(t *testing.T) {
	s, base := newServer(t)
	ds := New(http.DefaultClient, nil, env(base))
	d := dep.Dep{Name: "geerlingguy.java"}
	rels, err := ds.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	var vs []string
	for _, r := range rels {
		vs = append(vs, r.Version)
	}
	if got := strings.Join(vs, " "); got != "2.2.0 2.3.0 2.3.1 2.3.2 2.3.3 2.4.0 2.5.0 2.5.1 2.5.2 2.6.0 2.6.1" {
		t.Errorf("releases = %s", got)
	}
	tm, err := ds.ReleaseTime(t.Context(), d, "2.6.1")
	if err != nil || !tm.Equal(time.Date(2026, 3, 20, 19, 4, 12, 0, time.UTC)) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if repo, err := ds.SourceRepo(t.Context(), d, "2.6.1"); err != nil || repo != "https://github.com/geerlingguy/ansible-role-java" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if s.hits["/api/v1/roles/?owner__username=geerlingguy&name=java"] != 1 {
		t.Errorf("hits = %v", s.hits)
	}
	if _, err := ds.Digest(t.Context(), d, "2.6.1"); err == nil {
		t.Error("Digest: want an error")
	}

	rels, err = ds.Releases(t.Context(), dep.Dep{Name: "dup.role"}, "")
	if err != nil || len(rels) != 1 || rels[0].Version != "v1.0.0" {
		t.Errorf("dup: %v %v", rels, err)
	}
	if repo, _ := ds.SourceRepo(t.Context(), dep.Dep{Name: "dup.role"}, ""); repo != "https://github.com/dup/ansible-role" {
		t.Errorf("dup repo %q", repo)
	}
	if tm, _ := ds.ReleaseTime(t.Context(), dep.Dep{Name: "dup.role"}, "v1.0.0"); tm.Year() != 2019 {
		t.Errorf("created fallback: %v", tm)
	}
	var skip *dep.SkipError
	if _, err := ds.Releases(t.Context(), dep.Dep{Name: "missing.role"}, ""); !errors.As(err, &skip) {
		t.Errorf("missing: %v", err)
	}
	if _, err := ds.Releases(t.Context(), dep.Dep{Name: "nover.role"}, ""); err == nil || !strings.Contains(err.Error(), "no version tags") {
		t.Errorf("no tags: %v", err)
	}
	if _, err := ds.Releases(t.Context(), dep.Dep{Name: "noname"}, ""); err == nil {
		t.Error("invalid name: want an error")
	}
	if _, err := ds.ReleaseTime(t.Context(), dep.Dep{Name: "noname"}, ""); err == nil {
		t.Error("invalid name: want an error")
	}
	if _, err := ds.SourceRepo(t.Context(), dep.Dep{Name: "noname"}, ""); err == nil {
		t.Error("invalid name: want an error")
	}
}

func TestCollections(t *testing.T) {
	s, base := newServer(t)
	ds := NewCollections(http.DefaultClient, nil, env(base))
	d := dep.Dep{Name: "community.general"}
	rels, err := ds.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	// 247 versions, of which 3 are semver prereleases that are no PEP 440 versions
	// (0.3.0-experimental.meta.redirects-2)
	if len(rels) != 244 || rels[len(rels)-1].Version != "14.0.0" || rels[0].Version != "0.1.1" {
		t.Errorf("%d releases, first %v last %v", len(rels), rels[0], rels[len(rels)-1])
	}
	tm, err := ds.ReleaseTime(t.Context(), d, "14.0.0")
	if err != nil || !tm.Equal(time.Date(2026, 11, 2, 20, 34, 37, 847000000, time.UTC)) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if repo, err := ds.SourceRepo(t.Context(), d, "14.0.0"); err != nil || repo != "https://github.com/ansible-collections/community.general" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if got := ds.Deprecation(d, "14.0.0"); got != "" {
		t.Errorf("Deprecation = %q", got)
	}
	for _, u := range []string{"/api/", "/api/v3/collections/community/general/", plugin + "?limit=100&offset=200"} {
		if s.hits[u] != 1 {
			t.Errorf("hits[%s] = %d", u, s.hits[u])
		}
	}
	if _, err := ds.Digest(t.Context(), d, "14.0.0"); err == nil {
		t.Error("Digest: want an error")
	}

	old := dep.Dep{Name: "old.thing"}
	if rels, err := ds.Releases(t.Context(), old, ""); err != nil || len(rels) != 1 {
		t.Errorf("old: %v %v", rels, err)
	}
	if got := ds.Deprecation(old, "1.0.0"); got != "deprecated" {
		t.Errorf("Deprecation = %q", got)
	}
	var skip *dep.SkipError
	if _, err := ds.Releases(t.Context(), dep.Dep{Name: "missing.thing"}, ""); !errors.As(err, &skip) {
		t.Errorf("missing: %v", err)
	}
	if _, err := ds.Releases(t.Context(), dep.Dep{Name: "empty.thing"}, ""); err == nil {
		t.Error("empty: want an error")
	}
	if _, err := ds.Releases(t.Context(), dep.Dep{Name: "a/b"}, ""); err == nil {
		t.Error("invalid name: want an error")
	}
	if _, err := ds.ReleaseTime(t.Context(), dep.Dep{Name: "a/b"}, ""); err == nil {
		t.Error("invalid name: want an error")
	}
	if _, err := ds.SourceRepo(t.Context(), dep.Dep{Name: "a/b"}, ""); err == nil {
		t.Error("invalid name: want an error")
	}

	// A server whose API root is the server URL itself (`<server>/api/` is 404), and one
	// that names the API path (Automation Hub).
	for _, tc := range []struct{ source, name, want string }{
		{base + "/hub", "community.general", "9.0.0"},
		{base + "/api/automation-hub/content/published/", "redhat.rhel_system_roles", "1.80.0"},
	} {
		rels, err := ds.Releases(t.Context(), dep.Dep{Name: tc.name, Source: tc.source}, "")
		if err != nil || len(rels) != 1 || rels[0].Version != tc.want {
			t.Errorf("%s: %v %v", tc.source, rels, err)
		}
	}
	if s.hits["/hub/api/"] != 1 || s.hits["/hub/"] != 1 {
		t.Errorf("discovery hits = %v", s.hits)
	}
	if _, err := ds.Releases(t.Context(), dep.Dep{Name: "a.b", Source: base + "/nothing"}, ""); err == nil || !strings.Contains(err.Error(), "galaxy server") {
		t.Errorf("no API: %v", err)
	}

	// A links.next pointing back at an already-fetched page fails instead of looping.
	loop := "/api/v3/collections/loop/thing/versions/?limit=100"
	s.files["/api/v3/collections/loop/thing/"] = `{"deprecated":false}`
	s.files[loop] = `{"links":{"next":"` + loop + `"},"data":[{"version":"1.0.0"}]}`
	if _, err := ds.Releases(t.Context(), dep.Dep{Name: "loop.thing"}, ""); err == nil || !strings.Contains(err.Error(), "pagination loop") {
		t.Errorf("loop: %v", err)
	}
	if s.hits[loop] != 1 {
		t.Errorf("loop hits = %d", s.hits[loop])
	}
}

func TestDefaults(t *testing.T) {
	ds := NewCollections(http.DefaultClient, nil, func(string) string { return "" })
	if got := ds.server(dep.Dep{}); got != DefaultURL {
		t.Errorf("server = %q", got)
	}
	if got := New(http.DefaultClient, nil, func(string) string { return "" }).base; got != DefaultURL {
		t.Errorf("base = %q", got)
	}
	if !parseTime("bad").IsZero() {
		t.Error("parseTime")
	}
	if _, err := resolve("://", "x"); err == nil {
		t.Error("resolve: want an error")
	}
}
