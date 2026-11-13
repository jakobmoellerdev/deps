package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// giteaClient serves Gitea and Forgejo (same API surface).
type giteaClient struct {
	rest
	forgejo bool // the merge form field is "Do" on Forgejo, "do" on Gitea

	labelsMu     sync.Mutex
	labels       map[string]int64 // lowercase name -> id of the labels loaded so far
	labelSources int              // label lists loaded: the repository's, then the organization's
}

func giteaHeaders(token string) func(http.Header) {
	return func(h http.Header) {
		if token != "" {
			h.Set("Authorization", "token "+token)
		}
		h.Set("Accept", "application/json")
	}
}

// giteaPR converts a pull request of Gitea or Forgejo. A draft is marked by a WIP prefix in
// the title there; the prefix is the forge's mark, not part of the title deps wrote, so it is
// cut off again and reported as Draft. Callers compare the title with the one they would
// write (closedFor), which a leading "WIP: " would never match.
func giteaPR(p *apiPR) PR {
	pr := p.pr()
	pr.Merged = p.Merged
	m := p.Mergeable != nil && *p.Mergeable
	pr.Mergeable = &m
	title, wip := cutPrefixFold(p.Title, wipPrefixes)
	pr.Title = title
	pr.Draft = pr.Draft || wip // older forges report no draft field
	return pr
}

// wipPrefix marks a draft PR on Gitea and Forgejo; their default WORK_IN_PROGRESS_PREFIXES
// are wipPrefixes, compared case-insensitively. Their API exposes neither the configured
// prefixes nor the title without them, so deps assumes the defaults; a forge configured with
// other prefixes keeps them in the title it reports.
const wipPrefix = "WIP: "

var wipPrefixes = []string{"WIP:", "[WIP]"}

// cutPrefixFold cuts the first of the upper-case prefixes title starts with, compared
// case-insensitively, and the blanks following it; ok reports whether one matched.
func cutPrefixFold(title string, prefixes []string) (rest string, ok bool) {
	for _, p := range prefixes {
		if len(title) >= len(p) && strings.EqualFold(title[:len(p)], p) {
			return strings.TrimLeft(title[len(p):], " \t"), true
		}
	}
	return title, false
}

// hasPrefixFold reports whether title starts with one of the upper-case prefixes, compared
// case-insensitively.
func hasPrefixFold(title string, prefixes []string) bool {
	_, ok := cutPrefixFold(title, prefixes)
	return ok
}

// draftTitle returns title with prefix when draft and it starts with none of the marks yet.
func draftTitle(title string, draft bool, prefix string, marks []string) string {
	if !draft || hasPrefixFold(title, marks) {
		return title
	}
	return prefix + title
}

// CreatePR opens a draft PR by its title: the forges' API has no draft flag.
func (g *giteaClient) CreatePR(ctx context.Context, head, base, title, body string, draft bool) (PR, error) {
	return g.createPR(ctx, prCreate{Title: draftTitle(title, draft, wipPrefix, wipPrefixes), Head: head, Base: base, Body: body})
}

// UpdatePR keeps the WIP prefix of a draft PR, which marks it as one.
func (g *giteaClient) UpdatePR(ctx context.Context, n int, title, body string, draft bool) error {
	return g.updatePR(ctx, n, draftTitle(title, draft, wipPrefix, wipPrefixes), body)
}

func (g *giteaClient) ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) {
	return listPRs(ctx, &g.rest, g.headers, g.toPR, 50, maxPages, func(page int) string {
		return "/repos/" + g.repo + "/pulls?state=" + state + "&sort=recentupdate&limit=50&page=" + strconv.Itoa(page)
	})
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
func (g *giteaClient) ListIssues(ctx context.Context, maxPages int, f IssueFilter) ([]Issue, error) {
	order := ""
	if f.Oldest {
		order = "&sort=oldest"
	}
	is, err := listIssues(ctx, &g.rest, g.headers, 50, maxPages, f.Stop, func(page int) string {
		return "/repos/" + g.repo + "/issues?state=open&type=issues" + order + "&limit=50&page=" + strconv.Itoa(page)
	})
	if hasStatus(err, http.StatusNotFound, "") {
		if repo, rerr := g.repository(ctx, g.headers); rerr == nil &&
			(repo.HasIssues != nil && !*repo.HasIssues || present(repo.ExternalTracker)) {
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

// SetPRMetadata adds the labels by id (names resolved like CreateIssue's).
func (g *giteaClient) SetPRMetadata(ctx context.Context, n int, m PRMetadata) error {
	var labels any
	var errs []error
	if len(m.Labels) > 0 {
		ids, err := g.labelIDs(ctx, m.Labels)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("labels of #%d: %w", n, err))
		case len(ids) > 0:
			labels = ids
		}
	}
	return errors.Join(append(errs, setPRMetadata(ctx, &g.rest, g.headers, g.repo, n, labels, m))...)
}

// RemovePRLabel deletes the label by id (the name resolved like CreateIssue's); a name the
// repository and its organization do not know, or a 404, means the PR does not have it.
func (g *giteaClient) RemovePRLabel(ctx context.Context, n int, label string) error {
	ids, err := g.labelIDs(ctx, []string{label})
	if err != nil {
		return fmt.Errorf("labels of #%d: %w", n, err)
	}
	for _, id := range ids {
		err := g.do(ctx, http.MethodDelete, issuePath(g.repo, n)+"/labels/"+strconv.FormatInt(id, 10), nil, nil, g.headers)
		if err != nil && !hasStatus(err, http.StatusNotFound, "") {
			return err
		}
	}
	return nil
}

// labelIDs resolves label names (case-insensitive) to ids: the repository's labels, then its
// organization's, each listed at most once per client and only while names are missing.
func (g *giteaClient) labelIDs(ctx context.Context, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}
	g.labelsMu.Lock()
	defer g.labelsMu.Unlock()
	if g.labels == nil {
		g.labels = map[string]int64{}
	}
	byName := g.labels
	owner, _, _ := strings.Cut(g.repo, "/")
	bases := []string{"/repos/" + g.repo + "/labels", "/orgs/" + owner + "/labels"}
	for ; g.labelSources < len(bases) && !allKnown(byName, names); g.labelSources++ {
		base := bases[g.labelSources]
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
	}
	var ids []int64
	for _, n := range names {
		id, ok := byName[strings.ToLower(n)]
		if !ok {
			slog.WarnContext(ctx, "label not found, leaving it out", "label", n, "repo", g.repo)
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
