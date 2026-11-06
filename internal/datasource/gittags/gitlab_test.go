package gittags

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	forgeds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
)

// Tags of repositories on a GitLab instance (here self-managed under /gitlab) are dated by the
// gitlab-releases and gitlab-tags lists, each fetched once: the release, else a release of the
// same commit (the floating v1 next to v1.128.0), else the tag's commit date. A project whose
// release list fails is dated by its tags. Commit pins use the commits API. The entries are
// gitlab.com's answers for gitlab-org/cli (fetched 2026-11-06), cut to the fields deps reads.
func TestGitLabTimes(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs []string
	)
	const (
		released = "519ea19f7d3e9c701a4063f277ae27254bbafdad"
		older    = "8f7e7d6a7e8d1c0b2c3a4f5e6d7c8b9a0f1e2d3c"
	)
	refs := pkts(serviceHeader, "0000", strings.Repeat("0", 40)+" HEAD\x00agent=git/2.45\n",
		released+" refs/tags/v1.128.0\n", released+" refs/tags/v1\n", older+" refs/tags/v1.127.0\n", "0000")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/gitlab/gitlab-org/cli.git/info/refs", "/gitlab/grp/no-releases/info/refs":
			io.WriteString(w, refs)
		case "/gitlab/api/v4/projects/gitlab-org%2Fcli/releases":
			io.WriteString(w, `[{"tag_name":"v1.128.0","released_at":"2026-11-03T12:18:50.742Z","upcoming_release":false,
				"commit":{"id":"`+released+`","created_at":"2026-11-03T11:07:41.000+00:00"}}]`)
		case "/gitlab/api/v4/projects/gitlab-org%2Fcli/repository/tags", "/gitlab/api/v4/projects/grp%2Fno-releases/repository/tags":
			io.WriteString(w, `[{"name":"v1.128.0","created_at":"2026-11-03T12:07:37.000Z","commit":{"id":"`+released+`","created_at":"2026-11-03T11:07:41.000+00:00"}},
				{"name":"v1","commit":{"id":"`+released+`","created_at":"2026-11-03T11:07:41.000+00:00"}},
				{"name":"v1.127.0","commit":{"id":"`+older+`","created_at":"2026-10-20T09:00:00.000+00:00"}}]`)
		case "/gitlab/api/v4/projects/grp%2Fno-releases/releases":
			w.WriteHeader(http.StatusForbidden)
		case "/gitlab/api/v4/projects/gitlab-org%2Fcli/repository/commits/" + released:
			io.WriteString(w, `{"id":"`+released+`","committed_date":"2026-11-03T11:07:41.000+00:00","created_at":"2026-11-03T11:07:41.000+00:00"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	gh := githubapi.New(srv.Client(), nil, nil, githubapi.WithAPI(srv.URL, srv.URL+"/graphql"))
	base := srv.URL + "/gitlab"
	l := New(srv.Client(), nil, make(chan struct{}, 1), gh).WithGitLab(GitLab{
		Bases:    []string{"https://gitlab.com", base + "/"},
		Releases: forgeds.New(forgeds.GitLabReleases, srv.Client(), nil, nil),
		Tags:     forgeds.New(forgeds.GitLabTags, srv.Client(), nil, nil),
	})
	var (
		timer datasource.Timer       = l
		ct    datasource.CommitTimer = l
	)
	cli, noRel := dep.Dep{Source: base + "/gitlab-org/cli.git"}, dep.Dep{Source: base + "/grp/no-releases"}
	rel := time.Date(2026, 11, 3, 12, 18, 50, 742e6, time.UTC)
	for _, tt := range []struct {
		d       dep.Dep
		version string
		want    time.Time
	}{
		{cli, "v1.128.0", rel},
		{cli, "v1", rel}, // floats on v1.128.0's commit
		{cli, "v1.127.0", time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)},
		{cli, "v0.1.0", time.Time{}},
		{noRel, "v1", time.Date(2026, 11, 3, 11, 7, 41, 0, time.UTC)},
	} {
		for range 2 {
			got, err := timer.ReleaseTime(t.Context(), tt.d, tt.version)
			if err != nil || !got.Equal(tt.want) {
				t.Fatalf("%s@%s = %v, %v; want %v", tt.d.Source, tt.version, got, err, tt.want)
			}
		}
	}
	if got, err := ct.CommitTime(t.Context(), cli, released); err != nil || !got.Equal(time.Date(2026, 11, 3, 11, 7, 41, 0, time.UTC)) {
		t.Errorf("CommitTime = %v, %v", got, err)
	}
	if got, err := ct.CommitTime(t.Context(), cli, older); err != nil || !got.IsZero() {
		t.Errorf("CommitTime of a missing commit = %v, %v", got, err)
	}
	count := func(prefix string) int {
		return len(slices.DeleteFunc(slices.Clone(reqs), func(r string) bool { return !strings.HasPrefix(r, prefix) }))
	}
	for prefix, want := range map[string]int{
		"/gitlab/api/v4/projects/gitlab-org%2Fcli/releases?":        1,
		"/gitlab/api/v4/projects/gitlab-org%2Fcli/repository/tags?": 1,
		"/gitlab/gitlab-org/cli.git/info/refs":                      1,
		"/gitlab/api/v1/":                                           0, // no Gitea probe
	} {
		if n := count(prefix); n != want {
			t.Errorf("%s: %d requests, want %d (%q)", prefix, n, want, reqs)
		}
	}
}

func TestGitLabRepo(t *testing.T) {
	l := New(nil, nil, nil, nil)
	if _, ok := l.gitlabRepo("https://gitlab.com/a/b"); ok {
		t.Error("no GitLab configured: matched")
	}
	l.WithGitLab(GitLab{Bases: []string{"https://gitlab.com", "https://git.example/gl"}})
	for in, want := range map[string]string{
		"https://gitlab.com/a/b":            "a/b@https://gitlab.com",
		"https://GitLab.com/g/s/p.git":      "g/s/p@https://gitlab.com",
		"https://git.example/gl/g/p":        "g/p@https://git.example/gl",
		"https://git.example/glx/g/p":       "",
		"https://git.example/gl":            "",
		"https://gitlab.com.evil.example/a": "",
		"https://github.com/a/b":            "",
	} {
		d, ok := l.gitlabRepo(in)
		got := ""
		if ok {
			got = d.Name + "@" + d.Source
		}
		if got != want {
			t.Errorf("%s = %q, want %q", in, got, want)
		}
	}
}
