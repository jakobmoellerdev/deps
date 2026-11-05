package platform

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// giteaClient serves Gitea and Forgejo (same API surface).
type giteaClient struct {
	rest
	forgejo bool // the merge form field is "Do" on Forgejo, "do" on Gitea
}

func (g *giteaClient) headers(h http.Header) {
	if g.token != "" {
		h.Set("Authorization", "token "+g.token)
	}
	h.Set("Accept", "application/json")
}

func (g *giteaClient) toPR(p *apiPR) PR {
	pr := p.pr()
	pr.Merged = p.Merged
	m := p.Mergeable != nil && *p.Mergeable
	pr.Mergeable = &m
	return pr
}

func (g *giteaClient) DefaultBranch(ctx context.Context) (string, error) {
	return defaultBranch(ctx, &g.rest, g.headers)
}

func (g *giteaClient) ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) {
	return listPRs(ctx, &g.rest, g.headers, g.toPR, 50, maxPages, func(page int) string {
		return "/repos/" + g.repo + "/pulls?state=" + state + "&sort=recentupdate&limit=50&page=" + strconv.Itoa(page)
	})
}

func (g *giteaClient) GetPR(ctx context.Context, n int) (PR, error) {
	var p apiPR
	if err := g.do(ctx, http.MethodGet, prPath(g.repo, n), nil, &p, g.headers); err != nil {
		return PR{}, err
	}
	return g.toPR(&p), nil
}

func (g *giteaClient) CreatePR(ctx context.Context, head, base, title, body string) (PR, error) {
	var p apiPR
	in := prCreate{Title: title, Head: head, Base: base, Body: body}
	if err := g.do(ctx, http.MethodPost, "/repos/"+g.repo+"/pulls", in, &p, g.headers); err != nil {
		return PR{}, err
	}
	return g.toPR(&p), nil
}

func (g *giteaClient) UpdatePR(ctx context.Context, n int, title, body string) error {
	return g.do(ctx, http.MethodPatch, prPath(g.repo, n), prEdit{Title: title, Body: body}, nil, g.headers)
}

func (g *giteaClient) ClosePR(ctx context.Context, n int) error {
	return g.do(ctx, http.MethodPatch, prPath(g.repo, n), prEdit{State: "closed"}, nil, g.headers)
}

// EnableAutoMerge schedules the merge with POST /pulls/{n}/merge and
// merge_when_checks_succeed (MergePullRequestOption); the forge merges right away when the
// checks already passed. 409 "already scheduled" is success; 405 "Please try again later"
// (the mergeability check of a fresh push is still running, or the PR conflicts) is
// ErrAutoMergePending.
func (g *giteaClient) EnableAutoMerge(ctx context.Context, pr PR, strategy string) error {
	err := g.merge(ctx, pr, strategy, true)
	switch {
	case hasStatus(err, http.StatusConflict, "already scheduled"):
		return nil
	case hasStatus(err, http.StatusMethodNotAllowed, "try again later"):
		return fmt.Errorf("enable auto-merge #%d: %w: %w", pr.Number, ErrAutoMergePending, err)
	}
	return err
}

// MergePR merges with POST /pulls/{n}/merge; head_commit_id makes the forge refuse a head that
// moved.
func (g *giteaClient) MergePR(ctx context.Context, pr PR, strategy string) error {
	return g.merge(ctx, pr, strategy, false)
}

func (g *giteaClient) merge(ctx context.Context, pr PR, strategy string, whenChecksSucceed bool) error {
	style, err := g.style(ctx, strategy)
	if err != nil {
		return err
	}
	// Gitea 28 names the style field "do", Forgejo "Do" (swagger.v1.json of both).
	doKey := "do"
	if g.forgejo {
		doKey = "Do"
	}
	in := map[string]any{doKey: style, "delete_branch_after_merge": true}
	if whenChecksSucceed {
		in["merge_when_checks_succeed"] = true
	}
	if pr.HeadSHA != "" {
		in["head_commit_id"] = pr.HeadSHA
	}
	return g.do(ctx, http.MethodPost, prPath(g.repo, pr.Number)+"/merge", in, nil, g.headers)
}

