package platform

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

type apiCall struct {
	method, uri, auth string
	body              map[string]any
}

// route answers a request with a status and a JSON body.
type route struct {
	code int
	body string
}

// routes serves responses by "<METHOD> <request uri>" and records every request.
func routes(t *testing.T, rs map[string]route) (*httptest.Server, func() []apiCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []apiCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := apiCall{method: r.Method, uri: r.URL.RequestURI(), auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &c.body); err != nil {
				t.Errorf("%s %s: body %s: %v", r.Method, c.uri, b, err)
			}
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		rt, ok := rs[r.Method+" "+c.uri]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(cmpOr(rt.code, http.StatusOK))
		io.WriteString(w, rt.body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []apiCall {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func uris(calls []apiCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.method + " " + c.uri
	}
	return out
}

func TestGitHubPRFields(t *testing.T) {
	srv, _ := routes(t, map[string]route{
		"GET /repos/o/r/pulls/1": {body: `{"number":1,"node_id":"PR_kw1","head":{"ref":"deps/a","sha":"abc"},"auto_merge":{"merge_method":"squash"}}`},
		"GET /repos/o/r/pulls/2": {body: `{"number":2,"node_id":"PR_kw2","head":{"ref":"deps/b","sha":"def"},"auto_merge":null}`},
	})
	c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
	p1, err := c.GetPR(t.Context(), 1)
	if err != nil || p1.NodeID != "PR_kw1" || p1.HeadSHA != "abc" || !p1.AutoMerge {
		t.Errorf("PR 1 %+v %v", p1, err)
	}
	p2, err := c.GetPR(t.Context(), 2)
	if err != nil || p2.NodeID != "PR_kw2" || p2.HeadSHA != "def" || p2.AutoMerge {
		t.Errorf("PR 2 %+v %v", p2, err)
	}
}

func TestGitHubEnableAutoMerge(t *testing.T) {
	pr := PR{Number: 5, NodeID: "PR_kw5", HeadSHA: "abc"}
	for _, tc := range []struct {
		name, repo, graphql, strategy string
		wantMethod                    string // "" = no GraphQL call
		wantErr                       error  // errors.Is target; nil = success
		wantErrText                   string
	}{
		{name: "explicit strategy", repo: `{"default_branch":"main"}`, graphql: `{"data":{"enablePullRequestAutoMerge":{"clientMutationId":null}}}`, strategy: "rebase", wantMethod: "REBASE"},
		{name: "auto picks the first allowed method", repo: `{"allow_merge_commit":false,"allow_squash_merge":true,"allow_rebase_merge":true}`, graphql: `{"data":{}}`, strategy: "auto", wantMethod: "SQUASH"},
		{name: "auto without settings merges", repo: `{}`, graphql: `{"data":{}}`, strategy: "auto", wantMethod: "MERGE"},
		{
			name: "clean status is unavailable", repo: `{}`, strategy: "merge", wantMethod: "MERGE", wantErr: ErrAutoMergeUnavailable,
			graphql: `{"data":{"enablePullRequestAutoMerge":null},"errors":[{"type":"UNPROCESSABLE","message":"Pull request Pull request is in clean status"}]}`,
		},
		{
			name: "disallowed in the repository is unavailable", repo: `{}`, strategy: "merge", wantMethod: "MERGE", wantErr: ErrAutoMergeUnavailable,
			graphql: `{"errors":[{"type":"UNPROCESSABLE","message":"Auto merge is not allowed for this repository"}]}`,
		},
		{name: "known disabled setting skips the mutation", repo: `{"allow_auto_merge":false}`, strategy: "merge", wantErr: ErrAutoMergeUnavailable},
		{
			name: "other GraphQL errors", repo: `{}`, strategy: "squash", wantMethod: "SQUASH",
			graphql:     `{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`,
			wantErrText: "enable auto-merge #5: Resource not accessible by integration",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := routes(t, map[string]route{
				"GET /repos/o/r": {body: tc.repo},
				"POST /graphql":  {body: tc.graphql},
			})
			c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
			err := c.EnableAutoMerge(t.Context(), pr, tc.strategy)
			switch {
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			case tc.wantErrText != "" && (err == nil || err.Error() != tc.wantErrText):
				t.Fatalf("err = %v, want %q", err, tc.wantErrText)
			case tc.wantErr == nil && tc.wantErrText == "" && err != nil:
				t.Fatal(err)
			}
			cs := calls()
			want := []string{"GET /repos/o/r"}
			if tc.wantMethod != "" {
				want = append(want, "POST /graphql")
			}
			if !slices.Equal(uris(cs), want) {
				t.Fatalf("calls %v, want %v", uris(cs), want)
			}
			for _, c := range cs {
				if c.auth != "Bearer tok" {
					t.Errorf("%s auth %q", c.uri, c.auth)
				}
			}
			if tc.wantMethod == "" {
				return
			}
			q, _ := cs[1].body["query"].(string)
			vars, _ := cs[1].body["variables"].(map[string]any)
			if !strings.Contains(q, "enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $method})") || vars["id"] != "PR_kw5" || vars["method"] != tc.wantMethod {
				t.Errorf("graphql body %v", cs[1].body)
			}
		})
	}
	if _, calls := routes(t, nil); NewClient(Info{Kind: GitHub, APIURL: "http://x", Repo: "o/r"}, "", nil).EnableAutoMerge(t.Context(), PR{Number: 1}, "merge") == nil || len(calls()) != 0 {
		t.Error("PR without node_id accepted")
	}
}

func TestGraphQLURL(t *testing.T) {
	for api, want := range map[string]string{
		"https://api.github.com":      "https://api.github.com/graphql",
		"https://ghe.example/api/v3":  "https://ghe.example/api/graphql",
		"https://ghe.example/api/v3/": "https://ghe.example/api/graphql",
	} {
		if got := graphqlURL(api); got != want {
			t.Errorf("graphqlURL(%q) = %q, want %q", api, got, want)
		}
	}
}

func TestGitHubChecksAndMerge(t *testing.T) {
	const statusURI, runsURI = "GET /repos/o/r/commits/abc/status", "GET /repos/o/r/commits/abc/check-runs?per_page=100&page=1"
	run := func(status, conclusion string) string {
		return fmt.Sprintf(`{"status":%q,"conclusion":%q}`, status, conclusion)
	}
	for _, tc := range []struct {
		name, status, runs string
		want               CheckState
		wantCalls          int
	}{
		{"nothing reported", `{"state":"pending","total_count":0}`, `{"total_count":0,"check_runs":[]}`, ChecksNone, 2},
		{"statuses only", `{"state":"success","total_count":2}`, `{"total_count":0,"check_runs":[]}`, ChecksSuccess, 2},
		{"pending status", `{"state":"pending","total_count":1}`, `{"total_count":0,"check_runs":[]}`, ChecksPending, 2},
		{"pending status, failed check", `{"state":"pending","total_count":1}`, `{"total_count":1,"check_runs":[` + run("completed", "failure") + `]}`, ChecksFailure, 2},
		{"failed status", `{"state":"failure","total_count":1}`, ``, ChecksFailure, 1},
		{"errored status", `{"state":"error","total_count":1}`, ``, ChecksFailure, 1},
		{"check runs only", `{"state":"pending","total_count":0}`, `{"total_count":3,"check_runs":[` + run("completed", "success") + "," + run("completed", "skipped") + "," + run("completed", "neutral") + `]}`, ChecksSuccess, 2},
		{"running check", `{"state":"success","total_count":1}`, `{"total_count":2,"check_runs":[` + run("completed", "success") + "," + run("in_progress", "") + `]}`, ChecksPending, 2},
		{"running then failed check", `{"state":"success","total_count":1}`, `{"total_count":2,"check_runs":[` + run("queued", "") + "," + run("completed", "timed_out") + `]}`, ChecksFailure, 2},
		{"failed check", `{"state":"success","total_count":1}`, `{"total_count":1,"check_runs":[` + run("completed", "failure") + `]}`, ChecksFailure, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := routes(t, map[string]route{statusURI: {body: tc.status}, runsURI: {body: tc.runs}})
			c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
			got, err := c.Checks(t.Context(), "abc")
			if err != nil || got != tc.want || len(calls()) != tc.wantCalls {
				t.Errorf("Checks = %v %v, calls %v; want %v", got, err, uris(calls()), tc.want)
			}
		})
	}

	srv, calls := routes(t, map[string]route{
		"GET /repos/o/r":               {body: `{"allow_merge_commit":false,"allow_squash_merge":true}`},
		"PUT /repos/o/r/pulls/5/merge": {body: `{"merged":true}`},
	})
	c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
	if err := c.MergePR(t.Context(), PR{Number: 5, HeadSHA: "abc"}, "auto"); err != nil {
		t.Fatal(err)
	}
	cs := calls()
	if b := cs[len(cs)-1].body; len(cs) != 2 || b["merge_method"] != "squash" || b["sha"] != "abc" {
		t.Errorf("merge calls %v body %v", uris(cs), b)
	}
}

