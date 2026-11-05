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
	paths = nil
	if _, err := ListRepos(t.Context(), Info{Kind: GitLab, APIURL: srv.URL}, "tok", srv.Client(), "group"); err == nil || !strings.Contains(err.Error(), "not supported on GitLab") || len(paths) != 0 {
		t.Errorf("GitLab: %v, paths %v", err, paths)
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