// style maps strategy to a merge style; auto is the repository's default_merge_style, merge
// when unset.
func (g *giteaClient) style(ctx context.Context, strategy string) (string, error) {
	switch strategy {
	case "merge", "squash", "rebase":
		return strategy, nil
	case "", "auto":
		repo, err := g.repository(ctx, g.headers)
		if err != nil {
			return "", err
		}
		if repo.DefaultMergeStyle != "" {
			return repo.DefaultMergeStyle, nil
		}
		return "merge", nil
	}
	return "", fmt.Errorf("unknown merge strategy %q", strategy)
}

// Checks reads the combined commit status (Gitea and Forgejo Actions report commit statuses).
// States other than success and pending (failure, error, warning) count as failed.
func (g *giteaClient) Checks(ctx context.Context, sha string) (CheckState, error) {
	var st struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
	}
	if err := g.do(ctx, http.MethodGet, "/repos/"+g.repo+"/commits/"+sha+"/status", nil, &st, g.headers); err != nil {
		return "", err
	}
	switch {
	case st.TotalCount == 0:
		return ChecksNone, nil
	case st.State == "success":
		return ChecksSuccess, nil
	case st.State == "pending":
		return ChecksPending, nil
	}
	return ChecksFailure, nil
}

// ListIssues pages through open issues. A 404 for an existing repository whose issues are
// disabled or external (has_issues false, external_tracker set) is ErrIssuesDisabled.
func (g *giteaClient) ListIssues(ctx context.Context, maxPages int) ([]Issue, error) {
	is, err := listIssues(ctx, &g.rest, g.headers, 50, maxPages, func(page int) string {
		return "/repos/" + g.repo + "/issues?state=open&type=issues&limit=50&page=" + strconv.Itoa(page)
	})
	if hasStatus(err, http.StatusNotFound, "") {
		if repo, rerr := g.repository(ctx, g.headers); rerr == nil &&
			(repo.HasIssues != nil && !*repo.HasIssues || len(repo.ExternalTracker) > 0 && string(repo.ExternalTracker) != "null") {
			return nil, fmt.Errorf("%w: %w", ErrIssuesDisabled, err)
		}
	}
	return is, err
}

// CreateIssue creates an issue. Gitea and Forgejo take label ids: names are looked up in the
// repository's labels, then its organization's; unknown names are left out with a warning.
func (g *giteaClient) CreateIssue(ctx context.Context, title, body string, labels []string) (Issue, error) {
	ids, err := g.labelIDs(ctx, labels)
	if err != nil {
		return Issue{}, err
	}
	in := struct {
		Title  string  `json:"title"`
		Body   string  `json:"body"`
		Labels []int64 `json:"labels,omitempty"`
	}{title, body, ids}
	var out apiIssue
	if err := g.do(ctx, http.MethodPost, "/repos/"+g.repo+"/issues", in, &out, g.headers); err != nil {
		return Issue{}, err
	}
	return out.issue(), nil
}

func (g *giteaClient) UpdateIssue(ctx context.Context, n int, body string) error {
	return g.do(ctx, http.MethodPatch, issuePath(g.repo, n), map[string]string{"body": body}, nil, g.headers)
}

// labelIDs resolves label names (case-insensitive) to ids.
func (g *giteaClient) labelIDs(ctx context.Context, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}
	byName := map[string]int64{}
	owner, _, _ := strings.Cut(g.repo, "/")
	for _, base := range []string{"/repos/" + g.repo + "/labels", "/orgs/" + owner + "/labels"} {
		for page := 1; page <= 10; page++ {
			var ls []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}
			err := g.do(ctx, http.MethodGet, base+"?limit=50&page="+strconv.Itoa(page), nil, &ls, g.headers)
			if hasStatus(err, http.StatusNotFound, "") && strings.HasPrefix(base, "/orgs/") {
				break // the owner is a user
			}
			if err != nil {
				return nil, err
			}
			for _, l := range ls {
				if _, ok := byName[strings.ToLower(l.Name)]; !ok {
					byName[strings.ToLower(l.Name)] = l.ID
				}
			}
			if len(ls) < 50 {
				break
			}
		}
		if allKnown(byName, names) {
			break
		}
	}
	var ids []int64
	for _, n := range names {
		id, ok := byName[strings.ToLower(n)]
		if !ok {
			slog.WarnContext(ctx, "label not found, creating the issue without it", "label", n, "repo", g.repo)
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func allKnown(byName map[string]int64, names []string) bool {
	for _, n := range names {
		if _, ok := byName[strings.ToLower(n)]; !ok {
			return false
		}
	}
	return true
}
