package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	mu    sync.Mutex
	info  *gitlabProject         // GET /projects/:id, cached after the first success
	users map[string]*gitlabUser // lowercase username -> its lookup

	branchWaits []time.Duration // CreatePR's pauses while the pushed branch is unknown
}

// gitlabBranchWaits are the pauses before CreatePR asks again while GitLab answers 400
// {"source_branch":["does not exist"]}: GitLab learns of a push asynchronously (its
// PostReceive job refreshes the project's cached branch names), so a merge request created
// right after deps pushed its branch can be refused for a few seconds.
var gitlabBranchWaits = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}

// gitlabUser is the lookup of one username. mu is held across the request, so concurrent
// callers of the name share it while other names proceed; a failed lookup is retried.
type gitlabUser struct {
	mu   sync.Mutex
	id   int64 // 0: no such user
	done bool
}

func newGitLab(info Info, token string, c *http.Client) *gitlabClient {
	return &gitlabClient{
		r:           rest{api: info.APIURL, repo: info.Repo, c: c, headers: gitlabHeaders(token)},
		project:     "/projects/" + url.PathEscape(info.Repo),
		branchWaits: gitlabBranchWaits,
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
	UpdatedAt    time.Time `json:"updated_at"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	SHA          string    `json:"sha"`
	Labels       []string  `json:"labels"`
	Draft        bool      `json:"draft"`
	HasConflicts bool      `json:"has_conflicts"`
	// SourceProjectID and TargetProjectID name the projects of the source and the target
	// branch; they differ for a merge request opened from a fork.
	SourceProjectID int64 `json:"source_project_id"`
	TargetProjectID int64 `json:"target_project_id"`
	// MergeWhenPipelineSucceeds reports auto-merge as set (the MR JSON keeps this name after
	// GitLab 17.11 renamed the request parameter to auto_merge).
	MergeWhenPipelineSucceeds bool `json:"merge_when_pipeline_succeeds"`
	// DetailedMergeStatus is mergeable, conflict, need_rebase, or why the MR waits (checking,
	// ci_must_pass, not_approved, …).
	DetailedMergeStatus string `json:"detailed_merge_status"`
}

// fromFork reports whether the merge request's source branch lives in another project than
// its target one. A GitLab that reports neither project is taken at its word that the branch
// is the project's own, as every GitLab version deps supports reports both.
func (m *gitlabMR) fromFork() bool {
	if m.SourceProjectID == 0 || m.TargetProjectID == 0 {
		return false
	}
	return m.SourceProjectID != m.TargetProjectID
}

// pr converts m. AutoMerge is merge_when_pipeline_succeeds, which a push to the source branch
// cancels (PushCancelsAutoMerge). Mergeable is false for conflicts and when a fast-forward
// project needs a rebase (deps rebuilds the branch), true when GitLab reports it mergeable,
// and nil while GitLab checks it or the MR waits for something else (pipeline, approvals, …).
// GitLab derives the draft state from a prefix of the title, which is its mark and not part
// of the title deps wrote: it is cut off again and reported as Draft.
func (m *gitlabMR) pr() PR {
	title, draft := cutPrefixFold(m.Title, gitlabDraftPrefixes)
	pr := PR{
		Number: m.IID, Title: title, Body: m.Description, Head: m.SourceBranch, HeadSHA: m.SHA,
		Base: m.TargetBranch,
		URL:  m.WebURL, CreatedAt: m.CreatedAt, Merged: m.State == "merged", Draft: m.Draft || draft,
		State: "open", AutoMerge: m.MergeWhenPipelineSucceeds, PushCancelsAutoMerge: true,
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
// closed and merged ones, as GitHub's closed PRs include the merged; each GitLab state gets
// its own page budget so a busy project's open MRs cannot push them out of the window.
func (g *gitlabClient) ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) {
	if state == "open" {
		ms, err := g.listMRs(ctx, "opened", maxPages)
		return gitlabPRs(ms), err
	}
	closed, err := g.listMRs(ctx, "closed", maxPages)
	if err != nil {
		return nil, err
	}
	merged, err := g.listMRs(ctx, "merged", maxPages)
	if err != nil {
		return nil, err
	}
	ms := slices.Concat(closed, merged)
	slices.SortStableFunc(ms, func(a, b gitlabMR) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return gitlabPRs(ms), nil
}

// listMRs pages through the merge requests in GitLab state q, most recently updated first.
func (g *gitlabClient) listMRs(ctx context.Context, q string, maxPages int) ([]gitlabMR, error) {
	const per = 100
	var out []gitlabMR
	for page := 1; page <= maxPages; page++ {
		var ms []gitlabMR
		path := g.project + "/merge_requests?state=" + q + "&order_by=updated_at&sort=desc&per_page=100&page=" + strconv.Itoa(page)
		if err := g.do(ctx, http.MethodGet, path, nil, &ms); err != nil {
			return nil, err
		}
		out = append(out, ms...)
		if len(ms) < per {
			break
		}
	}
	return out, nil
}

// gitlabPRs converts the merge requests, leaving out the ones opened from a fork (ErrForkPR).
func gitlabPRs(ms []gitlabMR) []PR {
	if len(ms) == 0 {
		return nil
	}
	out := make([]PR, 0, len(ms))
	for i := range ms {
		if !ms[i].fromFork() {
			out = append(out, ms[i].pr())
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (g *gitlabClient) GetPR(ctx context.Context, n int) (PR, error) {
	var m gitlabMR
	if err := g.do(ctx, http.MethodGet, g.mrPath(n), nil, &m); err != nil {
		return PR{}, err
	}
	if m.fromFork() {
		return m.pr(), forkError(n, "project "+strconv.FormatInt(m.SourceProjectID, 10))
	}
	return m.pr(), nil
}

// gitlabDraftPrefix marks a draft merge request: GitLab derives the draft state from a title
// starting with one of gitlabDraftPrefixes (case-insensitive), and its REST API has no draft
// flag.
const gitlabDraftPrefix = "Draft: "

var gitlabDraftPrefixes = []string{"DRAFT:", "[DRAFT]", "(DRAFT)", "DRAFT - "}

// CreatePR opens a merge request that deletes its source branch when merged; a draft gets
// the Draft: title prefix. While GitLab does not know the just-pushed source branch yet, it
// asks again after each of g.branchWaits.
func (g *gitlabClient) CreatePR(ctx context.Context, head, base, title, body string, draft bool) (PR, error) {
	in := map[string]any{
		"source_branch": head, "target_branch": base, "title": draftTitle(title, draft, gitlabDraftPrefix, gitlabDraftPrefixes),
		"description": body, "remove_source_branch": true,
	}
	var m gitlabMR
	err := g.do(ctx, http.MethodPost, g.project+"/merge_requests", in, &m)
	for _, wait := range g.branchWaits {
		if !hasStatus(err, http.StatusBadRequest, `"source_branch":["does not exist"]`) {
			break
		}
		slog.DebugContext(ctx, "source branch not known to GitLab yet, creating the merge request again", "branch", head, "wait", wait)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return PR{}, ctx.Err()
		case <-t.C:
		}
		err = g.do(ctx, http.MethodPost, g.project+"/merge_requests", in, &m)
	}
	if err != nil {
		return PR{}, err
	}
	return m.pr(), nil
}

// UpdatePR keeps the Draft: prefix of a draft merge request: a title without it marks the MR
// ready.
func (g *gitlabClient) UpdatePR(ctx context.Context, n int, title, body string, draft bool) error {
	in := map[string]string{}
	if title != "" {
		in["title"] = draftTitle(title, draft, gitlabDraftPrefix, gitlabDraftPrefixes)
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
// reviewers and assignees by user id (usernames looked up with GET /users?username=), all in
// one PUT. GitLab has no team reviewers: team:<slug> entries fail that part, the users are
// still requested; unknown users fail it too, the known ones are still set.
func (g *gitlabClient) SetPRMetadata(ctx context.Context, n int, m PRMetadata) error {
	in := map[string]any{}
	var parts []string // what the PUT sets, for its error
	var errs []error
	if len(m.Labels) > 0 {
		in["add_labels"] = strings.Join(m.Labels, ",")
		parts = append(parts, "labels")
	}
	if len(m.Reviewers) > 0 {
		users, teams := splitReviewers(m.Reviewers)
		if len(teams) > 0 {
			errs = append(errs, fmt.Errorf("request reviewers of !%d: GitLab has no team reviewers: %s", n, strings.Join(teams, ", ")))
		}
		ids, err := g.userIDsOf(ctx, users)
		if err != nil {
			errs = append(errs, fmt.Errorf("request reviewers of !%d: %w", n, err))
		}
		if len(ids) > 0 {
			in["reviewer_ids"] = ids
			parts = append(parts, "reviewers")
		}
	}
	if len(m.Assignees) > 0 {
		ids, err := g.userIDsOf(ctx, m.Assignees)
		if err != nil {
			errs = append(errs, fmt.Errorf("assign !%d: %w", n, err))
		}
		if len(ids) > 0 {
			in["assignee_ids"] = ids
			parts = append(parts, "assignees")
		}
	}
	if len(in) > 0 {
		if err := g.do(ctx, http.MethodPut, g.mrPath(n), in, nil); err != nil {
			errs = append(errs, fmt.Errorf("set %s of !%d: %w", strings.Join(parts, ", "), n, err))
		}
	}
	return errors.Join(errs...)
}

// userIDsOf resolves usernames to user ids, each looked up at most once per client. Unknown
// names are an error; the ids of the known ones are still returned.
func (g *gitlabClient) userIDsOf(ctx context.Context, names []string) ([]int64, error) {
	var ids []int64
	var errs []error
	for _, name := range names {
		name = strings.TrimPrefix(name, "@")
		id, err := g.userID(ctx, name)
		if err != nil {
			errs = append(errs, err)
			continue
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

// userID returns the id of the user name, 0 when there is none.
func (g *gitlabClient) userID(ctx context.Context, name string) (int64, error) {
	key := strings.ToLower(name)
	g.mu.Lock()
	u := g.users[key]
	if u == nil {
		if g.users == nil {
			g.users = map[string]*gitlabUser{}
		}
		u = &gitlabUser{}
		g.users[key] = u
	}
	g.mu.Unlock()
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.done {
		var us []struct {
			ID int64 `json:"id"`
		}
		if err := g.do(ctx, http.MethodGet, "/users?username="+url.QueryEscape(name), nil, &us); err != nil {
			return 0, err
		}
		if len(us) > 0 {
			u.id = us[0].ID
		}
		u.done = true
	}
	return u.id, nil
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
// approved, …) are ErrAutoMergePending: the next run asks again. So is an answer with the MR
// neither merged nor set to auto-merge.
func (g *gitlabClient) EnableAutoMerge(ctx context.Context, pr PR, strategy string) error {
	m, err := g.merge(ctx, pr, strategy, true)
	switch {
	case mergeRefused(err):
		return fmt.Errorf("enable auto-merge !%d: %w: %w", pr.Number, ErrAutoMergePending, err)
	case err == nil && m.State != "merged" && !m.MergeWhenPipelineSucceeds:
		return fmt.Errorf("enable auto-merge !%d: %w: merge_when_pipeline_succeeds still false (state %s, %s)", pr.Number, ErrAutoMergePending, m.State, m.DetailedMergeStatus)
	}
	return err
}

// MergePR merges with PUT /merge_requests/:iid/merge; sha makes GitLab refuse a head that moved.
func (g *gitlabClient) MergePR(ctx context.Context, pr PR, strategy string) error {
	_, err := g.merge(ctx, pr, strategy, false)
	return err
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
func (g *gitlabClient) merge(ctx context.Context, pr PR, strategy string, whenPipelineSucceeds bool) (gitlabMR, error) {
	in := map[string]any{"should_remove_source_branch": true}
	switch strategy {
	case "squash":
		in["squash"] = true
	case "merge", "rebase":
		in["squash"] = false
	case "", "auto":
	default:
		return gitlabMR{}, fmt.Errorf("unknown merge strategy %q", strategy)
	}
	if whenPipelineSucceeds {
		in["merge_when_pipeline_succeeds"] = true
		in["auto_merge"] = true
	}
	if pr.HeadSHA != "" {
		in["sha"] = pr.HeadSHA
	}
	var m gitlabMR
	err := g.do(ctx, http.MethodPut, g.mrPath(pr.Number)+"/merge", in, &m)
	return m, err
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

// ListIssues pages through the open issues, narrowed to those holding f.Title in their title
// (GitLab searches its database, so an issue created moments ago is in the answer). A project
// with issues disabled (issues_enabled false in the project, cached from DefaultBranch) is
// ErrIssuesDisabled: GitLab lists no issues for it and refuses to create one with 403.
func (g *gitlabClient) ListIssues(ctx context.Context, maxPages int, f IssueFilter) (IssueList, error) {
	p, err := g.projectInfo(ctx)
	if err != nil {
		return IssueList{}, err
	}
	if p.IssuesEnabled != nil && !*p.IssuesEnabled {
		return IssueList{}, fmt.Errorf("%w: issues_enabled is false in %s", ErrIssuesDisabled, g.r.repo)
	}
	const per = 100
	query := ""
	if f.Oldest {
		query = "&order_by=created_at&sort=asc"
	}
	if f.Title != "" {
		query += "&in=title&search=" + url.QueryEscape(f.Title)
	}
	var out IssueList
	for page := 1; page <= maxPages; page++ {
		var is []gitlabIssue
		if err := g.do(ctx, http.MethodGet, g.project+"/issues?state=opened"+query+"&per_page=100&page="+strconv.Itoa(page), nil, &is); err != nil {
			return IssueList{}, err
		}
		found := false
		for i := range is {
			v := is[i].issue()
			out.Issues = append(out.Issues, v)
			found = found || (f.Stop != nil && f.Stop(v))
		}
		if found || len(is) < per {
			return out, nil
		}
	}
	out.Truncated = true
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
