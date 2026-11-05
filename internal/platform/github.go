package platform

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NewClient returns the pull request client for info.Kind (gitea and forgejo share one).
func NewClient(info Info, token string, c *http.Client) Client {
	if info.Kind == GitHub {
		return newGitHub(info, token, c)
	}
	return &giteaClient{
		api: info.APIURL, repo: info.Repo, c: c, headers: giteaHeaders(token), toPR: giteaPR,
		forgejo: info.Kind == Forgejo,
	}
}

// rest is the shared JSON-over-HTTP core of both clients; headers and toPR are the forge's.
type rest struct {
	api, repo string
	c         *http.Client
	headers   func(http.Header)
	toPR      func(*apiPR) PR

	mu   sync.Mutex
	info *apiRepo // GET /repos/{repo}, cached after the first success
}

// statusError is a non-2xx API response.
type statusError struct {
	method, path, status string
	code                 int
	body                 []byte // first 200 bytes
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s %s: %s: %s", e.method, e.path, e.status, e.body)
}

// hasStatus reports whether err is an API response with code whose body contains substr
// (case-insensitive).
func hasStatus(err error, code int, substr string) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == code && strings.Contains(strings.ToLower(string(se.body)), substr)
}

func (r *rest) do(ctx context.Context, method, path string, in, out any, setHeaders func(http.Header)) error {
	return r.doURL(ctx, method, r.api+path, path, in, out, setHeaders)
}

// doURL sends a JSON request to url; errors name the request as "<method> <label>".
func (r *rest) doURL(ctx context.Context, method, url, label string, in, out any, setHeaders func(http.Header)) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setHeaders(req.Header)
	resp, err := r.c.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, label, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return &statusError{method: method, path: label, status: resp.Status, code: resp.StatusCode, body: b}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		return fmt.Errorf("%s %s: decode: %w", method, label, err)
	}
	return nil
}

// apiRepo covers the repository JSON of GitHub and Gitea/Forgejo.
type apiRepo struct {
	DefaultBranch string `json:"default_branch"`
	NodeID        string `json:"node_id"` // GitHub GraphQL id
	// GitHub; only reported to tokens with push access, so nil means unknown.
	AllowAutoMerge   *bool `json:"allow_auto_merge"`
	AllowMergeCommit *bool `json:"allow_merge_commit"`
	AllowSquashMerge *bool `json:"allow_squash_merge"`
	AllowRebaseMerge *bool `json:"allow_rebase_merge"`
	// Gitea and Forgejo.
	DefaultMergeStyle string `json:"default_merge_style"`
	// Both: has_issues false disables the tracker; Gitea/Forgejo also report an external
	// tracker (external_tracker non-null), which leaves the issue API at 404.
	HasIssues       *bool          `json:"has_issues"`
	ExternalTracker jsontext.Value `json:"external_tracker"`
}

// repository returns the repository JSON, fetched once per client.
func (r *rest) repository(ctx context.Context, h func(http.Header)) (*apiRepo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.info != nil {
		return r.info, nil
	}
	var v apiRepo
	if err := r.do(ctx, http.MethodGet, "/repos/"+r.repo, nil, &v, h); err != nil {
		return nil, err
	}
	r.info = &v
	return r.info, nil
}

