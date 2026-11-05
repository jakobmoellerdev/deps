package platform

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestSetPRMetadataGitHub(t *testing.T) {
	srv, calls := routes(t, map[string]route{
		"POST /repos/o/r/issues/7/labels":             {body: `[]`},
		"POST /repos/o/r/pulls/7/requested_reviewers": {code: http.StatusCreated, body: `{}`},
		"PATCH /repos/o/r/issues/7":                   {code: http.StatusUnprocessableEntity, body: `{"message":"bad assignee"}`},
	})
	c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
	err := c.SetPRMetadata(t.Context(), 7, PRMetadata{Labels: []string{"dependencies"}, Reviewers: []string{"alice", "team:core"}, Assignees: []string{"bob"}})
	if err == nil || !strings.Contains(err.Error(), "assign #7") || strings.Contains(err.Error(), "labels") {
		t.Errorf("err = %v; want only the assignee failure", err)
	}
	got := calls()
	if len(got) != 3 {
		t.Fatalf("calls = %+v", got)
	}
	for i, want := range []string{
		"POST /repos/o/r/issues/7/labels map[labels:[dependencies]]",
		"POST /repos/o/r/pulls/7/requested_reviewers map[reviewers:[alice] team_reviewers:[core]]",
		"PATCH /repos/o/r/issues/7 map[assignees:[bob]]",
	} {
		if s := fmt.Sprintf("%s %s %v", got[i].method, got[i].uri, got[i].body); s != want {
			t.Errorf("call %d = %s, want %s", i, s, want)
		}
	}
	if _, calls := routes(t, nil); c.SetPRMetadata(t.Context(), 7, PRMetadata{}) != nil || len(calls()) != 0 {
		t.Error("empty metadata must not call the API")
	}
}

func TestSetPRMetadataGiteaLabelIDs(t *testing.T) {
	srv, calls := routes(t, map[string]route{
		"GET /api/v1/repos/o/r/labels?limit=50&page=1": {body: `[{"id":3,"name":"Dependencies"}]`},
		"GET /api/v1/orgs/o/labels?limit=50&page=1":    {body: `[{"id":9,"name":"security"}]`},
		"POST /api/v1/repos/o/r/issues/5/labels":       {body: `[]`},
		"POST /api/v1/repos/o/r/issues/6/labels":       {body: `[]`},
	})
	c := NewClient(Info{Kind: Gitea, APIURL: srv.URL + "/api/v1", Repo: "o/r"}, "tok", srv.Client())
	for _, n := range []int{5, 6} {
		if err := c.SetPRMetadata(t.Context(), n, PRMetadata{Labels: []string{"dependencies", "security", "missing"}}); err != nil {
			t.Fatal(err)
		}
	}
	var lists, posts []string
	for _, c := range calls() {
		switch c.method {
		case http.MethodGet:
			lists = append(lists, c.uri)
		case http.MethodPost:
			posts = append(posts, fmt.Sprintf("%s %v", c.uri, c.body))
		}
	}
	// "missing" is in no list: both lists are read once, then the result is reused.
	if len(lists) != 2 {
		t.Errorf("label lists fetched %v, want each once", lists)
	}
	want := []string{"/api/v1/repos/o/r/issues/5/labels map[labels:[3 9]]", "/api/v1/repos/o/r/issues/6/labels map[labels:[3 9]]"}
	if fmt.Sprint(posts) != fmt.Sprint(want) {
		t.Errorf("posts = %v, want %v", posts, want)
	}
}

