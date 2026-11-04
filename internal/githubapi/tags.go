package githubapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"
)

const tagQuery = `query($owner:String!,$name:String!,$ref:String!){repository(owner:$owner,name:$name){ref(qualifiedName:$ref){target{` +
	`... on Commit{committedDate} ... on Tag{tagger{date} target{... on Commit{committedDate}}}}}}}`

// TagTime returns when tag was made, for tags without a release of their own. Over GraphQL it
// is the tagger date of an annotated tag, else the committer date of the tagged commit (as
// Renovate's github-tags datasource); over REST, which would need a request per object, it is
// the committer date of commit, the commit tag peels to (one request). A missing repository,
// tag or commit is zero. Callers dedupe.
func (c *Client) TagTime(ctx context.Context, r Repo, tag, commit string) (time.Time, error) {
	t, err := c.tagTime(ctx, r, tag, commit)
	if errors.Is(err, ErrNotFound) {
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
	if commit == "" {
		return time.Time{}, nil
	}
	if err := c.acquire(ctx); err != nil {
		return time.Time{}, err
	}
	defer c.release()
	resp, err := c.restGet(ctx, r, "/repos/"+r.Path()+"/git/commits/"+url.PathEscape(commit))
	if err != nil {
		return time.Time{}, err
	}
	defer closeBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return time.Time{}, statusError(resp)
	}
	var raw struct {
		Committer struct {
			Date *time.Time `json:"date"`
		} `json:"committer"`
	}
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), &raw); err != nil {
		return time.Time{}, fmt.Errorf("malformed response: %w", err)
	}
	if raw.Committer.Date == nil {
		return time.Time{}, nil
	}
	return *raw.Committer.Date, nil
}
