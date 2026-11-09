package platform

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestListRepos(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch {
		case r.Header.Get("Authorization") == "":
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasPrefix(r.URL.Path, "/orgs/user/"):
			http.NotFound(w, r)
		case strings.HasPrefix(r.URL.Path, "/users/user/"):
			fmt.Fprint(w, `[{"full_name":"user/a"}]`)
		case r.URL.Path == "/orgs/org/repos" && r.URL.Query().Get("page") == "1":
			// A full GitHub page (100), of which two are left out.
			var b strings.Builder
			b.WriteString(`[{"full_name":"org/archived","archived":true},{"full_name":"org/mirror","mirror":true}`)
			for i := range 98 {
				fmt.Fprintf(&b, `,{"full_name":"org/r%d"}`, i)
			}
			b.WriteString("]")
			fmt.Fprint(w, b.String())
		case r.URL.Path == "/orgs/org/repos":
			fmt.Fprint(w, `[{"full_name":"org/last"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	gh := Info{Kind: GitHub, APIURL: srv.URL}
	got, err := ListRepos(t.Context(), gh, "tok", srv.Client(), "org")
	if err != nil || len(got) != 99 || got[0] != "org/r0" || got[98] != "org/last" || slices.Contains(got, "org/archived") || slices.Contains(got, "org/mirror") {
		t.Fatalf("ListRepos(org) = %v, %v", got, err)
	}
	if !slices.Equal(paths, []string{"/orgs/org/repos?per_page=100&type=all&page=1", "/orgs/org/repos?per_page=100&type=all&page=2"}) {
		t.Errorf("paths = %v", paths)
	}
	paths = nil
	got, err = ListRepos(t.Context(), Info{Kind: Gitea, APIURL: srv.URL}, "tok", srv.Client(), "user")
	if err != nil || !slices.Equal(got, []string{"user/a"}) {
		t.Fatalf("ListRepos(user) = %v, %v", got, err)
	}
	if !slices.Equal(paths, []string{"/orgs/user/repos?limit=50&page=1", "/users/user/repos?limit=50&page=1"}) {
		t.Errorf("paths = %v", paths)
	}
	if _, err := ListRepos(t.Context(), gh, "", srv.Client(), "org"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("without token: %v", err)
	}
}

func TestListReposGitLab(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		switch {
		case r.Header.Get("Authorization") != "Bearer tok":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.EscapedPath() == "/groups/group%2Fsub/projects" && r.URL.Query().Get("page") == "1":
			// A full page (100), of which one is a pull mirror.
			var b strings.Builder
			b.WriteString(`[{"path_with_namespace":"group/sub/mirror","mirror":true}`)
			for i := range 99 {
				fmt.Fprintf(&b, `,{"path_with_namespace":"group/sub/deep/p%d"}`, i)
			}
			b.WriteString("]")
			fmt.Fprint(w, b.String())
		case r.URL.EscapedPath() == "/groups/group%2Fsub/projects":
			fmt.Fprint(w, `[{"path_with_namespace":"group/sub/last"}]`)
		case r.URL.Path == "/users/alice/projects":
			fmt.Fprint(w, `[{"path_with_namespace":"alice/a"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	gl := Info{Kind: GitLab, APIURL: srv.URL}
	got, err := ListRepos(t.Context(), gl, "tok", srv.Client(), "group/sub")
	if err != nil || len(got) != 100 || got[0] != "group/sub/deep/p0" || got[99] != "group/sub/last" || slices.Contains(got, "group/sub/mirror") {
		t.Fatalf("ListRepos(group/sub) = %v, %v", got, err)
	}
	const q = "per_page=100&archived=false&order_by=id&sort=asc"
	if want := []string{
		"/groups/group%2Fsub/projects?include_subgroups=true&with_shared=false&" + q + "&page=1",
		"/groups/group%2Fsub/projects?include_subgroups=true&with_shared=false&" + q + "&page=2",
	}; !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	paths = nil
	got, err = ListRepos(t.Context(), gl, "tok", srv.Client(), "alice")
	if err != nil || !slices.Equal(got, []string{"alice/a"}) {
		t.Fatalf("ListRepos(alice) = %v, %v", got, err)
	}
	if want := []string{"/groups/alice/projects?include_subgroups=true&with_shared=false&" + q + "&page=1", "/users/alice/projects?" + q + "&page=1"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	if _, err := ListRepos(t.Context(), gl, "", srv.Client(), "group"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("without token: %v", err)
	}
}

func TestRemoteServerURL(t *testing.T) {
	for remote, want := range map[string]string{
		"https://git.example.com:3000/o/r.git": "https://git.example.com:3000",
		"git@github.com:o/r.git":               "https://github.com",
		"ssh://git@host:2222/o/r":              "https://host",
	} {
		if got, err := RemoteServerURL(remote); err != nil || got != want {
			t.Errorf("RemoteServerURL(%q) = %q, %v; want %q", remote, got, err, want)
		}
	}
	if _, err := RemoteServerURL("/local/path"); err == nil {
		t.Error("local path: no error")
	}
}