func TestPRLabels(t *testing.T) {
	for _, tc := range []struct {
		kind      Kind
		api, path string
	}{
		{GitHub, "", "/repos/o/r/pulls/1"},
		{Gitea, "/api/v1", "/api/v1/repos/o/r/pulls/1"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			srv, _ := routes(t, map[string]route{
				"GET " + tc.path: {body: `{"number":1,"head":{"ref":"deps/a"},"labels":[{"id":3,"name":"Rebase"},{"id":4,"name":"dependencies"}]}`},
			})
			pr, err := NewClient(Info{Kind: tc.kind, APIURL: srv.URL + tc.api, Repo: "o/r"}, "tok", srv.Client()).GetPR(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(pr.Labels) != "[Rebase dependencies]" || !pr.HasLabel("rebase") || pr.HasLabel("stop-updating") {
				t.Errorf("labels = %q", pr.Labels)
			}
		})
	}
}

func TestRemovePRLabel(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		srv, calls := routes(t, map[string]route{
			"DELETE /repos/o/r/issues/7/labels/stop%20updating": {body: `[]`},
			"DELETE /repos/o/r/issues/8/labels/rebase":          {code: http.StatusInternalServerError, body: `{}`},
		})
		c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
		if err := c.RemovePRLabel(t.Context(), 7, "stop updating"); err != nil {
			t.Error(err)
		}
		if err := c.RemovePRLabel(t.Context(), 9, "rebase"); err != nil {
			t.Errorf("a label the PR lacks (404): %v", err)
		}
		if err := c.RemovePRLabel(t.Context(), 8, "rebase"); err == nil {
			t.Error("a 500 must be an error")
		}
		if got := fmt.Sprint(uris(calls())); got != "[DELETE /repos/o/r/issues/7/labels/stop%20updating DELETE /repos/o/r/issues/9/labels/rebase DELETE /repos/o/r/issues/8/labels/rebase]" {
			t.Errorf("calls = %s", got)
		}
	})
	t.Run("gitea", func(t *testing.T) {
		srv, calls := routes(t, map[string]route{
			"GET /api/v1/repos/o/r/labels?limit=50&page=1": {body: `[{"id":3,"name":"Rebase"}]`},
			"GET /api/v1/orgs/o/labels?limit=50&page=1":    {body: `[]`},
			"DELETE /api/v1/repos/o/r/issues/5/labels/3":   {code: http.StatusNoContent},
			"DELETE /api/v1/repos/o/r/issues/7/labels/3":   {code: http.StatusInternalServerError},
		})
		c := NewClient(Info{Kind: Gitea, APIURL: srv.URL + "/api/v1", Repo: "o/r"}, "tok", srv.Client())
		if err := c.RemovePRLabel(t.Context(), 5, "rebase"); err != nil {
			t.Error(err)
		}
		if err := c.RemovePRLabel(t.Context(), 6, "rebase"); err != nil {
			t.Errorf("a label the PR lacks (404): %v", err)
		}
		if err := c.RemovePRLabel(t.Context(), 7, "rebase"); err == nil {
			t.Error("a 500 must be an error")
		}
		if err := c.RemovePRLabel(t.Context(), 5, "unknown"); err != nil {
			t.Errorf("an unknown label: %v", err)
		}
		want := "[GET /api/v1/repos/o/r/labels?limit=50&page=1 DELETE /api/v1/repos/o/r/issues/5/labels/3 DELETE /api/v1/repos/o/r/issues/6/labels/3 DELETE /api/v1/repos/o/r/issues/7/labels/3 GET /api/v1/orgs/o/labels?limit=50&page=1]"
		if got := fmt.Sprint(uris(calls())); got != want {
			t.Errorf("calls = %s\nwant    %s", got, want)
		}
	})
	t.Run("gitea label lookup fails", func(t *testing.T) {
		srv, _ := routes(t, map[string]route{"GET /api/v1/repos/o/r/labels?limit=50&page=1": {code: http.StatusInternalServerError}})
		c := NewClient(Info{Kind: Gitea, APIURL: srv.URL + "/api/v1", Repo: "o/r"}, "tok", srv.Client())
		if err := c.RemovePRLabel(t.Context(), 5, "rebase"); err == nil || !strings.Contains(err.Error(), "labels of #5") {
			t.Errorf("err = %v", err)
		}
	})
}