func TestGitHubIssues(t *testing.T) {
	var page1 []string
	for i := range 100 {
		if i%2 == 0 {
			page1 = append(page1, fmt.Sprintf(`{"number":%d,"title":"pr","pull_request":{"url":"u"}}`, i))
		} else {
			page1 = append(page1, fmt.Sprintf(`{"number":%d,"title":"t%d","body":"b","html_url":"u%d","pull_request":null}`, i, i, i))
		}
	}
	srv, calls := routes(t, map[string]route{
		"GET /repos/o/r/issues?state=open&per_page=100&page=1":                            {body: "[" + strings.Join(page1, ",") + "]"},
		"GET /repos/o/r/issues?state=open&per_page=100&page=2":                            {body: `[{"number":200,"title":"Dependency Dashboard","body":"x"}]`},
		"GET /repos/o/r/issues?state=open&sort=created&direction=asc&per_page=100&page=1": {body: "[" + strings.Join(page1, ",") + "]"},
		"POST /repos/o/r/issues":      {code: http.StatusCreated, body: `{"number":201,"title":"Dependency Dashboard","html_url":"https://x/issues/201"}`},
		"PATCH /repos/o/r/issues/201": {body: `{}`},
	})
	c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
	is, err := c.ListIssues(t.Context(), 10, IssueFilter{})
	if err != nil || len(is.Issues) != 51 || is.Issues[0] != (Issue{Number: 1, Title: "t1", Body: "b", URL: "u1"}) || is.Issues[50].Number != 200 {
		t.Fatalf("ListIssues %d %v %v", len(is.Issues), err, is.Issues[:1])
	}
	if is.Truncated {
		t.Error("a listing that reached a short page is not truncated")
	}
	// Running out of pages on a full one leaves issues unread.
	if is, _ := c.ListIssues(t.Context(), 1, IssueFilter{}); len(is.Issues) != 50 || !is.Truncated {
		t.Errorf("maxPages: %d issues, truncated %v", len(is.Issues), is.Truncated)
	}
	// Oldest asks for ascending creation order, and Stop ends the paging on the page of the
	// first issue it accepts: the second page is never requested.
	stop := IssueFilter{Oldest: true, Stop: func(i Issue) bool { return i.Number == 3 }}
	if is, err := c.ListIssues(t.Context(), 10, stop); err != nil || len(is.Issues) != 50 || is.Truncated {
		t.Errorf("ListIssues oldest = %d %v truncated %v", len(is.Issues), err, is.Truncated)
	}
	created, err := c.CreateIssue(t.Context(), "Dependency Dashboard", "body", []string{"dependencies", "bot"})
	if err != nil || created.Number != 201 || created.URL != "https://x/issues/201" {
		t.Fatalf("CreateIssue %+v %v", created, err)
	}
	if err := c.UpdateIssue(t.Context(), 201, "new"); err != nil {
		t.Fatal(err)
	}
	cs := calls()
	if b := cs[4].body; b["title"] != "Dependency Dashboard" || b["body"] != "body" || fmt.Sprint(b["labels"]) != "[dependencies bot]" {
		t.Errorf("create body %v", b)
	}
	if b := cs[5].body; b["body"] != "new" || len(b) != 1 {
		t.Errorf("update body %v", b)
	}
	for _, c := range cs {
		if c.auth != "Bearer tok" {
			t.Errorf("%s auth %q", c.uri, c.auth)
		}
	}
}

