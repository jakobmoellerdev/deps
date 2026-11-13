package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// noHelper is a credential helper that has no credentials.
func noHelper(context.Context, string) (string, string, bool) { return "", "", false }

func newAuth(opts ...httpx.AuthOption) *httpx.Auth {
	return httpx.NewAuth(platform.Info{}, "", func(string) string { return "" }, append([]httpx.AuthOption{httpx.WithHelper(noHelper)}, opts...)...)
}

// serve answers path (without query) with the fixture file, recording the request.
func serve(t *testing.T, routes map[string]string, seen func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen(r)
		}
		file, ok := routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = strings.TrimSuffix(r.Version+"@"+r.Digest, "@")
	}
	return out
}

// TestRealResponses decodes responses captured from gitlab.com (gitlab-org/cli), gitea.com
// (gitea/tea, archived on 2026-10-29) and code.forgejo.org (forgejo/runner). The repository
// documents (gitea-repo.json, forgejo-repo.json: `GET /api/v1/repos/<owner>/<repo>` trimmed
// with jq; gitlab-project.json: GraphQL `project(fullPath){archived}`) are asked once,
// alongside the first page, and answer Deprecation.
func TestRealResponses(t *testing.T) {
	for _, tc := range []struct {
		ds, name, path, file string
		want                 []string
		time                 string // of want[0]
		repoPath, repoFile   string
		archived             bool
	}{
		{GitLabReleases, "gitlab-org/cli", "/api/v4/projects/gitlab-org%2Fcli/releases", "gitlab-releases.json",
			[]string{"v1.128.0", "v1.127.0", "v1.126.0"}, "2026-11-03T12:18:50.742Z", "/api/graphql", "gitlab-project.json", false},
		{GitLabTags, "gitlab-org/cli", "/api/v4/projects/gitlab-org%2Fcli/repository/tags", "gitlab-tags.json",
			[]string{"v1.128.0@519ea19f7d3e9c701a4063f277ae27254bbafdad", "v1.127.0@2e352a17e7e00bef420b0f38e0ce9ab4e1a38e5d", "v1.126.0@4dd0d32d18361db6c1c668eb4ac691c0d6edf255"}, "2026-11-03T11:07:41Z",
			"/api/graphql", "gitlab-project.json", false},
		{GiteaReleases, "gitea/tea", "/api/v1/repos/gitea/tea/releases", "gitea-releases.json",
			[]string{"v0.16.0", "v0.15.1", "v0.15.0"}, "2026-09-10T11:34:12Z", "/api/v1/repos/gitea/tea", "gitea-repo.json", true},
		{GiteaTags, "gitea/tea", "/api/v1/repos/gitea/tea/tags", "gitea-tags.json",
			[]string{"v0.16.0@9c12138d6273ba8feb6e7578833cc330c0cd6461", "v0.15.1@f34697c5ed65928e265d6f48e16928819ce0f332", "v0.15.0@61b8536e4a3e04d9db74b05b4d9cd489d07d8d50"}, "2026-09-10T04:29:39Z",
			"/api/v1/repos/gitea/tea", "gitea-repo.json", true},
		{ForgejoReleases, "forgejo/runner", "/api/v1/repos/forgejo/runner/releases", "forgejo-releases.json",
			[]string{"v13.2.1", "v13.2.0", "v13.1.0"}, "2026-10-29T21:36:30Z", "/api/v1/repos/forgejo/runner", "forgejo-repo.json", false},
		{ForgejoTags, "forgejo/runner", "/api/v1/repos/forgejo/runner/tags", "forgejo-tags.json",
			[]string{"v13.2.1@c4a92ffa5e3a400ae7e9c40a2914e649eb338ad7", "v13.2.0@df6b843fb929bb04b933b09c6bf208774d42480b", "v13.1.0@6095cb17bfdbded5aa4ea84c18a4b69fd9574cca"}, "2026-10-02T02:59:29Z",
			"/api/v1/repos/forgejo/runner", "forgejo-repo.json", false},
	} {
		t.Run(tc.ds, func(t *testing.T) {
			var mu sync.Mutex
			var queries, repoQueries []string
			srv := serve(t, map[string]string{tc.path: tc.file, tc.repoPath: tc.repoFile}, func(r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == tc.repoPath {
					repoQueries = append(repoQueries, r.URL.RawQuery)
				} else {
					queries = append(queries, r.URL.RawQuery)
				}
			})
			s := New(tc.ds, srv.Client(), nil, newAuth())
			d := dep.Dep{Datasource: tc.ds, Name: tc.name, Source: srv.URL + "/"}
			if why := s.Deprecation(d, ""); why != "" {
				t.Errorf("Deprecation before Releases = %q, want none (no request)", why)
			}
			rels, err := s.Releases(t.Context(), d, "")
			if err != nil {
				t.Fatal(err)
			}
			if got := versions(rels); !slices.Equal(got, tc.want) {
				t.Errorf("releases = %v, want %v", got, tc.want)
			}
			at, err := s.ReleaseTime(t.Context(), d, strings.Split(tc.want[0], "@")[0])
			if err != nil {
				t.Fatal(err)
			}
			if want, _ := time.Parse(time.RFC3339, tc.time); !at.Equal(want) {
				t.Errorf("release time = %v, want %v", at, want)
			}
			if at, _ := s.ReleaseTime(t.Context(), d, "v0.0.1"); !at.IsZero() {
				t.Errorf("unlisted release time = %v, want zero", at)
			}
			want := ""
			if tc.archived {
				want = "repository archived"
			}
			if why := s.Deprecation(d, tc.want[0]); why != want {
				t.Errorf("Deprecation = %q, want %q", why, want)
			}
			if archived, err := s.Archived(t.Context(), d); err != nil || archived != tc.archived {
				t.Errorf("Archived = %t, %v; want %t", archived, err, tc.archived)
			}
			if len(queries) != 1 || !strings.Contains(queries[0], "page=1") {
				t.Errorf("queries = %v, want one first page (lists are fetched once)", queries)
			}
			if len(repoQueries) != 1 {
				t.Fatalf("repository queries = %v, want one", repoQueries)
			}
			if q, _ := url.ParseQuery(repoQueries[0]); tc.repoPath == "/api/graphql" &&
				(q.Get("query") != gitlabArchivedQuery || q.Get("variables") != `{"p":"gitlab-org/cli"}`) {
				t.Errorf("GraphQL query = %v", q)
			}
		})
	}
}

