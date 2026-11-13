package githubapi

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/url"
	"time"
)

const (
	tagQuery = `query($owner:String!,$name:String!,$ref:String!){repository(owner:$owner,name:$name){ref(qualifiedName:$ref){target{` +
		`... on Commit{committedDate} ... on Tag{tagger{date} target{... on Commit{committedDate}}}}}}}`
	commitQuery = `query($owner:String!,$name:String!,$oid:GitObjectID!){repository(owner:$owner,name:$name){object(oid:$oid){... on Commit{committedDate}}}}`
)

// TagTime returns when tag was made, for tags without a release of their own. Over GraphQL it
// is the tagger date of an annotated tag, else the committer date of the tagged commit (as
// Renovate's github-tags datasource); over REST, which would need a request per object, it is
// the committer date of commit, the commit tag peels to (one request). A missing repository,
// tag or commit is zero. Callers dedupe.
func (c *Client) TagTime(ctx context.Context, r Repo, tag, commit string) (time.Time, error) {
	t, err := c.tagTime(ctx, r, tag, commit)
	if Unreadable(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("tag %s of %s: %w", tag, r.Path(), err)
	}
	return t, nil
}

func (c *Client) tagTime(ctx context.Context, r Repo, tag, commit string) (time.Time, error) {
	type dated struct {
		CommittedDate *time.Time `json:"committedDate"`
	}
	if c.GraphQL(r) {
		var data struct {
			Repository *struct {
				Ref *struct {
					Target struct {
						CommittedDate *time.Time `json:"committedDate"`
						Tagger        *struct {
							Date *time.Time `json:"date"`
						} `json:"tagger"`
						Target *dated `json:"target"`
					} `json:"target"`
				} `json:"ref"`
			} `json:"repository"`
		}
		if err := graphql(ctx, c, r, tagQuery, map[string]any{"owner": r.Owner, "name": r.Name, "ref": "refs/tags/" + tag}, &data); err != nil {
			return time.Time{}, err
		}
		if data.Repository == nil || data.Repository.Ref == nil {
			return time.Time{}, nil
		}
		t := data.Repository.Ref.Target
		switch {
		case t.Tagger != nil && t.Tagger.Date != nil:
			return *t.Tagger.Date, nil
		case t.CommittedDate != nil:
			return *t.CommittedDate, nil
		case t.Target != nil && t.Target.CommittedDate != nil:
			return *t.Target.CommittedDate, nil
		}
		return time.Time{}, nil
	}
	return c.commitTimeREST(ctx, r, commit)
}

// CommitTime returns the committer date of commit, over GraphQL when available, else over REST
// (`/git/commits/<sha>`). A missing repository or commit is zero. Callers dedupe.
func (c *Client) CommitTime(ctx context.Context, r Repo, commit string) (time.Time, error) {
	t, err := c.commitTime(ctx, r, commit)
	if Unreadable(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("commit %s of %s: %w", commit, r.Path(), err)
	}
	return t, nil
}

func (c *Client) commitTime(ctx context.Context, r Repo, commit string) (time.Time, error) {
	if !c.GraphQL(r) {
		return c.commitTimeREST(ctx, r, commit)
	}
	var data struct {
		Repository *struct {
			Object *struct {
				CommittedDate *time.Time `json:"committedDate"`
			} `json:"object"`
		} `json:"repository"`
	}
	if err := graphql(ctx, c, r, commitQuery, map[string]any{"owner": r.Owner, "name": r.Name, "oid": commit}, &data); err != nil {
		return time.Time{}, err
	}
	if data.Repository == nil || data.Repository.Object == nil || data.Repository.Object.CommittedDate == nil {
		return time.Time{}, nil
	}
	return *data.Repository.Object.CommittedDate, nil
}

// commitTimeREST is the committer date of commit over REST; zero for an empty commit.
func (c *Client) commitTimeREST(ctx context.Context, r Repo, commit string) (time.Time, error) {
	if commit == "" {
		return time.Time{}, nil
	}
	var raw struct {
		Committer struct {
			Date *time.Time `json:"date"`
		} `json:"committer"`
	}
	err := c.restDo(ctx, r, "/repos/"+r.Path()+"/git/commits/"+url.PathEscape(commit), false, false, func(body io.Reader) error {
		if err := json.UnmarshalRead(io.LimitReader(body, 1<<20), &raw); err != nil {
			return fmt.Errorf("malformed response: %w", err)
		}
		return nil
	})
	if err != nil || raw.Committer.Date == nil {
		return time.Time{}, err
	}
	return *raw.Committer.Date, nil
}