// A title is looked up with the search API, the only filter by title GitHub's REST API has,
// so an issue behind thousands of others is found without paging through them. A search the
// deployment refuses falls back to that paging.
func TestGitHubIssueTitleSearch(t *testing.T) {
	const title = "Dependency Dashboard"
	search := "GET /search/issues?advanced_search=true&per_page=100&q=" +
		neturl.QueryEscape(`repo:o/r is:issue is:open in:title "`+title+`"`)
	found := `{"total_count":2,"items":[{"number":7,"title":"Dependency Dashboard","body":"b","html_url":"u7"},` +
		`{"number":8,"title":"Dependency Dashboard pull","pull_request":{"url":"u"}}]}`
	srv, calls := routes(t, map[string]route{search: {body: found}})
	c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
	is, err := c.ListIssues(t.Context(), 10, IssueFilter{Title: title, Oldest: true})
	if err != nil || len(is.Issues) != 1 || is.Issues[0] != (Issue{Number: 7, Title: title, Body: "b", URL: "u7"}) || is.Truncated {
		t.Fatalf("ListIssues = %+v %v", is, err)
	}
	if got := uris(calls()); len(got) != 1 {
		t.Errorf("calls %v, want the search alone", got)
	}

	// The search is unavailable: the issues are listed instead.
	srv, calls = routes(t, map[string]route{
		search: {code: http.StatusForbidden, body: `{"message":"API rate limit exceeded"}`},
		"GET /repos/o/r/issues?state=open&sort=created&direction=asc&per_page=100&page=1": {body: `[{"number":7,"title":"Dependency Dashboard","body":"b","html_url":"u7"}]`},
	})
	c = NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
	is, err = c.ListIssues(t.Context(), 10, IssueFilter{Title: title, Oldest: true})
	if err != nil || len(is.Issues) != 1 || is.Issues[0].Number != 7 {
		t.Fatalf("fallback ListIssues = %+v %v", is, err)
	}
	if got := uris(calls()); len(got) != 2 {
		t.Errorf("calls %v, want the search and the listing", got)
	}
}

