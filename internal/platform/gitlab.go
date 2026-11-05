package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gitlabClient serves GitLab's REST API v4: merge requests are the pull requests (PR.Number
// is the MR's project-scoped iid), issues the dashboard, pipelines the checks.
type gitlabClient struct {
	r       rest   // the JSON core; its /repos/ helpers are not used
	project string // "/projects/<url-encoded path>"

	mu      sync.Mutex
	info    *gitlabProject   // GET /projects/:id, cached after the first success
	userIDs map[string]int64 // username -> id of the users looked up so far
}

func newGitLab(info Info, token string, c *http.Client) *gitlabClient {
	return &gitlabClient{
		r:       rest{api: info.APIURL, repo: info.Repo, c: c, headers: gitlabHeaders(token)},
		project: "/projects/" + url.PathEscape(info.Repo),
	}
}

// gitlabHeaders sends the token as OAuth-compliant bearer token, which GitLab accepts for
// personal, project and group access tokens and OAuth tokens alike.
func gitlabHeaders(token string) func(http.Header) {
	return func(h http.Header) {
		if token != "" {
			h.Set("Authorization", "Bearer "+token)
		}
		h.Set("Accept", "application/json")
	}
}

func (g *gitlabClient) do(ctx context.Context, method, path string, in, out any) error {
	return g.r.do(ctx, method, path, in, out, g.r.headers)
}

// gitlabProject is the subset of GET /projects/:id deps uses.
type gitlabProject struct {
	DefaultBranch string `json:"default_branch"`
	IssuesEnabled *bool  `json:"issues_enabled"` // nil when the token cannot see it
}

func (g *gitlabClient) projectInfo(ctx context.Context) (*gitlabProject, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.info != nil {
		return g.info, nil
	}
	var p gitlabProject
	if err := g.do(ctx, http.MethodGet, g.project, nil, &p); err != nil {
		return nil, err
	}
	g.info = &p
	return g.info, nil
}

func (g *gitlabClient) DefaultBranch(ctx context.Context) (string, error) {
	p, err := g.projectInfo(ctx)
	if err != nil {
		return "", err
	}
	if p.DefaultBranch == "" {
		return "", fmt.Errorf("GET %s: empty default_branch", g.project)
	}
	return p.DefaultBranch, nil
}

