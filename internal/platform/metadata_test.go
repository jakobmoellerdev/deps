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
