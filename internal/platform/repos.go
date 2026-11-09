package platform

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// maxRepoPages bounds the pages ListRepos reads (100 repositories each on GitHub and GitLab,
// 50 on Gitea and Forgejo).
const maxRepoPages = 50

// apiRepoEntry covers the repository list JSON of GitHub, Gitea/Forgejo and GitLab.
type apiRepoEntry struct {
	FullName          string `json:"full_name"`
	PathWithNamespace string `json:"path_with_namespace"` // GitLab
	Archived          bool   `json:"archived"`
	Mirror            bool   `json:"mirror"` // Gitea, Forgejo and GitLab: pull mirrors cannot be pushed to
}

// ListRepos returns the owner/name of the repositories of owner, an organization or else a
// user, that token can see, in the forge's order; archived repositories and mirrors are left
// out, as deps cannot push to them. On GitLab owner is a group path, whose projects are
// listed with those of its subgroups (as group/sub/name), else a username.
func ListRepos(ctx context.Context, info Info, token string, c *http.Client, owner string) ([]string, error) {
	esc := url.PathEscape(owner)
	if info.Kind == GitLab {
		// Projects shared with the group from other namespaces are not its own.
		r := &rest{api: info.APIURL, c: c, headers: gitlabHeaders(token)}
		const query = "per_page=100&archived=false&order_by=id&sort=asc"
		out, err := listRepos(ctx, r, 100, "/groups/"+esc+"/projects?include_subgroups=true&with_shared=false&"+query)
		if hasStatus(err, http.StatusNotFound, "") {
			out, err = listRepos(ctx, r, 100, "/users/"+esc+"/projects?"+query)
		}
		return out, err
	}
	r := &rest{api: info.APIURL, c: c, headers: giteaHeaders(token)}
	per, query := 50, "limit=50"
	if info.Kind == GitHub {
		r.headers = githubHeaders(token)
		per, query = 100, "per_page=100&type=all"
	}
	out, err := listRepos(ctx, r, per, "/orgs/"+esc+"/repos?"+query)
	if hasStatus(err, http.StatusNotFound, "") {
		out, err = listRepos(ctx, r, per, "/users/"+esc+"/repos?"+query)
	}
	return out, err
}

func listRepos(ctx context.Context, r *rest, per int, path string) ([]string, error) {
	var out []string
	for page := 1; page <= maxRepoPages; page++ {
		var rs []apiRepoEntry
		if err := r.do(ctx, http.MethodGet, path+"&page="+strconv.Itoa(page), nil, &rs, r.headers); err != nil {
			return nil, err
		}
		for _, e := range rs {
			if name := cmp.Or(e.FullName, e.PathWithNamespace); !e.Archived && !e.Mirror && name != "" {
				out = append(out, name)
			}
		}
		if len(rs) < per {
			break
		}
	}
	return out, nil
}
