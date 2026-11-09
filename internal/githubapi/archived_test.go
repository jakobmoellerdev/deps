package githubapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// TestArchived asks up to 100 repositories per GraphQL query; a repository the API does not
// show (null with a NOT_FOUND error, as GitHub answers) is not archived and does not fail the
// others. Without a token nothing is asked.
func TestArchived(t *testing.T) {
	c, s := newServer(t, tokenAuth(), func(w http.ResponseWriter, q gql) {
		var data, errs []string
		for i := 0; q.Variables["o"+strconv.Itoa(i)] != nil; i++ {
			n := strconv.Itoa(i)
			if name := q.Variables["n"+n].(string); name == "missing" {
				data = append(data, `"r`+n+`":null`)
				errs = append(errs, `{"type":"NOT_FOUND","path":["r`+n+`"],"message":"Could not resolve to a Repository with the name 'o/missing'."}`)
			} else {
				data = append(data, `"r`+n+`":{"isArchived":`+strconv.FormatBool(strings.HasPrefix(name, "old"))+`}`)
			}
		}
		_, _ = io.WriteString(w, `{"data":{`+strings.Join(data, ",")+`},"errors":[`+strings.Join(errs, ",")+`]}`)
	})
	repos := []Repo{{Scheme: "https", Host: "github.com", Owner: "o", Name: "missing"}}
	for i := range 150 {
		name := "r" + strconv.Itoa(i)
		if i%50 == 0 {
			name = "old" + strconv.Itoa(i)
		}
		repos = append(repos, Repo{Scheme: "https", Host: "github.com", Owner: "o", Name: name})
	}
	got, err := c.Archived(t.Context(), repos)
	if err != nil {
		t.Fatal(err)
	}
	archived := 0
	for _, r := range repos {
		a, ok := got[r]
		if !ok {
			t.Errorf("%s not in the result", r.Path())
		}
		if a {
			archived++
			if !strings.HasPrefix(r.Name, "old") {
				t.Errorf("%s archived", r.Path())
			}
		}
	}
	if archived != 3 || len(got) != len(repos) {
		t.Errorf("archived = %d of %d, want 3 of %d", archived, len(got), len(repos))
	}
	if len(s.reqs) != 2 {
		t.Fatalf("queries = %d, want 2 (100 repositories per query)", len(s.reqs))
	}
	if q := s.reqs[0].Query; !strings.HasPrefix(q, "query($o0:String!,$n0:String!,$o1:String!") ||
		!strings.Contains(q, "r99:repository(owner:$o99,name:$n99){isArchived}}") || strings.Contains(q, "r100:") {
		t.Errorf("query = %s", q)
	}

	if got, err := New(http.DefaultClient, nil, nil).Archived(t.Context(), repos[:2]); err != nil || len(got) != 2 || got[repos[1]] {
		t.Errorf("without a token: %v, %v", got, err)
	}
	if got, err := c.Archived(t.Context(), nil); err != nil || len(got) != 0 {
		t.Errorf("no repositories: %v, %v", got, err)
	}
}

func TestArchivedRateLimited(t *testing.T) {
	c, _ := newServer(t, tokenAuth(), func(w http.ResponseWriter, _ gql) {
		_, _ = io.WriteString(w, `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`)
	})
	if _, err := c.Archived(t.Context(), []Repo{oR}); !errors.Is(err, httpx.ErrRateLimited) ||
		!strings.HasPrefix(err.Error(), "archived repositories: ") {
		t.Errorf("err = %v", err)
	}
}
