package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// glAPI is the project path of the GitLab test project grp/sub/r under /api/v4.
const glAPI = "/api/v4/projects/grp%2Fsub%2Fr"

func gitlabTestClient(t *testing.T, rs map[string]route) (Client, func() []apiCall) {
	t.Helper()
	srv, calls := routes(t, rs)
	return NewClient(Info{Kind: GitLab, APIURL: srv.URL + "/api/v4", Repo: "grp/sub/r"}, "tok", srv.Client()), calls
}

func TestGitLabMRFields(t *testing.T) {
	c, calls := gitlabTestClient(t, map[string]route{
		"GET " + glAPI + "/merge_requests/3": {body: `{"iid":3,"id":900,"title":"t","description":"b","state":"opened",
			"web_url":"https://gl/grp/sub/r/-/merge_requests/3","created_at":"2026-11-05T10:00:00Z","source_branch":"deps/a",
			"target_branch":"main","sha":"abc","labels":["Rebase","dependencies"],"detailed_merge_status":"mergeable",
			"merge_when_pipeline_succeeds":true}`},
	})
	pr, err := c.GetPR(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	want := PR{Number: 3, Title: "t", Body: "b", Head: "deps/a", HeadSHA: "abc", Base: "main", State: "open", URL: "https://gl/grp/sub/r/-/merge_requests/3",
		CreatedAt: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC), Labels: []string{"Rebase", "dependencies"}, AutoMerge: true, PushCancelsAutoMerge: true}
	if pr.Mergeable == nil || !*pr.Mergeable {
		t.Errorf("mergeable = %v", pr.Mergeable)
	}
	pr.Mergeable = nil
	if fmt.Sprintf("%+v", pr) != fmt.Sprintf("%+v", want) {
		t.Errorf("pr = %+v\nwant %+v", pr, want)
	}
	if !pr.HasLabel("rebase") {
		t.Error("HasLabel(rebase) = false")
	}
	if got := calls(); len(got) != 1 || got[0].auth != "Bearer tok" {
		t.Errorf("calls = %+v", got)
	}
}

func TestGitLabMergeable(t *testing.T) {
	f, tr := false, true
	for _, tc := range []struct {
		mr   gitlabMR
		want *bool
	}{
		{gitlabMR{DetailedMergeStatus: "mergeable"}, &tr},
		{gitlabMR{DetailedMergeStatus: "conflict"}, &f},
		{gitlabMR{DetailedMergeStatus: "need_rebase"}, &f},
		{gitlabMR{DetailedMergeStatus: "mergeable", HasConflicts: true}, &f},
		{gitlabMR{DetailedMergeStatus: "checking"}, nil},
		{gitlabMR{DetailedMergeStatus: "ci_must_pass"}, nil},
		{gitlabMR{}, nil},
	} {
		got := tc.mr.pr().Mergeable
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("%+v: mergeable = %v, want %v", tc.mr, got, tc.want)
		}
	}
	for state, want := range map[string]struct {
		state  string
		merged bool
	}{"opened": {"open", false}, "locked": {"open", false}, "closed": {"closed", false}, "merged": {"closed", true}} {
		if pr := (&gitlabMR{State: state}).pr(); pr.State != want.state || pr.Merged != want.merged {
			t.Errorf("%s: state %q merged %v", state, pr.State, pr.Merged)
		}
	}
}