func TestGiteaAutoMerge(t *testing.T) {
	for _, kind := range []Kind{Gitea, Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			doKey := map[Kind]string{Gitea: "do", Forgejo: "Do"}[kind]
			srv, calls := routes(t, map[string]route{
				"GET /repos/o/r":                    {body: `{"default_branch":"main","default_merge_style":"rebase-merge"}`},
				"POST /repos/o/r/pulls/1/merge":     {code: http.StatusCreated},
				"POST /repos/o/r/pulls/2/merge":     {code: http.StatusConflict, body: `{"message":"pull request is already scheduled to auto merge when checks succeed [pull_id: 2]"}`},
				"POST /repos/o/r/pulls/3/merge":     {code: http.StatusMethodNotAllowed, body: `{"message":"Please try again later"}`},
				"POST /repos/o/r/pulls/4/merge":     {code: http.StatusMethodNotAllowed, body: `{"message":"User not allowed to merge PR"}`},
				"POST /repos/o/r/pulls/5/merge":     {code: http.StatusConflict, body: `{"message":"merge failed because of conflict"}`},
				"GET /repos/o/r/commits/abc/status": {body: `{"state":"success","total_count":2}`},
				"GET /repos/o/r/commits/new/status": {body: `{"state":"pending","total_count":0}`},
				"GET /repos/o/r/commits/run/status": {body: `{"state":"pending","total_count":1}`},
				"GET /repos/o/r/commits/bad/status": {body: `{"state":"warning","total_count":2}`},
			})
			c := NewClient(Info{Kind: kind, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
			ctx := t.Context()
			if err := c.EnableAutoMerge(ctx, PR{Number: 1, HeadSHA: "abc"}, "auto"); err != nil {
				t.Errorf("scheduled: %v", err)
			}
			if err := c.EnableAutoMerge(ctx, PR{Number: 2}, "squash"); err != nil {
				t.Errorf("already scheduled: %v", err)
			}
			if err := c.EnableAutoMerge(ctx, PR{Number: 3}, "merge"); !errors.Is(err, ErrAutoMergePending) {
				t.Errorf("try again later: %v", err)
			}
			if err := c.EnableAutoMerge(ctx, PR{Number: 4}, "merge"); err == nil || errors.Is(err, ErrAutoMergePending) || !strings.Contains(err.Error(), "405") {
				t.Errorf("not allowed: %v", err)
			}
			if err := c.MergePR(ctx, PR{Number: 5, HeadSHA: "abc"}, "rebase"); err == nil || !strings.Contains(err.Error(), "POST /repos/o/r/pulls/5/merge: 409 Conflict") {
				t.Errorf("merge conflict: %v", err)
			}
			for sha, want := range map[string]CheckState{"abc": ChecksSuccess, "new": ChecksNone, "run": ChecksPending, "bad": ChecksFailure} {
				if got, err := c.Checks(ctx, sha); got != want || err != nil {
					t.Errorf("checks %s: %v %v, want %v", sha, got, err, want)
				}
			}
			cs := calls()
			want := []string{"GET /repos/o/r", "POST /repos/o/r/pulls/1/merge", "POST /repos/o/r/pulls/2/merge", "POST /repos/o/r/pulls/3/merge", "POST /repos/o/r/pulls/4/merge",
				"POST /repos/o/r/pulls/5/merge"}
			if got := uris(cs); len(got) != len(want)+4 || !slices.Equal(got[:len(want)], want) {
				t.Fatalf("calls %v", got)
			}
			for _, c := range cs {
				if c.auth != "token tok" {
					t.Errorf("%s auth %q", c.uri, c.auth)
				}
			}
			wantBody := func(i int, want map[string]any) {
				t.Helper()
				if fmt.Sprint(cs[i].body) != fmt.Sprint(want) {
					t.Errorf("%s body %v, want %v", cs[i].uri, cs[i].body, want)
				}
			}
			wantBody(1, map[string]any{doKey: "rebase-merge", "merge_when_checks_succeed": true, "delete_branch_after_merge": true, "head_commit_id": "abc"})
			wantBody(2, map[string]any{doKey: "squash", "merge_when_checks_succeed": true, "delete_branch_after_merge": true})
			wantBody(5, map[string]any{doKey: "rebase", "delete_branch_after_merge": true, "head_commit_id": "abc"})
		})
	}
}

