package githubapi

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// archivedBatch is the number of repositories asked per Archived query, GraphQL's limit of
// nodes per connection that GitHub also applies to aliased fields.
const archivedBatch = 100

// Archived reports which of repos, all on one host, are archived (GraphQL `isArchived`), asking
// up to 100 repositories per query (`r<i>: repository(owner:, name:)`). The result has an entry
// for every repository asked; one the API does not show is not archived. GraphQL needs a
// token for the host; without one nothing is asked and nothing is archived.
func (c *Client) Archived(ctx context.Context, repos []Repo) (map[Repo]bool, error) {
	out := make(map[Repo]bool, len(repos))
	if len(repos) == 0 || !c.GraphQL(repos[0]) {
		for _, r := range repos {
			out[r] = false
		}
		return out, nil
	}
	for chunk := range slices.Chunk(repos, archivedBatch) {
		if err := c.archived(ctx, chunk, out); err != nil {
			return out, fmt.Errorf("archived repositories: %w", err)
		}
	}
	return out, nil
}

// archived asks one query for repos and records their state in out.
func (c *Client) archived(ctx context.Context, repos []Repo, out map[Repo]bool) error {
	var q strings.Builder
	vars := make(map[string]any, 2*len(repos))
	q.WriteString("query(")
	for i, r := range repos {
		n := strconv.Itoa(i)
		if i > 0 {
			q.WriteByte(',')
		}
		q.WriteString("$o" + n + ":String!,$n" + n + ":String!")
		vars["o"+n], vars["n"+n] = r.Owner, r.Name
	}
	q.WriteString("){")
	for i := range repos {
		n := strconv.Itoa(i)
		q.WriteString("r" + n + ":repository(owner:$o" + n + ",name:$n" + n + "){isArchived}")
	}
	q.WriteByte('}')
	var data map[string]*struct {
		IsArchived bool `json:"isArchived"`
	}
	// A repository the API does not show is null with a NOT_FOUND error, and an organization
	// that refuses the credentials (IP allow list) with a FORBIDDEN one; the others answer.
	if err := graphql(ctx, c, repos[0], q.String(), vars, &data); err != nil && !Unreadable(err) {
		return err
	}
	for i, r := range repos {
		v := data["r"+strconv.Itoa(i)]
		out[r] = v != nil && v.IsArchived
	}
	return nil
}