// apiPR covers the PR JSON of GitHub and Gitea/Forgejo.
type apiPR struct {
	Number    int            `json:"number"`
	NodeID    string         `json:"node_id"`
	Title     string         `json:"title"`
	Body      string         `json:"body"`
	State     string         `json:"state"`
	HTMLURL   string         `json:"html_url"`
	Merged    bool           `json:"merged"`
	MergedAt  *string        `json:"merged_at"`
	Mergeable *bool          `json:"mergeable"`
	AutoMerge jsontext.Value `json:"auto_merge"`
	CreatedAt time.Time      `json:"created_at"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// present reports whether a raw JSON value is set and not null.
func present(v jsontext.Value) bool { return len(v) > 0 && string(v) != "null" }

func (p *apiPR) pr() PR {
	pr := PR{Number: p.Number, Title: p.Title, Body: p.Body, Head: p.Head.Ref, HeadSHA: p.Head.SHA, Base: p.Base.Ref, State: p.State, URL: p.HTMLURL, CreatedAt: p.CreatedAt}
	for _, l := range p.Labels {
		pr.Labels = append(pr.Labels, l.Name)
	}
	return pr
}

type prEdit struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	State string `json:"state,omitempty"`
}

type prCreate struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

// apiIssue covers the issue JSON of GitHub and Gitea/Forgejo.
type apiIssue struct {
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	HTMLURL     string         `json:"html_url"`
	PullRequest jsontext.Value `json:"pull_request"` // non-null for PRs in issue lists
}

func (i *apiIssue) issue() Issue {
	return Issue{Number: i.Number, Title: i.Title, Body: i.Body, URL: i.HTMLURL}
}

// isPR reports whether an issue list entry is a pull request.
func (i *apiIssue) isPR() bool { return present(i.PullRequest) }

func (r *rest) DefaultBranch(ctx context.Context) (string, error) {
	return defaultBranch(ctx, r, r.headers)
}

func (r *rest) GetPR(ctx context.Context, n int) (PR, error) {
	var p apiPR
	if err := r.do(ctx, http.MethodGet, prPath(r.repo, n), nil, &p, r.headers); err != nil {
		return PR{}, err
	}
	return r.toPR(&p), nil
}

func (r *rest) CreatePR(ctx context.Context, head, base, title, body string) (PR, error) {
	var p apiPR
	in := prCreate{Title: title, Head: head, Base: base, Body: body}
	if err := r.do(ctx, http.MethodPost, "/repos/"+r.repo+"/pulls", in, &p, r.headers); err != nil {
		return PR{}, err
	}
	return r.toPR(&p), nil
}

func (r *rest) UpdatePR(ctx context.Context, n int, title, body string) error {
	return r.do(ctx, http.MethodPatch, prPath(r.repo, n), prEdit{Title: title, Body: body}, nil, r.headers)
}

func (r *rest) ClosePR(ctx context.Context, n int) error {
	return r.do(ctx, http.MethodPatch, prPath(r.repo, n), prEdit{State: "closed"}, nil, r.headers)
}

func (r *rest) UpdateIssue(ctx context.Context, n int, body string) error {
	return r.do(ctx, http.MethodPatch, issuePath(r.repo, n), map[string]string{"body": body}, nil, r.headers)
}

type githubClient struct{ rest }

func newGitHub(info Info, token string, c *http.Client) *githubClient {
	return &githubClient{api: info.APIURL, repo: info.Repo, c: c, headers: githubHeaders(token), toPR: githubPR}
}

func githubHeaders(token string) func(http.Header) {
	return func(h http.Header) {
		if token != "" {
			h.Set("Authorization", "Bearer "+token)
		}
		h.Set("Accept", "application/vnd.github+json")
		h.Set("X-GitHub-Api-Version", "2022-11-28")
	}
}

func githubPR(p *apiPR) PR {
	pr := p.pr()
	pr.Merged = p.MergedAt != nil
	pr.Mergeable = p.Mergeable
	pr.NodeID = p.NodeID
	pr.AutoMerge = present(p.AutoMerge)
	return pr
}

func (g *githubClient) ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) {
	const per = 100
	return listPRs(ctx, &g.rest, g.headers, g.toPR, per, maxPages, func(page int) string {
		return "/repos/" + g.repo + "/pulls?state=" + state + "&sort=updated&direction=desc&per_page=100&page=" + strconv.Itoa(page)
	})
}

func (g *githubClient) SetPRMetadata(ctx context.Context, n int, m PRMetadata) error {
	var labels any
	if len(m.Labels) > 0 {
		labels = m.Labels
	}
	return setPRMetadata(ctx, &g.rest, g.headers, g.repo, n, labels, m)
}

// RemovePRLabel deletes the label by name; GitHub answers 404 when the PR does not have it.
func (g *githubClient) RemovePRLabel(ctx context.Context, n int, label string) error {
	err := g.do(ctx, http.MethodDelete, issuePath(g.repo, n)+"/labels/"+neturl.PathEscape(label), nil, nil, g.headers)
	if hasStatus(err, http.StatusNotFound, "") {
		return nil
	}
	return err
}

// setPRMetadata is SetPRMetadata over the REST paths GitHub, Gitea and Forgejo share; labels
// is the forge's form of m.Labels (names, or Gitea ids), nil for none.
func setPRMetadata(ctx context.Context, r *rest, h func(http.Header), repo string, n int, labels any, m PRMetadata) error {
	var errs []error
	if labels != nil {
		if err := r.do(ctx, http.MethodPost, issuePath(repo, n)+"/labels", map[string]any{"labels": labels}, nil, h); err != nil {
			errs = append(errs, fmt.Errorf("add labels to #%d: %w", n, err))
		}
	}
	if len(m.Reviewers) > 0 {
		users, teams := splitReviewers(m.Reviewers)
		in := struct {
			Reviewers []string `json:"reviewers,omitempty"`
			Teams     []string `json:"team_reviewers,omitempty"`
		}{users, teams}
		if err := r.do(ctx, http.MethodPost, prPath(repo, n)+"/requested_reviewers", in, nil, h); err != nil {
			errs = append(errs, fmt.Errorf("request reviewers of #%d: %w", n, err))
		}
	}
	if len(m.Assignees) > 0 {
		if err := r.do(ctx, http.MethodPatch, issuePath(repo, n), map[string][]string{"assignees": m.Assignees}, nil, h); err != nil {
			errs = append(errs, fmt.Errorf("assign #%d: %w", n, err))
		}
	}
	return errors.Join(errs...)
}

// githubUnavailableRe matches enablePullRequestAutoMerge errors after which GitHub will not
// auto-merge the PR: the repository disallows auto-merge, or the PR can be merged right away
// ("Pull request Pull request is in clean status"), so there is nothing to wait for.
var githubUnavailableRe = regexp.MustCompile(`(?i)auto.merge is not allowed|not allowed for this repository|is in (clean|unstable|has_hooks) status`)

const enableAutoMergeMutation = `mutation($id: ID!, $method: PullRequestMergeMethod) {
  enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $method}) { clientMutationId }
}`

// EnableAutoMerge runs the GraphQL enablePullRequestAutoMerge mutation. A repository known to
// disallow auto-merge (allow_auto_merge false) is not asked.
func (g *githubClient) EnableAutoMerge(ctx context.Context, pr PR, strategy string) error {
	if pr.NodeID == "" {
		return fmt.Errorf("enable auto-merge #%d: no node_id", pr.Number)
	}
	method, repo, err := g.method(ctx, strategy)
	if err != nil {
		return err
	}
	if repo.AllowAutoMerge != nil && !*repo.AllowAutoMerge {
		return fmt.Errorf("enable auto-merge #%d: %w: disabled in the repository settings", pr.Number, ErrAutoMergeUnavailable)
	}
	in := map[string]string{"id": pr.NodeID, "method": strings.ToUpper(method)}
	msg, err := g.graphql(ctx, "enablePullRequestAutoMerge", enableAutoMergeMutation, in, nil)
	switch {
	case err != nil:
		return err
	case msg == "":
		return nil
	case githubUnavailableRe.MatchString(msg):
		return fmt.Errorf("enable auto-merge #%d: %w: %s", pr.Number, ErrAutoMergeUnavailable, msg)
	}
	return fmt.Errorf("enable auto-merge #%d: %s", pr.Number, msg)
}

// graphql runs a GraphQL query or mutation (op names it in errors) and decodes its data into
// data (unless nil). GraphQL errors come back as msg, their messages joined by "; ", with a
// nil err; err reports transport and HTTP errors.
func (g *githubClient) graphql(ctx context.Context, op, query string, vars, data any) (msg string, err error) {
	var out struct {
		Data   jsontext.Value `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	in := map[string]any{"query": query, "variables": vars}
	if err := g.doURL(ctx, http.MethodPost, graphqlURL(g.api), "/graphql "+op, in, &out, g.headers); err != nil {
		return "", err
	}
	if len(out.Errors) > 0 {
		msgs := make([]string, len(out.Errors))
		for i, e := range out.Errors {
			msgs[i] = e.Message
		}
		return strings.Join(msgs, "; "), nil
	}
	if data != nil {
		if err := json.Unmarshal(out.Data, data); err != nil {
			return "", fmt.Errorf("POST /graphql %s: decode: %w", op, err)
		}
	}
	return "", nil
}

