package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
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
// (gitea/tea) and code.forgejo.org (forgejo/runner).
func TestRealResponses(t *testing.T) {
	for _, tc := range []struct {
		ds, name, path, file string
		want                 []string
		time                 string // of want[0]
	}{
		{GitLabReleases, "gitlab-org/cli", "/api/v4/projects/gitlab-org%2Fcli/releases", "gitlab-releases.json",
			[]string{"v1.128.0", "v1.127.0", "v1.126.0"}, "2026-11-03T12:18:50.742Z"},
		{GitLabTags, "gitlab-org/cli", "/api/v4/projects/gitlab-org%2Fcli/repository/tags", "gitlab-tags.json",
			[]string{"v1.128.0@519ea19f7d3e9c701a4063f277ae27254bbafdad", "v1.127.0@2e352a17e7e00bef420b0f38e0ce9ab4e1a38e5d", "v1.126.0@4dd0d32d18361db6c1c668eb4ac691c0d6edf255"}, "2026-11-03T11:07:41Z"},
		{GiteaReleases, "gitea/tea", "/api/v1/repos/gitea/tea/releases", "gitea-releases.json",
			[]string{"v0.16.0", "v0.15.1", "v0.15.0"}, "2026-09-10T11:34:12Z"},
		{GiteaTags, "gitea/tea", "/api/v1/repos/gitea/tea/tags", "gitea-tags.json",
			[]string{"v0.16.0@9c12138d6273ba8feb6e7578833cc330c0cd6461", "v0.15.1@f34697c5ed65928e265d6f48e16928819ce0f332", "v0.15.0@61b8536e4a3e04d9db74b05b4d9cd489d07d8d50"}, "2026-09-10T04:29:39Z"},
		{ForgejoReleases, "forgejo/runner", "/api/v1/repos/forgejo/runner/releases", "forgejo-releases.json",
			[]string{"v13.2.1", "v13.2.0", "v13.1.0"}, "2026-10-29T21:36:30Z"},
		{ForgejoTags, "forgejo/runner", "/api/v1/repos/forgejo/runner/tags", "forgejo-tags.json",
			[]string{"v13.2.1@c4a92ffa5e3a400ae7e9c40a2914e649eb338ad7", "v13.2.0@df6b843fb929bb04b933b09c6bf208774d42480b", "v13.1.0@6095cb17bfdbded5aa4ea84c18a4b69fd9574cca"}, "2026-10-02T02:59:29Z"},
	} {
		t.Run(tc.ds, func(t *testing.T) {
			var queries []string
			srv := serve(t, map[string]string{tc.path: tc.file}, func(r *http.Request) { queries = append(queries, r.URL.RawQuery) })
			s := New(tc.ds, srv.Client(), nil, newAuth())
			d := dep.Dep{Datasource: tc.ds, Name: tc.name, Source: srv.URL + "/"}
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
			if len(queries) != 1 || !strings.Contains(queries[0], "page=1") {
				t.Errorf("queries = %v, want one first page (lists are fetched once)", queries)
			}
		})
	}
}

// TestFiltersUnpublished drops drafts and prereleases (Gitea/Forgejo) and upcoming releases
// (GitLab).
func TestFiltersUnpublished(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v4/") {
			fmt.Fprint(w, `[{"tag_name":"v3","upcoming_release":true},{"tag_name":"v2","released_at":null},{"tag_name":""},{"tag_name":"v2"}]`)
			return
		}
		fmt.Fprint(w, `[{"tag_name":"v4","draft":true},{"tag_name":"v3-rc1","prerelease":true},{"tag_name":"v2"}]`)
	}))
	defer srv.Close()
	for _, ds := range []string{GitLabReleases, GiteaReleases} {
		s := New(ds, srv.Client(), nil, newAuth())
		rels, err := s.Releases(t.Context(), dep.Dep{Name: "o/r", Source: srv.URL}, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := versions(rels); !slices.Equal(got, []string{"v2"}) {
			t.Errorf("%s releases = %v, want [v2]", ds, got)
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
				fmt.Fprint(w, `[{"tag_name":"v1","name":"v1","commit":{"id":"a","sha":"a"}}]`)
			}))
			defer srv.Close()
			opt := httpx.WithToken("127.0.0.1", tc.user, "tok")
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
			want := int32(1)
			if tc.helper {
				want = 2
			}
			if requests.Load() != want || helped.Load() != want-1 {
				t.Errorf("requests = %d, helper calls = %d", requests.Load(), helped.Load())
			}
		})
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
