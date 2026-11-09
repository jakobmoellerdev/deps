package gittags

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	forgeds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// TestArchived asks each repository once, alongside its tags, whether it is archived:
// github.com through GraphQL `isArchived` (with a token), all planned repositories in one
// query and an unplanned one on its own; the GitLab instances through gitlab-tags (GraphQL
// `project{archived}`); other hosts through the Gitea/Forgejo API (`archived` of gitea.com's
// gitea/tea, archived on 2026-10-29). Without a GitHub token, on a host without the API and
// for a repository the API does not show, nothing is reported. Deprecation itself sends no
// request.
func TestArchived(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs []string
	)
	tags := pkts(serviceHeader, "0000", strings.Repeat("0", 40)+" HEAD\x00agent=git/2.45\n",
		strings.Repeat("1", 40)+" refs/tags/v1.0.0\n", "0000")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info/refs") {
			io.WriteString(w, tags)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/graphql" {
			var q struct {
				Query     string            `json:"query"`
				Variables map[string]string `json:"variables"`
			}
			_ = json.UnmarshalRead(r.Body, &q)
			var data, errs []string
			var asked []string
			for i := 0; q.Variables["o"+strconv.Itoa(i)] != ""; i++ {
				n := strconv.Itoa(i)
				repo := q.Variables["o"+n] + "/" + q.Variables["n"+n]
				asked = append(asked, repo)
				if repo == "gone/repo" {
					data = append(data, `"r`+n+`":null`)
					errs = append(errs, `{"type":"NOT_FOUND","path":["r`+n+`"],"message":"Could not resolve to a Repository with the name 'gone/repo'."}`)
				} else {
					data = append(data, `"r`+n+`":{"isArchived":`+strconv.FormatBool(repo == "old/action")+`}`)
				}
			}
			reqs = append(reqs, "/graphql "+strings.Join(asked, ","))
			io.WriteString(w, `{"data":{`+strings.Join(data, ",")+`},"errors":[`+strings.Join(errs, ",")+`]}`)
			return
		}
		reqs = append(reqs, r.URL.Path)
		switch r.URL.Path {
		case "/gitlab/api/graphql":
			if r.URL.Query().Get("variables") == `{"p":"grp/old"}` {
				io.WriteString(w, `{"data":{"project":{"archived":true}}}`)
				return
			}
			io.WriteString(w, `{"data":{"project":null}}`)
		case "/api/v1/repos/gitea/tea":
			io.WriteString(w, `{"id":550,"full_name":"gitea/tea","archived":true,"archived_at":"2026-10-29T21:07:11Z"}`)
		case "/api/v1/repos/forgejo/runner":
			io.WriteString(w, `{"id":2,"full_name":"forgejo/runner","archived":false,"archived_at":"1970-01-01T00:00:00Z"}`)
		case "/api/v1/repos/o/web":
			io.WriteString(w, `<html>not an API</html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	// Requests for github.com go to srv as well.
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "github.com" {
			r = r.Clone(r.Context())
			r.URL.Scheme, r.URL.Host = "http", u.Host
		}
		return srv.Client().Transport.RoundTrip(r)
	})}
	lister := func(ghToken string) *Lister {
		auth := httpx.NewAuth(platform.Info{}, "", func(k string) string {
			return map[string]string{"DEPS_GITHUB_COM_TOKEN": ghToken}[k]
		})
		gh := githubapi.New(srv.Client(), auth, nil, githubapi.WithAPI(srv.URL, srv.URL+"/graphql"))
		return New(client, auth, make(chan struct{}, 2), gh).WithGitLab(GitLab{
			Bases: []string{srv.URL + "/gitlab"},
			Tags:  forgeds.New(forgeds.GitLabTags, srv.Client(), nil, nil),
		})
	}
	l := lister("gh-tok")
	var refs datasource.Datasource = l.Refs()
	l.Plan([]dep.Dep{{Source: "https://github.com/old/action"}, {Source: "https://github.com/actions/checkout/"}, {Source: srv.URL + "/gitea/tea"}})
	refs.(datasource.Planner).Plan([]dep.Dep{{Source: "https://github.com/gone/repo"}, {Source: "https://github.com/Old/Action"}})
	for _, tt := range []struct {
		repo string
		want string
	}{
		{"https://github.com/old/action", "repository archived"},
		{"https://github.com/actions/checkout", ""},
		{"https://github.com/gone/repo", ""},
		{"https://github.com/late/unplanned", ""},
		{srv.URL + "/gitlab/grp/old.git", "repository archived"},
		{srv.URL + "/gitlab/grp/private", ""},
		{srv.URL + "/gitea/tea/", "repository archived"},
		{srv.URL + "/forgejo/runner", ""},
		{srv.URL + "/o/web", ""},
		{srv.URL + "/o/none", ""},
	} {
		d := dep.Dep{Datasource: "git-tags", Source: tt.repo}
		if why := l.Deprecation(d, "v1.0.0"); why != "" {
			t.Errorf("%s: Deprecation before Releases = %q", tt.repo, why)
		}
		for range 2 {
			if _, err := l.Releases(t.Context(), d, ""); err != nil {
				t.Fatalf("%s: %v", tt.repo, err)
			}
		}
		if why := l.Deprecation(d, "v1.0.0"); why != tt.want {
			t.Errorf("%s: Deprecation = %q, want %q", tt.repo, why, tt.want)
		}
		if why := refs.(datasource.DeprecationSource).Deprecation(d, ""); why != tt.want {
			t.Errorf("%s: git-refs Deprecation = %q, want %q", tt.repo, why, tt.want)
		}
	}
	slices.Sort(reqs)
	want := []string{
		"/api/v1/repos/forgejo/runner", "/api/v1/repos/gitea/tea", "/api/v1/repos/o/none", "/api/v1/repos/o/web",
		"/gitlab/api/graphql", "/gitlab/api/graphql",
		"/graphql actions/checkout,gone/repo,old/action", "/graphql late/unplanned",
	}
	if !slices.Equal(reqs, want) {
		t.Errorf("API requests =\n%q\nwant one per repository\n%q", reqs, want)
	}

	// Without a GitHub token the REST API is not asked (no archived flag, scarce anonymous
	// requests).
	reqs = nil
	l = lister("")
	d := dep.Dep{Source: "https://github.com/old/action"}
	if _, err := l.Releases(t.Context(), d, ""); err != nil {
		t.Fatal(err)
	}
	if why := l.Deprecation(d, "v1.0.0"); why != "" || len(reqs) != 0 {
		t.Errorf("without a token: Deprecation = %q, API requests %q", why, reqs)
	}
	if _, err := l.Archived(t.Context(), "::"); err == nil {
		t.Error("Archived(invalid URL): no error")
	}
}

// TestGitHubArchivedRateLimited reports nothing when the batch query fails, and asks again
// for none of the planned repositories.
func TestGitHubArchivedRateLimited(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`)
	}))
	t.Cleanup(srv.Close)
	auth := httpx.NewAuth(platform.Info{}, "", func(k string) string {
		return map[string]string{"DEPS_GITHUB_COM_TOKEN": "gh-tok"}[k]
	})
	gh := githubapi.New(srv.Client(), auth, nil, githubapi.WithAPI(srv.URL, srv.URL+"/graphql"))
	l := New(srv.Client(), auth, nil, gh)
	l.Plan([]dep.Dep{{Source: "https://github.com/a/b"}, {Source: "https://github.com/c/d"}})
	for _, repo := range []string{"https://github.com/a/b", "https://github.com/c/d"} {
		if archived, err := l.Archived(t.Context(), repo); archived || !errors.Is(err, httpx.ErrRateLimited) {
			t.Errorf("Archived(%s) = %t, %v", repo, archived, err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("GraphQL requests = %d, want 1", n)
	}
}