// method maps strategy to a GitHub merge method (merge, squash, rebase); auto picks the first
// method the repository allows, merge when that is unknown.
func (g *githubClient) method(ctx context.Context, strategy string) (string, *apiRepo, error) {
	repo, err := g.repository(ctx, g.headers)
	if err != nil {
		return "", nil, err
	}
	switch strategy {
	case "merge", "squash", "rebase":
		return strategy, repo, nil
	case "", "auto":
		for _, m := range []struct {
			name    string
			allowed *bool
		}{{"merge", repo.AllowMergeCommit}, {"squash", repo.AllowSquashMerge}, {"rebase", repo.AllowRebaseMerge}} {
			if m.allowed != nil && *m.allowed {
				return m.name, repo, nil
			}
		}
		return "merge", repo, nil
	}
	return "", nil, fmt.Errorf("unknown merge strategy %q", strategy)
}

// graphqlURL derives the GraphQL endpoint from the REST API URL: https://api.github.com ->
// https://api.github.com/graphql, GHES https://host/api/v3 -> https://host/api/graphql.
func graphqlURL(api string) string {
	api = strings.TrimSuffix(api, "/")
	if base, ok := strings.CutSuffix(api, "/v3"); ok {
		return base + "/graphql"
	}
	return api + "/graphql"
}