// gitlabMR is the subset of the merge request JSON deps uses.
type gitlabMR struct {
	IID          int       `json:"iid"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	State        string    `json:"state"` // opened, closed, merged, locked
	WebURL       string    `json:"web_url"`
	CreatedAt    time.Time `json:"created_at"`
	SourceBranch string    `json:"source_branch"`
	SHA          string    `json:"sha"`
	Labels       []string  `json:"labels"`
	HasConflicts bool      `json:"has_conflicts"`
	// DetailedMergeStatus is mergeable, conflict, need_rebase, or why the MR waits (checking,
	// ci_must_pass, not_approved, …).
	DetailedMergeStatus string `json:"detailed_merge_status"`
}

// pr converts m. AutoMerge stays false although GitLab reports merge_when_pipeline_succeeds:
// a push to the source branch cancels auto-merge (GitLab 19.5), and the bot keeps the flag
// of the PR it listed when it pushes, so it asks again on every run; the request is
// idempotent. Mergeable is false for conflicts and when a fast-forward project needs a
// rebase (deps rebuilds the branch), true when GitLab reports it mergeable, and nil while
// GitLab checks it or the MR waits for something else (pipeline, approvals, …).
func (m *gitlabMR) pr() PR {
	pr := PR{
		Number: m.IID, Title: m.Title, Body: m.Description, Head: m.SourceBranch, HeadSHA: m.SHA,
		URL: m.WebURL, CreatedAt: m.CreatedAt, Merged: m.State == "merged",
		State: "open",
	}
	if m.State == "closed" || m.State == "merged" {
		pr.State = "closed"
	}
	if len(m.Labels) > 0 {
		pr.Labels = m.Labels
	}
	switch {
	case m.HasConflicts || m.DetailedMergeStatus == "conflict" || m.DetailedMergeStatus == "need_rebase":
		f := false
		pr.Mergeable = &f
	case m.DetailedMergeStatus == "mergeable":
		t := true
		pr.Mergeable = &t
	}
	return pr
}

func (g *gitlabClient) mrPath(n int) string {
	return g.project + "/merge_requests/" + strconv.Itoa(n)
}

// ListPRs pages through the merge requests, most recently updated first. "closed" lists
// closed and merged ones (state=all without the open ones), as GitHub's closed PRs include
// the merged.
func (g *gitlabClient) ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) {
	const per = 100
	q := "opened"
	if state != "open" {
		q = "all"
	}
	var out []PR
	for page := 1; page <= maxPages; page++ {
		var ms []gitlabMR
		path := g.project + "/merge_requests?state=" + q + "&order_by=updated_at&sort=desc&per_page=100&page=" + strconv.Itoa(page)
		if err := g.do(ctx, http.MethodGet, path, nil, &ms); err != nil {
			return nil, err
		}
		for i := range ms {
			if p := ms[i].pr(); p.State == state {
				out = append(out, p)
			}
		}
		if len(ms) < per {
			break
		}
	}
	return out, nil
}

func (g *gitlabClient) GetPR(ctx context.Context, n int) (PR, error) {
	var m gitlabMR
	if err := g.do(ctx, http.MethodGet, g.mrPath(n), nil, &m); err != nil {
		return PR{}, err
	}
	return m.pr(), nil
}

// CreatePR opens a merge request that deletes its source branch when merged.
func (g *gitlabClient) CreatePR(ctx context.Context, head, base, title, body string) (PR, error) {
	in := map[string]any{
		"source_branch": head, "target_branch": base, "title": title, "description": body,
		"remove_source_branch": true,
	}
	var m gitlabMR
	if err := g.do(ctx, http.MethodPost, g.project+"/merge_requests", in, &m); err != nil {
		return PR{}, err
	}
	return m.pr(), nil
}

func (g *gitlabClient) UpdatePR(ctx context.Context, n int, title, body string) error {
	in := map[string]string{}
	if title != "" {
		in["title"] = title
	}
	if body != "" {
		in["description"] = body
	}
	return g.do(ctx, http.MethodPut, g.mrPath(n), in, nil)
}

func (g *gitlabClient) ClosePR(ctx context.Context, n int) error {
	return g.do(ctx, http.MethodPut, g.mrPath(n), map[string]string{"state_event": "close"}, nil)
}

// SetPRMetadata adds the labels (add_labels; GitLab creates missing project labels), and sets
// reviewers and assignees by user id (usernames looked up with GET /users?username=). GitLab
// has no team reviewers: team:<slug> entries fail that part, the users are still requested.
func (g *gitlabClient) SetPRMetadata(ctx context.Context, n int, m PRMetadata) error {
	var errs []error
	if len(m.Labels) > 0 {
		if err := g.do(ctx, http.MethodPut, g.mrPath(n), map[string]string{"add_labels": strings.Join(m.Labels, ",")}, nil); err != nil {
			errs = append(errs, fmt.Errorf("add labels to !%d: %w", n, err))
		}
	}
	if len(m.Reviewers) > 0 {
		users, teams := splitReviewers(m.Reviewers)
		if len(teams) > 0 {
			errs = append(errs, fmt.Errorf("request reviewers of !%d: GitLab has no team reviewers: %s", n, strings.Join(teams, ", ")))
		}
		if err := g.setUsers(ctx, n, "reviewer_ids", users); err != nil {
			errs = append(errs, fmt.Errorf("request reviewers of !%d: %w", n, err))
		}
	}
	if len(m.Assignees) > 0 {
		if err := g.setUsers(ctx, n, "assignee_ids", m.Assignees); err != nil {
			errs = append(errs, fmt.Errorf("assign !%d: %w", n, err))
		}
	}
	return errors.Join(errs...)
}

// setUsers sets field (reviewer_ids, assignee_ids) of MR n to the ids of names. Unknown
// names are an error; the known ones are still set.
func (g *gitlabClient) setUsers(ctx context.Context, n int, field string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	ids, err := g.userIDsOf(ctx, names)
	if len(ids) > 0 {
		if perr := g.do(ctx, http.MethodPut, g.mrPath(n), map[string][]int64{field: ids}, nil); perr != nil {
			return errors.Join(err, perr)
		}
	}
	return err
}

// userIDsOf resolves usernames to user ids, each looked up at most once per client.
func (g *gitlabClient) userIDsOf(ctx context.Context, names []string) ([]int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.userIDs == nil {
		g.userIDs = map[string]int64{}
	}
	var ids []int64
	var errs []error
	for _, name := range names {
		name = strings.TrimPrefix(name, "@")
		id, ok := g.userIDs[strings.ToLower(name)]
		if !ok {
			var us []struct {
				ID int64 `json:"id"`
			}
			if err := g.do(ctx, http.MethodGet, "/users?username="+url.QueryEscape(name), nil, &us); err != nil {
				errs = append(errs, err)
				continue
			}
			if len(us) > 0 {
				id = us[0].ID
			}
			g.userIDs[strings.ToLower(name)] = id
		}
		if id == 0 {
			errs = append(errs, fmt.Errorf("unknown user %q", name))
			continue
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, errors.Join(errs...)
}

// RemovePRLabel removes the MR's labels whose name equals label case-insensitively (GitLab's
// remove_labels matches exactly); none matching is no error.
func (g *gitlabClient) RemovePRLabel(ctx context.Context, n int, label string) error {
	pr, err := g.GetPR(ctx, n)
	if err != nil {
		return err
	}
	var names []string
	for _, l := range pr.Labels {
		if strings.EqualFold(l, label) {
			names = append(names, l)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return g.do(ctx, http.MethodPut, g.mrPath(n), map[string]string{"remove_labels": strings.Join(names, ",")}, nil)
}

// EnableAutoMerge sets auto-merge with PUT /merge_requests/:iid/merge and
// merge_when_pipeline_succeeds (auto_merge since GitLab 17.11, both are sent): GitLab merges
// when the head pipeline succeeds, or right away when there is none to wait for. 405, 406, 409
// and 422 (draft, conflicts, still checking, head moved, pipeline required but missing, not
// approved, …) are ErrAutoMergePending: the next run asks again.
func (g *gitlabClient) EnableAutoMerge(ctx context.Context, pr PR, strategy string) error {
	err := g.merge(ctx, pr, strategy, true)
	if mergeRefused(err) {
		return fmt.Errorf("enable auto-merge !%d: %w: %w", pr.Number, ErrAutoMergePending, err)
	}
	return err
}

// MergePR merges with PUT /merge_requests/:iid/merge; sha makes GitLab refuse a head that moved.
func (g *gitlabClient) MergePR(ctx context.Context, pr PR, strategy string) error {
	return g.merge(ctx, pr, strategy, false)
}

func mergeRefused(err error) bool {
	for _, code := range []int{http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusConflict, http.StatusUnprocessableEntity} {
		if hasStatus(err, code, "") {
			return true
		}
	}
	return false
}

// merge maps strategy onto GitLab's squash flag: the merge method (merge commit, semi-linear,
// fast-forward) is a project setting. squash squashes; merge and rebase do not; auto keeps
// the project's squash default.
func (g *gitlabClient) merge(ctx context.Context, pr PR, strategy string, whenPipelineSucceeds bool) error {
	in := map[string]any{"should_remove_source_branch": true}
	switch strategy {
	case "squash":
		in["squash"] = true
	case "merge", "rebase":
		in["squash"] = false
	case "", "auto":
	default:
		return fmt.Errorf("unknown merge strategy %q", strategy)
	}
	if whenPipelineSucceeds {
		in["merge_when_pipeline_succeeds"] = true
		in["auto_merge"] = true
	}
	if pr.HeadSHA != "" {
		in["sha"] = pr.HeadSHA
	}
	return g.do(ctx, http.MethodPut, g.mrPath(pr.Number)+"/merge", in, nil)
}

// Checks combines the latest pipeline of each ref that ran for sha (branch and merge request
// pipelines; GitLab files external commit statuses under pipelines too). Failed and canceled
// pipelines fail; skipped ones pass; any other unfinished state (created, pending, running,
// manual, scheduled, …) is pending.
func (g *gitlabClient) Checks(ctx context.Context, sha string) (CheckState, error) {
	var ps []struct {
		ID     int64  `json:"id"`
		Ref    string `json:"ref"`
		Status string `json:"status"`
	}
	path := g.project + "/pipelines?sha=" + url.QueryEscape(sha) + "&order_by=id&sort=desc&per_page=100"
	if err := g.do(ctx, http.MethodGet, path, nil, &ps); err != nil {
		return "", err
	}
	if len(ps) == 0 {
		return ChecksNone, nil
	}
	seen := map[string]bool{}
	pending := false
	for _, p := range ps { // newest first: the first of each ref is its latest
		if seen[p.Ref] {
			continue
		}
		seen[p.Ref] = true
		switch p.Status {
		case "success", "skipped":
		case "failed", "canceled":
			return ChecksFailure, nil
		default:
			pending = true
		}
	}
	if pending {
		return ChecksPending, nil
	}
	return ChecksSuccess, nil
}

// gitlabIssue is the subset of the issue JSON deps uses.
type gitlabIssue struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	WebURL      string `json:"web_url"`
}

func (i *gitlabIssue) issue() Issue {
	return Issue{Number: i.IID, Title: i.Title, Body: i.Description, URL: i.WebURL}
}

// ListIssues pages through the open issues. A project with issues disabled (issues_enabled
// false in the project, cached from DefaultBranch) is ErrIssuesDisabled: GitLab lists no
// issues for it and refuses to create one with 403.
func (g *gitlabClient) ListIssues(ctx context.Context, maxPages int) ([]Issue, error) {
	p, err := g.projectInfo(ctx)
	if err != nil {
		return nil, err
	}
	if p.IssuesEnabled != nil && !*p.IssuesEnabled {
		return nil, fmt.Errorf("%w: issues_enabled is false in %s", ErrIssuesDisabled, g.r.repo)
	}
	const per = 100
	var out []Issue
	for page := 1; page <= maxPages; page++ {
		var is []gitlabIssue
		if err := g.do(ctx, http.MethodGet, g.project+"/issues?state=opened&per_page=100&page="+strconv.Itoa(page), nil, &is); err != nil {
			return nil, err
		}
		for i := range is {
			out = append(out, is[i].issue())
		}
		if len(is) < per {
			break
		}
	}
	return out, nil
}

// CreateIssue creates an issue; GitLab takes label names and creates missing project labels.
func (g *gitlabClient) CreateIssue(ctx context.Context, title, body string, labels []string) (Issue, error) {
	in := map[string]string{"title": title, "description": body}
	if len(labels) > 0 {
		in["labels"] = strings.Join(labels, ",")
	}
	var out gitlabIssue
	if err := g.do(ctx, http.MethodPost, g.project+"/issues", in, &out); err != nil {
		return Issue{}, err
	}
	return out.issue(), nil
}

func (g *gitlabClient) UpdateIssue(ctx context.Context, n int, body string) error {
	return g.do(ctx, http.MethodPut, g.project+"/issues/"+strconv.Itoa(n), map[string]string{"description": body}, nil)
}