// TestArchived reads the archived flag of gitlab-org/golang-crypto (GraphQL, captured from
// gitlab.com); a project GitLab does not show (`project: null`), a host without the API and a
// failing request are not archived.
func TestArchived(t *testing.T) {
	srv := serve(t, map[string]string{"/api/graphql": "gitlab-project-archived.json"}, nil)
	s := New(GitLabTags, srv.Client(), nil, newAuth())
	d := dep.Dep{Name: "gitlab-org/golang-crypto", Source: srv.URL}
	if archived, err := s.Archived(t.Context(), d); err != nil || !archived {
		t.Errorf("Archived = %t, %v", archived, err)
	}
	if why := s.Deprecation(d, "v1"); why != "repository archived" {
		t.Errorf("Deprecation = %q", why)
	}
	if _, err := s.Archived(t.Context(), dep.Dep{Name: "a/b/c", Source: srv.URL}); err != nil {
		t.Errorf("GitLab subgroup path: %v", err)
	}

	null := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":{"project":null}}`)
	}))
	defer null.Close()
	s = New(GitLabReleases, null.Client(), nil, newAuth())
	if archived, err := s.Archived(t.Context(), dep.Dep{Name: "g/private", Source: null.URL}); err != nil || archived {
		t.Errorf("Archived(project null) = %t, %v", archived, err)
	}

	s = New(GiteaTags, srv.Client(), nil, newAuth())
	if archived, err := s.Archived(t.Context(), dep.Dep{Name: "o/r", Source: srv.URL}); err == nil || archived {
		t.Errorf("Archived(404) = %t, %v; want the error", archived, err)
	}
	if _, err := s.Archived(t.Context(), dep.Dep{Name: "a/b/c", Source: srv.URL}); err == nil {
		t.Error("Archived(invalid name): no error")
	}
	if why := s.Deprecation(dep.Dep{Name: "a/b/c", Source: srv.URL}, ""); why != "" {
		t.Errorf("Deprecation(invalid name) = %q", why)
	}
}