// Checks combines the commit statuses and the check runs of sha: a failed one wins over an
// unfinished one. The combined status of a commit without statuses is "pending", so statuses
// only count when there are some.
func (g *githubClient) Checks(ctx context.Context, sha string) (CheckState, error) {
	var st struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
	}
	if err := g.do(ctx, http.MethodGet, "/repos/"+g.repo+"/commits/"+sha+"/status", nil, &st, g.headers); err != nil {
		return "", err
	}
	pending := false
	if st.TotalCount > 0 {
		switch st.State {
		case "success":
		case "pending":
			pending = true
		default: // failure, error
			return ChecksFailure, nil
		}
	}
	runs := 0
	for page := 1; page <= 10; page++ {
		var cr struct {
			TotalCount int `json:"total_count"`
			CheckRuns  []struct {
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}
		path := "/repos/" + g.repo + "/commits/" + sha + "/check-runs?per_page=100&page=" + strconv.Itoa(page)
		if err := g.do(ctx, http.MethodGet, path, nil, &cr, g.headers); err != nil {
			return "", err
		}
		for _, r := range cr.CheckRuns {
			switch {
			case r.Status != "completed":
				pending = true
			case r.Conclusion != "success" && r.Conclusion != "neutral" && r.Conclusion != "skipped":
				return ChecksFailure, nil
			}
		}
		runs += len(cr.CheckRuns)
		if len(cr.CheckRuns) < 100 || runs >= cr.TotalCount {
			break
		}
	}
	switch {
	case pending:
		return ChecksPending, nil
	case st.TotalCount+runs == 0:
		return ChecksNone, nil
	}
	return ChecksSuccess, nil
}

// MergePR merges with PUT /pulls/{n}/merge; sha makes GitHub refuse a head that moved.
func (g *githubClient) MergePR(ctx context.Context, pr PR, strategy string) error {
	method, _, err := g.method(ctx, strategy)
	if err != nil {
		return err
	}
	in := struct {
		MergeMethod string `json:"merge_method"`
		SHA         string `json:"sha,omitempty"`
	}{method, pr.HeadSHA}
	return g.do(ctx, http.MethodPut, prPath(g.repo, pr.Number)+"/merge", in, nil, g.headers)
}

// ListIssues pages through open issues; GitHub lists PRs as issues, they are dropped. GitHub
// answers 410 Gone when issues are disabled.
func (g *githubClient) ListIssues(ctx context.Context, maxPages int) ([]Issue, error) {
	is, err := listIssues(ctx, &g.rest, g.headers, 100, maxPages, func(page int) string {
		return "/repos/" + g.repo + "/issues?state=open&per_page=100&page=" + strconv.Itoa(page)
	})
	if hasStatus(err, http.StatusGone, "") {
		return nil, fmt.Errorf("%w: %w", ErrIssuesDisabled, err)
	}
	return is, err
}

// CreateIssue creates an issue; GitHub takes label names (and creates missing labels).
func (g *githubClient) CreateIssue(ctx context.Context, title, body string, labels []string) (Issue, error) {
	in := struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels,omitempty"`
	}{title, body, labels}
	var out apiIssue
	if err := g.do(ctx, http.MethodPost, "/repos/"+g.repo+"/issues", in, &out, g.headers); err != nil {
		return Issue{}, err
	}
	return out.issue(), nil
}

func prPath(repo string, n int) string    { return "/repos/" + repo + "/pulls/" + strconv.Itoa(n) }
func issuePath(repo string, n int) string { return "/repos/" + repo + "/issues/" + strconv.Itoa(n) }

func defaultBranch(ctx context.Context, r *rest, h func(http.Header)) (string, error) {
	v, err := r.repository(ctx, h)
	if err != nil {
		return "", err
	}
	if v.DefaultBranch == "" {
		return "", fmt.Errorf("GET /repos/%s: empty default_branch", r.repo)
	}
	return v.DefaultBranch, nil
}

func listPRs(ctx context.Context, r *rest, h func(http.Header), conv func(*apiPR) PR, per, maxPages int, path func(int) string) ([]PR, error) {
	var out []PR
	for page := 1; page <= maxPages; page++ {
		var ps []apiPR
		if err := r.do(ctx, http.MethodGet, path(page), nil, &ps, h); err != nil {
			return nil, err
		}
		for i := range ps {
			out = append(out, conv(&ps[i]))
		}
		if len(ps) < per {
			break
		}
	}
	return out, nil
}

// listIssues pages through an issue list; a short page (PRs included) ends it.
func listIssues(ctx context.Context, r *rest, h func(http.Header), per, maxPages int, path func(int) string) ([]Issue, error) {
	var out []Issue
	for page := 1; page <= maxPages; page++ {
		var is []apiIssue
		if err := r.do(ctx, http.MethodGet, path(page), nil, &is, h); err != nil {
			return nil, err
		}
		for i := range is {
			if !is[i].isPR() {
				out = append(out, is[i].issue())
			}
		}
		if len(is) < per {
			break
		}
	}
	return out, nil
}