func TestGitLabListPRs(t *testing.T) {
	full := make([]string, 100)
	for i := range full {
		full[i] = fmt.Sprintf(`{"iid":%d,"state":"opened","source_branch":"deps/b%d"}`, i+1, i+1)
	}
	q := "&order_by=updated_at&sort=desc&per_page=100&page="
	c, calls := gitlabTestClient(t, map[string]route{
		"GET " + glAPI + "/merge_requests?state=opened" + q + "1": {body: "[" + strings.Join(full, ",") + "]"},
		"GET " + glAPI + "/merge_requests?state=opened" + q + "2": {body: `[{"iid":101,"state":"opened"}]`},
		"GET " + glAPI + "/merge_requests?state=closed" + q + "1": {body: `[{"iid":2,"state":"closed","updated_at":"2026-01-02T00:00:00Z"},
			{"iid":5,"state":"closed","updated_at":"2026-01-01T00:00:00Z"}]`},
		"GET " + glAPI + "/merge_requests?state=merged" + q + "1": {body: `[{"iid":3,"state":"merged","updated_at":"2026-01-01T12:00:00Z"}]`},
	})
	open, err := c.ListPRs(t.Context(), "open", 5)
	if err != nil || len(open) != 101 || open[100].Number != 101 || open[0].Head != "deps/b1" {
		t.Fatalf("open = %d %v", len(open), err)
	}
	closed, err := c.ListPRs(t.Context(), "closed", 1)
	if err != nil || len(closed) != 3 || closed[0].Number != 2 || closed[1].Number != 3 || !closed[1].Merged || closed[2].Number != 5 {
		t.Fatalf("closed = %+v %v", closed, err)
	}
	if n := len(calls()); n != 4 {
		t.Errorf("calls = %v", uris(calls()))
	}
	if _, err := c.ListPRs(t.Context(), "open", 0); err != nil {
		t.Fatal(err)
	}
	c, _ = gitlabTestClient(t, nil)
	if _, err := c.ListPRs(t.Context(), "open", 1); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

// A merge request opened from a fork has its source branch in another project, so ListPRs
// leaves it out and GetPR refuses it: deps must not push to, edit or close a branch that is
// not the project's own.
func TestGitLabForkMRs(t *testing.T) {
	const own = `{"iid":1,"state":"opened","source_branch":"deps/a","sha":"abc","source_project_id":7,"target_project_id":7}`
	const fork = `{"iid":2,"state":"opened","source_branch":"deps/a","sha":"def","source_project_id":9,"target_project_id":7}`
	q := "&order_by=updated_at&sort=desc&per_page=100&page="
	c, _ := gitlabTestClient(t, map[string]route{
		"GET " + glAPI + "/merge_requests?state=opened" + q + "1": {body: "[" + own + "," + fork + "]"},
		"GET " + glAPI + "/merge_requests/1":                      {body: own},
		"GET " + glAPI + "/merge_requests/2":                      {body: fork},
	})
	open, err := c.ListPRs(t.Context(), "open", 1)
	if err != nil || len(open) != 1 || open[0].Number != 1 {
		t.Fatalf("ListPRs = %+v %v", open, err)
	}
	if pr, err := c.GetPR(t.Context(), 1); err != nil || pr.HeadSHA != "abc" {
		t.Fatalf("GetPR 1 = %+v %v", pr, err)
	}
	pr, err := c.GetPR(t.Context(), 2)
	if !errors.Is(err, ErrForkPR) || pr.Number != 0 {
		t.Fatalf("GetPR 2 = %+v %v", pr, err)
	}
	if !strings.Contains(err.Error(), "project 9") {
		t.Errorf("GetPR 2: err = %v, want the source project named", err)
	}
}

// A merge request for a branch GitLab does not know yet (its push is still processed) is
// created again; other 400s and a canceled context end it.
func TestGitLabCreatePRBranchRace(t *testing.T) {
	const unknown = `{"message":{"source_branch":["does not exist"]}}`
	for _, tc := range []struct {
		name    string
		answers []route
		waits   int
		want    string // error substring, "" = created
		calls   int
	}{
		{"created after two refusals", []route{{http.StatusBadRequest, unknown}, {http.StatusBadRequest, unknown}, {http.StatusCreated, `{"iid":7}`}}, 4, "", 3},
		{"gives up after the waits", []route{{http.StatusBadRequest, unknown}}, 2, "does not exist", 3},
		{"other 400 is not retried", []route{{http.StatusBadRequest, `{"message":{"target_branch":["does not exist"]}}`}}, 4, "target_branch", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				a := tc.answers[min(n, len(tc.answers)-1)]
				n++
				w.WriteHeader(a.code)
				io.WriteString(w, a.body)
			}))
			t.Cleanup(srv.Close)
			c := newGitLab(Info{Kind: GitLab, APIURL: srv.URL + "/api/v4", Repo: "grp/sub/r"}, "tok", srv.Client())
			c.branchWaits = make([]time.Duration, tc.waits)
			pr, err := c.CreatePR(t.Context(), "deps/a", "main", "t", "b", false)
			if tc.want == "" && (err != nil || pr.Number != 7) || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) || n != tc.calls {
				t.Errorf("CreatePR = %+v, %v after %d calls", pr, err, n)
			}
		})
	}
	// The run is canceled while it waits.
	ctx, cancel := context.WithCancel(t.Context())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, unknown)
	}))
	t.Cleanup(srv.Close)
	c := newGitLab(Info{Kind: GitLab, APIURL: srv.URL + "/api/v4", Repo: "grp/sub/r"}, "tok", srv.Client())
	if len(c.branchWaits) != len(gitlabBranchWaits) {
		t.Errorf("default waits = %v", c.branchWaits)
	}
	if _, err := c.CreatePR(ctx, "deps/a", "main", "t", "b", false); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}