// TestFiltersUnpublished drops drafts (Gitea/Forgejo) and upcoming releases (GitLab), and
// keeps published prereleases: a dep pinned at one has no candidate without them.
func TestFiltersUnpublished(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v4/") {
			fmt.Fprint(w, `[{"tag_name":"v3","upcoming_release":true},{"tag_name":"v2","released_at":null},{"tag_name":""},{"tag_name":"v2"}]`)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"v4","draft":true},{"tag_name":"v3-rc1","prerelease":true,"published_at":"2026-01-02T03:04:05Z"},{"tag_name":"v2"}]`)
	}))
	defer srv.Close()
	for _, tc := range []struct {
		ds   string
		want []string
	}{{GitLabReleases, []string{"v2"}}, {GiteaReleases, []string{"v3-rc1", "v2"}}, {ForgejoReleases, []string{"v3-rc1", "v2"}}} {
		s := New(tc.ds, srv.Client(), nil, newAuth())
		d := dep.Dep{Name: "o/r", Source: srv.URL}
		rels, err := s.Releases(t.Context(), d, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := versions(rels); !slices.Equal(got, tc.want) {
			t.Errorf("%s releases = %v, want %v", tc.ds, got, tc.want)
		}
		if tc.ds == GitLabReleases {
			continue
		}
		at, err := s.ReleaseTime(t.Context(), d, "v3-rc1")
		if err != nil || !at.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
			t.Errorf("%s ReleaseTime(v3-rc1) = %v, %v", tc.ds, at, err)
		}
	}
}

// TestPagination follows rel="next" until a page without it, at most maxPages.
func TestPagination(t *testing.T) {
	for _, tc := range []struct {
		last, want int
	}{{3, 3}, {50, maxPages}} {
		var pages atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/releases") {
				fmt.Fprint(w, `{"archived":false}`) // the repository
				return
			}
			n := int(pages.Add(1))
			if got := r.URL.Query().Get("page"); got != fmt.Sprint(n) {
				t.Errorf("page = %s, want %d", got, n)
			}
			if r.URL.Query().Get("limit") != "50" || r.URL.Query().Get("draft") != "false" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			if n < tc.last {
				w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, n+1))
			}
			fmt.Fprintf(w, `[{"tag_name":"v1.%d.0"}]`, 100-n)
		}))
		s := New(ForgejoReleases, srv.Client(), nil, newAuth())
		rels, err := s.Releases(t.Context(), dep.Dep{Name: "o/r", Source: srv.URL}, "")
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(rels) != tc.want || int(pages.Load()) != tc.want {
			t.Errorf("last page %d: %d releases in %d pages, want %d", tc.last, len(rels), pages.Load(), tc.want)
		}
	}
}

// TestAuth sends the host's token as Bearer (GitLab), JOB-TOKEN (GitLab CI job token) or
// Basic (Gitea/Forgejo); without one a 404 asks the credential helper once and retries.
func TestAuth(t *testing.T) {
	for _, tc := range []struct {
		name, ds, user string
		helper         bool
		check          func(*http.Request) bool
	}{
		{"gitlab bearer", GitLabTags, "oauth2", false, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok" }},
		{"gitlab job token", GitLabReleases, "gitlab-ci-token", false, func(r *http.Request) bool {
			return r.Header.Get("JOB-TOKEN") == "tok" && r.Header.Get("Authorization") == ""
		}},
		{"gitea basic", GiteaReleases, "x-access-token", false, func(r *http.Request) bool {
			u, p, ok := r.BasicAuth()
			return ok && u == "x-access-token" && p == "tok"
		}},
		{"helper after 404", ForgejoTags, "me", true, func(r *http.Request) bool {
			u, p, ok := r.BasicAuth()
			return ok && u == "me" && p == "tok"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, helped atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !tc.check(r) {
					http.NotFound(w, r)
					return
				}
				if r.URL.Path == "/api/graphql" || r.URL.Path == "/api/v1/repos/o/r" {
					fmt.Fprint(w, `{"archived":false,"data":{"project":{"archived":false}}}`) // the repository
					return
				}
				fmt.Fprint(w, `[{"tag_name":"v1","name":"v1","commit":{"id":"a","sha":"a"}}]`)
			}))
			defer srv.Close()
			opt := httpx.WithToken("127.0.0.1", tc.user, "tok", "test")
			if tc.helper {
				opt = httpx.WithHelper(func(context.Context, string) (string, string, bool) {
					helped.Add(1)
					return "me", "tok", true
				})
			}
			s := New(tc.ds, srv.Client(), nil, newAuth(opt))
			rels, err := s.Releases(t.Context(), dep.Dep{Name: "o/r", Source: srv.URL}, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(rels) != 1 {
				t.Errorf("releases = %v", rels)
			}
			want := int32(2) // the list and the repository
			if tc.helper {
				want = 4
			}
			if requests.Load() != want || helped.Load() != want/2-1 {
				t.Errorf("requests = %d, helper calls = %d", requests.Load(), helped.Load())
			}
		})
	}
}

// A Forgejo Authorized Integration hands an Actions job a JWT instead of an access token, and
// Forgejo's Basic auth answers 401 for it. It has to go out as a bearer token.
func TestAuthJWT(t *testing.T) {
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJyZXBvOm8vciJ9.c2ln"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok || r.Header.Get("Authorization") != "Bearer "+jwt {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/v1/repos/o/r" {
			fmt.Fprint(w, `{"archived":false}`)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"v1","name":"v1","commit":{"id":"a","sha":"a"}}]`)
	}))
	defer srv.Close()
	s := New(ForgejoReleases, srv.Client(), nil, newAuth(httpx.WithToken("127.0.0.1", "x-access-token", jwt, "test")))
	rels, err := s.Releases(t.Context(), dep.Dep{Name: "o/r", Source: srv.URL}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Errorf("releases = %v", rels)
	}
}

func TestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	s := New(GitLabReleases, srv.Client(), nil, newAuth())
	_, err := s.Releases(t.Context(), dep.Dep{Name: "g/missing", Source: srv.URL}, "")
	if err == nil || !strings.Contains(err.Error(), "gitlab-releases: repository "+srv.URL+"/g/missing not found") {
		t.Errorf("err = %v", err)
	}
	s = New(GiteaTags, srv.Client(), nil, newAuth())
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "a/b/c", Source: srv.URL}, ""); err == nil || err.Error() != `invalid gitea-tags name "a/b/c"` {
		t.Errorf("err = %v", err)
	}
}

func TestServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	defer srv.Close()
	s := New(GiteaReleases, srv.Client(), nil, newAuth())
	_, err := s.Releases(t.Context(), dep.Dep{Name: "o/r", Source: srv.URL}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 418") {
		t.Errorf("err = %v", err)
	}
	if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "o/r", Source: srv.URL}, "v1"); err == nil {
		t.Error("ReleaseTime: no error")
	}
}

// TestDigest returns the commit of a listed tag, looks an unlisted one up by name, and reports
// a missing one as datasource.ErrVersionNotFound; release lists have no digest.
func TestDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/g%2Fs%2Fp/repository/tags":
			fmt.Fprint(w, `[{"name":"v2","commit":{"id":"bbb"}}]`)
		case "/api/v4/projects/g%2Fs%2Fp/repository/tags/release%2Fv1":
			fmt.Fprint(w, `{"name":"release/v1","commit":{"id":"aaa"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := New(GitLabTags, srv.Client(), nil, newAuth())
	d := dep.Dep{Name: srv.URL + "/g/s/p.git"}
	for v, want := range map[string]string{"v2": "bbb", "release/v1": "aaa"} {
		if got, err := s.Digest(t.Context(), d, v); err != nil || got != want {
			t.Errorf("Digest(%s) = %q, %v; want %q", v, got, err, want)
		}
	}
	if _, err := s.Digest(t.Context(), d, "v0"); !errors.Is(err, datasource.ErrVersionNotFound) {
		t.Errorf("Digest(v0) err = %v, want ErrVersionNotFound", err)
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "x"}, "v0"); err == nil {
		t.Error("Digest of an invalid name: no error")
	}
	if got, err := New(GitLabReleases, srv.Client(), nil, newAuth()).Digest(t.Context(), d, "v2"); got != "" || err != nil {
		t.Errorf("release Digest = %q, %v", got, err)
	}
}

func TestParseRepo(t *testing.T) {
	for _, tc := range []struct {
		ds, name, registry, want, err string
	}{
		{GitLabReleases, "gitlab-org/cli", "", "https://gitlab.com/gitlab-org/cli", ""},
		{GitLabTags, "a/b/c", "https://git.example.com/gitlab/ https://mirror", "https://git.example.com/gitlab/a/b/c", ""},
		{GiteaReleases, "gitea/tea", "", "https://gitea.com/gitea/tea", ""},
		{ForgejoTags, "forgejo/runner", "", "https://code.forgejo.org/forgejo/runner", ""},
		{ForgejoReleases, "https://codeberg.org/forgejo/forgejo.git", "https://ignored", "https://codeberg.org/forgejo/forgejo", ""},
		{GitLabReleases, "https://gitlab.com/group/proj/-/releases", "", "https://gitlab.com/group/proj", ""},
		{GitLabTags, "https://gitlab.com/group/proj/-/tree/main", "", "https://gitlab.com/group/proj", ""},
		{GitLabTags, "https://git.example.com/a/b/c/-/tags/v1.0.0", "", "https://git.example.com/a/b/c", ""},
		{GitLabTags, "https://gitlab.com/solo/-/releases", "", "", `invalid gitlab-tags name "https://gitlab.com/solo/-/releases"`},
		{GiteaTags, "a/b/c", "", "", `invalid gitea-tags name "a/b/c"`},
		{GitLabTags, "solo", "", "", `invalid gitlab-tags name "solo"`},
		{GitLabTags, "a//b", "", "", `invalid gitlab-tags name "a//b"`},
		{GitLabTags, "a/../b", "", "", `invalid gitlab-tags name "a/../b"`},
		{GitLabTags, "ftp://host/a/b", "", "", `invalid gitlab-tags name "ftp://host/a/b"`},
		{GiteaReleases, "o/r", "oci://x", "", `invalid gitea-releases registry URL "oci://x"`},
		{"github-releases", "o/r", "", "", `unknown forge datasource "github-releases"`},
	} {
		r, err := ParseRepo(tc.ds, tc.name, tc.registry)
		if tc.err != "" {
			if err == nil || err.Error() != tc.err {
				t.Errorf("ParseRepo(%s, %s) err = %v, want %s", tc.ds, tc.name, err, tc.err)
			}
			continue
		}
		if err != nil || r.URL() != tc.want {
			t.Errorf("ParseRepo(%s, %s) = %s, %v; want %s", tc.ds, tc.name, r.URL(), err, tc.want)
		}
	}
}

func TestNewUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	New("github-releases", nil, nil, nil)
}

// TestCommitTime reads the committer date from responses captured from gitlab.com
// (gitlab-org/cli) and gitea.com (gitea/tea); a missing commit is zero.
func TestCommitTime(t *testing.T) {
	const (
		glSHA = "519ea19f7d3e9c701a4063f277ae27254bbafdad"
		gtSHA = "9c12138d6273ba8feb6e7578833cc330c0cd6461"
	)
	var queries []string
	srv := serve(t, map[string]string{
		"/api/v4/projects/gitlab-org%2Fcli/repository/commits/" + glSHA: "gitlab-commit.json",
		"/api/v1/repos/gitea/tea/git/commits/" + gtSHA:                  "gitea-commit.json",
	}, func(r *http.Request) { queries = append(queries, r.URL.RawQuery) })
	for _, tc := range []struct {
		ds, name, commit string
		want             time.Time
	}{
		{GitLabTags, "gitlab-org/cli", glSHA, time.Date(2026, 11, 3, 11, 7, 41, 0, time.UTC)},
		{GiteaTags, "gitea/tea", gtSHA, time.Date(2026, 9, 10, 4, 29, 39, 0, time.UTC)},
		{GitLabTags, "gitlab-org/cli", gtSHA, time.Time{}},
	} {
		got, err := New(tc.ds, srv.Client(), nil, newAuth()).CommitTime(t.Context(), dep.Dep{Name: tc.name, Source: srv.URL}, tc.commit)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%s %s@%s = %v, %v; want %v", tc.ds, tc.name, tc.commit, got, err, tc.want)
		}
	}
	if queries[1] != "stat=false&verification=false&files=false" {
		t.Errorf("gitea query = %q", queries[1])
	}
	if _, err := New(GitLabTags, srv.Client(), nil, newAuth()).CommitTime(t.Context(), dep.Dep{Name: "x"}, glSHA); err == nil {
		t.Error("invalid name: no error")
	}
	srvErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	t.Cleanup(srvErr.Close)
	if _, err := New(GiteaTags, srvErr.Client(), nil, newAuth()).CommitTime(t.Context(), dep.Dep{Name: "o/r", Source: srvErr.URL}, glSHA); err == nil {
		t.Error("HTTP 418: no error")
	}
}