func TestGiteaIssues(t *testing.T) {
	var labels []string
	for i := range 50 {
		labels = append(labels, fmt.Sprintf(`{"id":%d,"name":"l%d"}`, i+1, i))
	}
	srv, calls := routes(t, map[string]route{
		"GET /repos/o/r/issues?state=open&type=issues&sort=oldest&q=Dependency+Dashboard&limit=50&page=1": {body: `[{"number":3,"title":"Dependency Dashboard","body":"b","html_url":"u3"}]`},
		"GET /repos/o/r/labels?limit=50&page=1":                                                           {body: "[" + strings.Join(labels, ",") + "]"},
		"GET /repos/o/r/labels?limit=50&page=2":                                                           {body: `[{"id":99,"name":"Dependencies"}]`},
		"GET /orgs/o/labels?limit=50&page=1":                                                              {body: `[{"id":7,"name":"bot"}]`},
		"POST /repos/o/r/issues":                                                                          {code: http.StatusCreated, body: `{"number":4,"title":"Dependency Dashboard","html_url":"u4"}`},
		"PATCH /repos/o/r/issues/3":                                                                       {code: http.StatusCreated, body: `{}`},
	})
	c := NewClient(Info{Kind: Gitea, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
	ctx := t.Context()
	is, err := c.ListIssues(ctx, 10, IssueFilter{Title: "Dependency Dashboard", Oldest: true})
	if err != nil || len(is.Issues) != 1 || is.Issues[0] != (Issue{Number: 3, Title: "Dependency Dashboard", Body: "b", URL: "u3"}) || is.Truncated {
		t.Fatalf("ListIssues %+v %v", is, err)
	}
	created, err := c.CreateIssue(ctx, "Dependency Dashboard", "body", []string{"dependencies", "bot", "missing"})
	if err != nil || created.Number != 4 || created.URL != "u4" {
		t.Fatalf("CreateIssue %+v %v", created, err)
	}
	if err := c.UpdateIssue(ctx, 3, "new"); err != nil {
		t.Fatal(err)
	}
	cs := calls()
	want := []string{"GET /repos/o/r/issues?state=open&type=issues&sort=oldest&q=Dependency+Dashboard&limit=50&page=1", "GET /repos/o/r/labels?limit=50&page=1", "GET /repos/o/r/labels?limit=50&page=2",
		"GET /orgs/o/labels?limit=50&page=1", "POST /repos/o/r/issues", "PATCH /repos/o/r/issues/3"}
	if !slices.Equal(uris(cs), want) {
		t.Fatalf("calls %v", uris(cs))
	}
	if b := cs[4].body; b["title"] != "Dependency Dashboard" || b["body"] != "body" || fmt.Sprint(b["labels"]) != "[99 7]" {
		t.Errorf("create body %v (label names resolved to ids, unknown dropped)", b)
	}
	if b := cs[5].body; b["body"] != "new" || len(b) != 1 {
		t.Errorf("update body %v", b)
	}

	// A user-owned repository has no organization labels; issues without labels skip lookups.
	srv, calls = routes(t, map[string]route{
		"GET /repos/u/r/labels?limit=50&page=1": {body: `[]`},
		"POST /repos/u/r/issues":                {code: http.StatusCreated, body: `{"number":1}`},
	})
	c = NewClient(Info{Kind: Forgejo, APIURL: srv.URL, Repo: "u/r"}, "tok", srv.Client())
	if _, err := c.CreateIssue(ctx, "t", "b", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIssue(ctx, "t", "b", nil); err != nil {
		t.Fatal(err)
	}
	if got := uris(calls()); !slices.Equal(got, []string{"GET /repos/u/r/labels?limit=50&page=1", "GET /orgs/u/labels?limit=50&page=1", "POST /repos/u/r/issues", "POST /repos/u/r/issues"}) {
		t.Errorf("user repo calls %v", got)
	}
}

func TestIssuesDisabled(t *testing.T) {
	srv, _ := routes(t, map[string]route{
		"GET /repos/o/r/issues?state=open&per_page=100&page=1": {code: http.StatusGone, body: `{"message":"Issues are disabled for this repo"}`},
	})
	_, err := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client()).ListIssues(t.Context(), 1, IssueFilter{})
	if !errors.Is(err, ErrIssuesDisabled) || !strings.Contains(err.Error(), "410 Gone") {
		t.Errorf("GitHub 410: %v", err)
	}
	for repo, want := range map[string]bool{
		`{"has_issues":true,"external_tracker":{"external_tracker_url":"https://github.com/o/r/issues"}}`: true,
		`{"has_issues":false,"external_tracker":null}`:                                                    true,
		`{"has_issues":true,"external_tracker":null}`:                                                     false,
	} {
		srv, _ := routes(t, map[string]route{"GET /repos/o/r": {body: repo}})
		_, err := NewClient(Info{Kind: Gitea, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client()).ListIssues(t.Context(), 1, IssueFilter{})
		if err == nil || errors.Is(err, ErrIssuesDisabled) != want || !strings.Contains(err.Error(), "404 Not Found") {
			t.Errorf("Gitea 404 with repo %s: %v, want disabled %v", repo, err, want)
		}
	}
}