func TestGitLabWrites(t *testing.T) {
	c, calls := gitlabTestClient(t, map[string]route{
		"POST " + glAPI + "/merge_requests":  {code: http.StatusCreated, body: `{"iid":7,"state":"opened","web_url":"u","sha":"s","source_branch":"deps/a","target_branch":"main"}`},
		"PUT " + glAPI + "/merge_requests/7": {body: `{}`},
		"POST " + glAPI + "/issues":          {code: http.StatusCreated, body: `{"iid":2,"title":"Dependency Dashboard","description":"d","web_url":"iu"}`},
		"PUT " + glAPI + "/issues/2":         {body: `{}`},
	})
	pr, err := c.CreatePR(t.Context(), "deps/a", "main", "title", "body", false)
	if err != nil || pr.Number != 7 || pr.URL != "u" || pr.HeadSHA != "s" || pr.Base != "main" {
		t.Fatalf("CreatePR = %+v %v", pr, err)
	}
	if err := c.UpdatePR(t.Context(), 7, "t2", "b2", false); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdatePR(t.Context(), 7, "", "b3", true); err != nil {
		t.Fatal(err)
	}
	if err := c.ClosePR(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	is, err := c.CreateIssue(t.Context(), "Dependency Dashboard", "d", []string{"deps", "dashboard"})
	if err != nil || is != (Issue{Number: 2, Title: "Dependency Dashboard", Body: "d", URL: "iu"}) {
		t.Fatalf("CreateIssue = %+v %v", is, err)
	}
	if _, err := c.CreateIssue(t.Context(), "x", "y", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateIssue(t.Context(), 2, "d2"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST " + glAPI + "/merge_requests map[description:body remove_source_branch:true source_branch:deps/a target_branch:main title:title]",
		"PUT " + glAPI + "/merge_requests/7 map[description:b2 title:t2]",
		"PUT " + glAPI + "/merge_requests/7 map[description:b3]",
		"PUT " + glAPI + "/merge_requests/7 map[state_event:close]",
		"POST " + glAPI + "/issues map[description:d labels:deps,dashboard title:Dependency Dashboard]",
		"POST " + glAPI + "/issues map[description:y title:x]",
		"PUT " + glAPI + "/issues/2 map[description:d2]",
	}
	got := calls()
	if len(got) != len(want) {
		t.Fatalf("calls = %v", uris(got))
	}
	for i, w := range want {
		if s := fmt.Sprintf("%s %s %v", got[i].method, got[i].uri, got[i].body); s != w {
			t.Errorf("call %d = %s\nwant %s", i, s, w)
		}
	}
}

// Draft merge requests: the Draft: title prefix, added once and kept on updates of a draft;
// GitLab reports the draft state.
func TestGitLabDraft(t *testing.T) {
	c, calls := gitlabTestClient(t, map[string]route{
		"POST " + glAPI + "/merge_requests":  {code: http.StatusCreated, body: `{"iid":7,"state":"opened","title":"Draft: chore: x","draft":true}`},
		"PUT " + glAPI + "/merge_requests/7": {body: `{}`},
	})
	pr, err := c.CreatePR(t.Context(), "deps/a", "main", "chore: x", "b", true)
	if err != nil || !pr.Draft {
		t.Fatalf("CreatePR = %+v %v", pr, err)
	}
	for _, title := range []string{"chore: x", "(draft) chore: x"} {
		if err := c.UpdatePR(t.Context(), 7, title, "b", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.UpdatePR(t.Context(), 7, "chore: x", "b", false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST " + glAPI + "/merge_requests map[description:b remove_source_branch:true source_branch:deps/a target_branch:main title:Draft: chore: x]",
		"PUT " + glAPI + "/merge_requests/7 map[description:b title:Draft: chore: x]",
		"PUT " + glAPI + "/merge_requests/7 map[description:b title:(draft) chore: x]",
		"PUT " + glAPI + "/merge_requests/7 map[description:b title:chore: x]",
	}
	got := calls()
	if len(got) != len(want) {
		t.Fatalf("calls = %v", uris(got))
	}
	for i, w := range want {
		if s := fmt.Sprintf("%s %s %v", got[i].method, got[i].uri, got[i].body); s != w {
			t.Errorf("call %d = %s\nwant %s", i, s, w)
		}
	}
}

func TestGitLabDefaultBranch(t *testing.T) {
	c, calls := gitlabTestClient(t, map[string]route{"GET " + glAPI: {body: `{"default_branch":"trunk"}`}})
	for range 2 {
		if b, err := c.DefaultBranch(t.Context()); err != nil || b != "trunk" {
			t.Fatalf("DefaultBranch = %q %v", b, err)
		}
	}
	if n := len(calls()); n != 1 {
		t.Errorf("project fetched %d times, want once", n)
	}
	c, _ = gitlabTestClient(t, map[string]route{"GET " + glAPI: {body: `{}`}})
	if _, err := c.DefaultBranch(t.Context()); err == nil || !strings.Contains(err.Error(), "empty default_branch") {
		t.Fatalf("err = %v", err)
	}
	c, _ = gitlabTestClient(t, nil)
	if _, err := c.DefaultBranch(t.Context()); err == nil {
		t.Fatal("want error")
	}
}

func TestGitLabSetPRMetadata(t *testing.T) {
	c, calls := gitlabTestClient(t, map[string]route{
		"PUT " + glAPI + "/merge_requests/7":    {body: `{}`},
		"PUT " + glAPI + "/merge_requests/8":    {body: `{}`},
		"GET /api/v4/users?username=alice":      {body: `[{"id":11,"username":"alice"}]`},
		"GET /api/v4/users?username=bob":        {body: `[{"id":12,"username":"bob"}]`},
		"GET /api/v4/users?username=nobody":     {body: `[]`},
		"GET /api/v4/users?username=broken%2Bx": {code: http.StatusInternalServerError, body: `{}`},
	})
	err := c.SetPRMetadata(t.Context(), 7, PRMetadata{
		Labels: []string{"dependencies", "go"}, Reviewers: []string{"alice", "team:core", "@bob"}, Assignees: []string{"bob", "nobody"},
	})
	if err == nil || !strings.Contains(err.Error(), "GitLab has no team reviewers: core") || !strings.Contains(err.Error(), `assign !7: unknown user "nobody"`) ||
		strings.Contains(err.Error(), "labels") {
		t.Errorf("err = %v", err)
	}
	// Users are looked up once per client.
	if err := c.SetPRMetadata(t.Context(), 8, PRMetadata{Assignees: []string{"alice", "Alice"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPRMetadata(t.Context(), 8, PRMetadata{Assignees: []string{"broken+x"}}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("lookup failure = %v", err)
	}
	var puts []string
	lookups := 0
	for _, call := range calls() {
		if call.method == http.MethodGet {
			lookups++
			continue
		}
		puts = append(puts, fmt.Sprintf("%s %v", call.uri, call.body))
	}
	want := []string{ // one PUT per MR
		glAPI + "/merge_requests/7 map[add_labels:dependencies,go assignee_ids:[12] reviewer_ids:[11 12]]",
		glAPI + "/merge_requests/8 map[assignee_ids:[11]]",
	}
	if fmt.Sprint(puts) != fmt.Sprint(want) {
		t.Errorf("puts = %v\nwant %v", puts, want)
	}
	if lookups != 4 { // alice, bob, nobody, broken+x
		t.Errorf("user lookups = %d", lookups)
	}
	if _, calls := routes(t, nil); c.SetPRMetadata(t.Context(), 7, PRMetadata{}) != nil || len(calls()) != 0 {
		t.Error("empty metadata must not call the API")
	}
	c, _ = gitlabTestClient(t, nil)
	err = c.SetPRMetadata(t.Context(), 7, PRMetadata{Labels: []string{"x"}, Reviewers: []string{"team:a"}})
	if err == nil || !strings.Contains(err.Error(), "set labels of !7") || !strings.Contains(err.Error(), "team reviewers") {
		t.Errorf("err = %v", err)
	}
}

func TestGitLabSetPRMetadataPutFails(t *testing.T) {
	c, _ := gitlabTestClient(t, map[string]route{
		"GET /api/v4/users?username=alice":   {body: `[{"id":11}]`},
		"GET /api/v4/users?username=nobody":  {body: `[]`},
		"PUT " + glAPI + "/merge_requests/7": {code: http.StatusForbidden, body: `{"message":"403 Forbidden"}`},
	})
	err := c.SetPRMetadata(t.Context(), 7, PRMetadata{Labels: []string{"x"}, Reviewers: []string{"alice"}, Assignees: []string{"nobody"}})
	if err == nil || !strings.Contains(err.Error(), "set labels, reviewers of !7") || !strings.Contains(err.Error(), `assign !7: unknown user "nobody"`) {
		t.Errorf("err = %v", err)
	}
}

// TestGitLabUserLookupsConcurrent checks that the lookup of one username does not hold up
// the lookups of others, and that concurrent callers of one username share its lookup.
func TestGitLabUserLookupsConcurrent(t *testing.T) {
	aliceAsked, bobAsked := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	lookups := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("username")
		if r.Method != http.MethodGet || name == "" {
			io.WriteString(w, `{}`)
			return
		}
		mu.Lock()
		lookups[name]++
		first := lookups[name] == 1
		mu.Unlock()
		switch {
		case name == "bob" && first:
			close(bobAsked)
		case name == "alice" && first:
			close(aliceAsked)
			select {
			case <-bobAsked:
			case <-time.After(5 * time.Second):
				t.Error("the lookup of alice held up the lookup of bob")
			}
		}
		io.WriteString(w, `[{"id":11}]`)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Info{Kind: GitLab, APIURL: srv.URL + "/api/v4", Repo: "grp/sub/r"}, "tok", srv.Client())
	var wg sync.WaitGroup
	set := func(n int, user string) {
		wg.Go(func() {
			if err := c.SetPRMetadata(t.Context(), n, PRMetadata{Assignees: []string{user}}); err != nil {
				t.Error(err)
			}
		})
	}
	set(7, "alice")
	<-aliceAsked
	set(8, "alice")
	set(9, "bob")
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if lookups["alice"] != 1 || lookups["bob"] != 1 {
		t.Errorf("lookups = %v, want each user once", lookups)
	}
}

func TestGitLabRemovePRLabel(t *testing.T) {
	c, calls := gitlabTestClient(t, map[string]route{
		"GET " + glAPI + "/merge_requests/7": {body: `{"iid":7,"labels":["Rebase","deps"]}`},
		"PUT " + glAPI + "/merge_requests/7": {body: `{}`},
	})
	if err := c.RemovePRLabel(t.Context(), 7, "rebase"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemovePRLabel(t.Context(), 7, "absent"); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 3 || got[1].method != http.MethodPut || fmt.Sprint(got[1].body) != "map[remove_labels:Rebase]" {
		t.Errorf("calls = %+v", got)
	}
	c, _ = gitlabTestClient(t, nil)
	if err := c.RemovePRLabel(t.Context(), 7, "x"); err == nil {
		t.Error("want error")
	}
}

func TestGitLabAutoMerge(t *testing.T) {
	merge := "PUT " + glAPI + "/merge_requests/7/merge"
	for _, tc := range []struct {
		name     string
		strategy string
		rt       route
		want     string // "" = success, "pending", or an error substring
		body     string
	}{
		{"scheduled", "auto", route{body: `{"iid":7,"merge_when_pipeline_succeeds":true}`}, "",
			"map[auto_merge:true merge_when_pipeline_succeeds:true sha:abc should_remove_source_branch:true]"},
		{"squash", "squash", route{body: `{"merge_when_pipeline_succeeds":true}`}, "",
			"map[auto_merge:true merge_when_pipeline_succeeds:true sha:abc should_remove_source_branch:true squash:true]"},
		{"merged right away", "merge", route{body: `{"state":"merged"}`}, "",
			"map[auto_merge:true merge_when_pipeline_succeeds:true sha:abc should_remove_source_branch:true squash:false]"},
		// GitLab answered without setting auto-merge (e.g. the push it awaits is still processed).
		{"not set", "", route{body: `{"state":"opened","merge_when_pipeline_succeeds":false,"detailed_merge_status":"checking"}`}, "pending", ""},
		{"not mergeable yet", "", route{code: http.StatusMethodNotAllowed, body: `{"message":"405 Method Not Allowed"}`}, "pending", ""},
		{"conflicts", "rebase", route{code: http.StatusNotAcceptable, body: `{"message":"Branch cannot be merged"}`}, "pending", ""},
		{"head moved", "", route{code: http.StatusConflict, body: `{"message":"SHA does not match HEAD of source branch"}`}, "pending", ""},
		{"failed", "", route{code: http.StatusUnprocessableEntity, body: `{"message":"Failed"}`}, "pending", ""},
		{"forbidden", "", route{code: http.StatusUnauthorized, body: `{"message":"401 Unauthorized"}`}, "401", ""},
		{"unknown strategy", "fast-forward", route{}, `unknown merge strategy "fast-forward"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := gitlabTestClient(t, map[string]route{merge: tc.rt})
			err := c.EnableAutoMerge(t.Context(), PR{Number: 7, HeadSHA: "abc"}, tc.strategy)
			switch tc.want {
			case "":
				if err != nil {
					t.Fatal(err)
				}
				if got := calls(); len(got) != 1 || fmt.Sprint(got[0].body) != tc.body {
					t.Errorf("calls = %+v", got)
				}
			case "pending":
				if !errors.Is(err, ErrAutoMergePending) {
					t.Errorf("err = %v, want ErrAutoMergePending", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, ErrAutoMergePending) {
					t.Errorf("err = %v, want %s", err, tc.want)
				}
			}
		})
	}
}

func TestGitLabMergePR(t *testing.T) {
	c, calls := gitlabTestClient(t, map[string]route{"PUT " + glAPI + "/merge_requests/7/merge": {body: `{}`}})
	if err := c.MergePR(t.Context(), PR{Number: 7}, "squash"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); len(got) != 1 || fmt.Sprint(got[0].body) != "map[should_remove_source_branch:true squash:true]" {
		t.Errorf("calls = %+v", got)
	}
}

func TestGitLabChecks(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       CheckState
	}{
		{"none", `[]`, ChecksNone},
		{"success", `[{"id":2,"ref":"deps/a","status":"success"},{"id":3,"ref":"refs/merge-requests/7/head","status":"skipped"}]`, ChecksSuccess},
		{"latest per ref wins", `[{"id":5,"ref":"deps/a","status":"success"},{"id":4,"ref":"deps/a","status":"failed"}]`, ChecksSuccess},
		{"running", `[{"id":5,"ref":"deps/a","status":"running"},{"id":4,"ref":"refs/merge-requests/7/head","status":"success"}]`, ChecksPending},
		{"manual", `[{"id":5,"ref":"deps/a","status":"manual"}]`, ChecksPending},
		{"failure beats pending", `[{"id":5,"ref":"deps/a","status":"pending"},{"id":4,"ref":"refs/merge-requests/7/head","status":"failed"}]`, ChecksFailure},
		{"canceled", `[{"id":5,"ref":"deps/a","status":"canceled"}]`, ChecksFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gitlabTestClient(t, map[string]route{
				"GET " + glAPI + "/pipelines?sha=abc&order_by=id&sort=desc&per_page=100": {body: tc.body},
			})
			got, err := c.Checks(t.Context(), "abc")
			if err != nil || got != tc.want {
				t.Fatalf("Checks = %q %v, want %q", got, err, tc.want)
			}
		})
	}
	c, _ := gitlabTestClient(t, nil)
	if _, err := c.Checks(t.Context(), "abc"); err == nil {
		t.Fatal("want error")
	}
}

func TestGitLabListIssues(t *testing.T) {
	full := make([]string, 100)
	for i := range full {
		full[i] = fmt.Sprintf(`{"iid":%d,"title":"i%d"}`, i+1, i+1)
	}
	c, calls := gitlabTestClient(t, map[string]route{
		"GET " + glAPI: {body: `{"default_branch":"main","issues_enabled":true}`},
		"GET " + glAPI + "/issues?state=opened&per_page=100&page=1": {body: "[" + strings.Join(full, ",") + "]"},
		"GET " + glAPI + "/issues?state=opened&per_page=100&page=2": {body: `[{"iid":101,"title":"Dependency Dashboard","description":"d","web_url":"u"}]`},
	})
	is, err := c.ListIssues(t.Context(), 3)
	if err != nil || len(is) != 101 || is[100] != (Issue{Number: 101, Title: "Dependency Dashboard", Body: "d", URL: "u"}) {
		t.Fatalf("ListIssues = %d %v", len(is), err)
	}
	if n := len(calls()); n != 3 {
		t.Errorf("calls = %v", uris(calls()))
	}

	issues := "GET " + glAPI + "/issues?state=opened&per_page=100&page=1"
	for _, tc := range []struct {
		name     string
		project  route
		issues   route
		err      bool
		disabled bool
	}{
		// GitLab lists nothing for a project with issues disabled (verified on GitLab 19.5).
		{"disabled", route{body: `{"issues_enabled":false}`}, route{body: `[]`}, true, true},
		{"unknown", route{body: `{}`}, route{body: `[]`}, false, false},
		{"list fails", route{body: `{"issues_enabled":true}`}, route{code: http.StatusForbidden, body: `{"message":"403 Forbidden"}`}, true, false},
		{"project fails", route{code: http.StatusNotFound, body: `{}`}, route{body: `[]`}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gitlabTestClient(t, map[string]route{issues: tc.issues, "GET " + glAPI: tc.project})
			_, err := c.ListIssues(t.Context(), 1)
			if (err != nil) != tc.err || errors.Is(err, ErrIssuesDisabled) != tc.disabled {
				t.Fatalf("err = %v, want err %v disabled %v", err, tc.err, tc.disabled)
			}
		})
	}
}
