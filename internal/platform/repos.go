package platform

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// maxRepoPages bounds the pages ListRepos reads (100 repositories each on GitHub, 50 on Gitea
// and Forgejo).
const maxRepoPages = 50

// apiRepoEntry covers the repository list JSON of GitHub and Gitea/Forgejo.
type apiRepoEntry struct {
	FullName string `json:"full_name"`
	Archived bool   `json:"archived"`
	Mirror   bool   `json:"mirror"` // Gitea and Forgejo: pull mirrors cannot be pushed to
}

// ListRepos returns the owner/name of the repositories of owner, an organization or else a
// user, that token can see, in the forge's order; archived repositories and mirrors are left
// out, as deps cannot push to them. GitLab is not supported: its projects must be named.
func ListRepos(ctx context.Context, info Info, token string, c *http.Client, owner string) ([]string, error) {
	if info.Kind == GitLab {
		return nil, errors.New("listing repositories is not supported on GitLab; name each project")
	}
	r := &rest{api: info.APIURL, c: c, headers: giteaHeaders(token)}
	per, query := 50, "limit=50"
	if info.Kind == GitHub {
		r.headers = githubHeaders(token)
		per, query = 100, "per_page=100&type=all"
	}
	esc := url.PathEscape(owner)
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
			if !e.Archived && !e.Mirror && e.FullName != "" {
				out = append(out, e.FullName)
			}
		}
		if len(rs) < per {
			break
		}
	}
	return out, nil
}
