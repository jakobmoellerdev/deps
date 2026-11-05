package githubapi

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

const (
	// PageSize is the number of releases per page (the maximum of both APIs).
	PageSize = 100
	// MaxPages bounds the pages listed per repository.
	MaxPages = 5
)

// Release is a published (non-draft) release.
type Release struct {
	Tag        string
	Prerelease bool
	Published  time.Time // zero when unknown
	assets     []Asset   // REST only; GraphQL lists fetch asset digests on demand
}

// Asset is a REST release asset; Digest is the API `digest` ("sha256:<hex>"), "" if none.
type Asset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// List is the cached release list of one repository, newest first and without drafts,
// fetched page by page on demand. Its methods are only valid inside a Releases callback.
type List struct {
	mu       sync.Mutex
	pages    int
	cursor   string // GraphQL endCursor of the last page
	done     bool   // no more pages are fetched: complete, MaxPages reached, or an error
	complete bool   // every release is listed
	archived bool   // the repository is archived (GraphQL only; REST lists do not say)
	err      error
	rels     []Release
	byTag    map[string]int // index in rels
	stable   []datasource.Release
}

// Find returns the release with tag name tag.
func (l *List) Find(tag string) (Release, bool) {
	i, ok := l.byTag[tag]
	if !ok {
		return Release{}, false
	}
	return l.rels[i], true
}

// Stable returns the non-prerelease releases as datasource releases (Version = tag). The
// slice is shared and must not be modified.
func (l *List) Stable() []datasource.Release { return slices.Clip(l.stable) }

// Complete reports whether the list holds every release of the repository.
func (l *List) Complete() bool { return l.complete }

// Archived reports whether the repository is archived; known from GraphQL pages only, false
// for lists fetched over REST.
func (l *List) Archived() bool { return l.archived }

func (l *List) add(rel Release) {
	l.byTag[rel.Tag] = len(l.rels)
	l.rels = append(l.rels, rel)
	if !rel.Prerelease {
		l.stable = append(l.stable, datasource.Release{Version: rel.Tag})
	}
}

// Releases runs fn on r's release list after fetching pages until enough reports true or no
// pages are left; the first page is always fetched and enough may be nil (one page). fn runs
// under the list's lock. A failed page fails the list for the run.
func (c *Client) Releases(ctx context.Context, r Repo, enough func(*List) bool, fn func(*List)) error {
	key := strings.ToLower(c.rest(r) + "/repos/" + r.Path()) // GitHub names are case-insensitive
	c.mu.Lock()
	l := c.lists[key]
	if l == nil {
		l = &List{byTag: make(map[string]int)}
		c.lists[key] = l
	}
	c.mu.Unlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	for !l.done && (l.pages == 0 || enough != nil && !enough(l)) {
		c.next(ctx, r, l)
	}
	if l.err != nil {
		return l.err
	}
	fn(l)
	return nil
}

// next fetches the list's next page.
func (c *Client) next(ctx context.Context, r Repo, l *List) {
	start := time.Now()
	graphql := c.GraphQL(r)
	var more bool
	var err error
	if graphql {
		more, err = c.graphqlPage(ctx, r, l)
	} else {
		more, err = c.restPage(ctx, r, l)
	}
	l.pages++
	slog.DebugContext(ctx, "github releases page", "repo", r.Path(), "graphql", graphql, "page", l.pages,
		"releases", len(l.rels), "duration", time.Since(start), "err", err)
	switch {
	case err != nil:
		l.err, l.done = fmt.Errorf("releases %s: %w", r.Path(), err), true
	case !more:
		l.complete, l.done = true, true
	case l.pages >= MaxPages:
		l.done = true
	}
}

const releasesQuery = `query($owner:String!,$name:String!,$first:Int!,$cursor:String){repository(owner:$owner,name:$name){isArchived ` +
	`releases(first:$first,after:$cursor,orderBy:{field:CREATED_AT,direction:DESC}){pageInfo{hasNextPage endCursor}` +
	`nodes{tagName isDraft isPrerelease publishedAt}}}}`

// graphqlPage fetches the page after l.cursor and reports whether more pages exist.
func (c *Client) graphqlPage(ctx context.Context, r Repo, l *List) (bool, error) {
	vars := map[string]any{"owner": r.Owner, "name": r.Name, "first": PageSize, "cursor": nil}
	if l.cursor != "" {
		vars["cursor"] = l.cursor
	}
	var data struct {
		Repository *struct {
			IsArchived bool `json:"isArchived"`
			Releases   struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					TagName      string     `json:"tagName"`
					IsDraft      bool       `json:"isDraft"`
					IsPrerelease bool       `json:"isPrerelease"`
					PublishedAt  *time.Time `json:"publishedAt"`
				} `json:"nodes"`
			} `json:"releases"`
		} `json:"repository"`
	}
	if err := graphql(ctx, c, r, releasesQuery, vars, &data); err != nil {
		return false, err
	}
	if data.Repository == nil {
		return false, fmt.Errorf("repository: %w", ErrNotFound)
	}
	l.archived = data.Repository.IsArchived
	rels := data.Repository.Releases
	for _, n := range rels.Nodes {
		if n.IsDraft || n.TagName == "" {
			continue
		}
		rel := Release{Tag: n.TagName, Prerelease: n.IsPrerelease}
		if n.PublishedAt != nil {
			rel.Published = *n.PublishedAt
		}
		l.add(rel)
	}
	l.cursor = rels.PageInfo.EndCursor
	return rels.PageInfo.HasNextPage && l.cursor != "", nil
}

// restPage fetches page l.pages+1 and reports whether it was full. The array is decoded
// element by element so only one release (with its body) is buffered.
func (c *Client) restPage(ctx context.Context, r Repo, l *List) (bool, error) {
	var more bool
	err := c.restDo(ctx, r, "/repos/"+r.Path()+"/releases?per_page="+strconv.Itoa(PageSize)+"&page="+strconv.Itoa(l.pages+1), true, func(body io.Reader) error {
		var err error
		more, err = decodePage(body, l)
		return err
	})
	return more, err
}

// decodePage adds the releases of one REST page to l and reports whether it was full.
func decodePage(body io.Reader, l *List) (bool, error) {
	dec := jsontext.NewDecoder(body, jsontext.AllowDuplicateNames(true)) // no per-object name sets
	tok, err := dec.ReadToken()
	if err == nil && tok.Kind() != '[' {
		err = errors.New("not an array")
	}
	if err != nil {
		return false, fmt.Errorf("malformed response: %w", err)
	}
	n := 0
	for dec.PeekKind() != ']' {
		var rel struct {
			TagName     string     `json:"tag_name"`
			Draft       bool       `json:"draft"`
			Prerelease  bool       `json:"prerelease"`
			Assets      []Asset    `json:"assets"`
			PublishedAt *time.Time `json:"published_at"`
		}
		if err := json.UnmarshalDecode(dec, &rel); err != nil {
			return false, fmt.Errorf("malformed response: %w", err)
		}
		n++
		if rel.Draft || rel.TagName == "" {
			continue
		}
		out := Release{Tag: rel.TagName, Prerelease: rel.Prerelease, assets: rel.Assets}
		if rel.PublishedAt != nil {
			out.Published = *rel.PublishedAt
		}
		l.add(out)
	}
	if _, err := dec.ReadToken(); err != nil {
		return false, fmt.Errorf("malformed response: %w", err)
	}
	return n >= PageSize, nil
}

const releaseQuery = `query($owner:String!,$name:String!,$tag:String!){repository(owner:$owner,name:$name){` +
	`release(tagName:$tag){isDraft publishedAt}}}`

// ReleaseTime looks up the publication time of the release of tag directly, for tags missing
// from a list that is not Complete. A missing repository or release, or a draft, is zero.
// Callers dedupe.
func (c *Client) ReleaseTime(ctx context.Context, r Repo, tag string) (time.Time, error) {
	t, err := c.releaseTime(ctx, r, tag)
	if errors.Is(err, ErrNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("release %s of %s: %w", tag, r.Path(), err)
	}
	return t, nil
}

func (c *Client) releaseTime(ctx context.Context, r Repo, tag string) (time.Time, error) {
	type release struct {
		IsDraft     bool       `json:"isDraft"`
		PublishedAt *time.Time `json:"publishedAt"`
	}
	var rel release
	if c.GraphQL(r) {
		var data struct {
			Repository *struct {
				Release *release `json:"release"`
			} `json:"repository"`
		}
		if err := graphql(ctx, c, r, releaseQuery, map[string]any{"owner": r.Owner, "name": r.Name, "tag": tag}, &data); err != nil {
			return time.Time{}, err
		}
		if data.Repository == nil || data.Repository.Release == nil {
			return time.Time{}, nil
		}
		rel = *data.Repository.Release
	} else {
		err := c.restDo(ctx, r, "/repos/"+r.Path()+"/releases/tags/"+platform.EscapePath(tag), false, func(body io.Reader) error {
			var raw struct {
				Draft       bool       `json:"draft"`
				PublishedAt *time.Time `json:"published_at"`
			}
			if err := json.UnmarshalRead(body, &raw); err != nil {
				return fmt.Errorf("malformed response: %w", err)
			}
			rel.IsDraft, rel.PublishedAt = raw.Draft, raw.PublishedAt
			return nil
		})
		if err != nil {
			return time.Time{}, err
		}
	}
	if rel.IsDraft || rel.PublishedAt == nil {
		return time.Time{}, nil
	}
	return *rel.PublishedAt, nil
}

// assetEntry is one deduped GraphQL asset digest lookup.
type assetEntry struct {
	once   sync.Once
	digest string
	err    error
}

const assetQuery = `query($owner:String!,$name:String!,$tag:String!,$asset:String!){repository(owner:$owner,name:$name){` +
	`release(tagName:$tag){releaseAssets(first:1,name:$asset){nodes{name digest}}}}}`

// AssetDigest returns the API digest ("sha256:<hex>") of asset in the release of tag, or ""
// when the release has no such asset or GitHub has no digest for it. Over REST it comes from
// the cached list (which must already list tag); over GraphQL it is fetched once per
// (repository, tag, asset).
func (c *Client) AssetDigest(ctx context.Context, r Repo, tag, asset string) (string, error) {
	if !c.GraphQL(r) {
		var digest string
		err := c.Releases(ctx, r, func(l *List) bool { _, ok := l.Find(tag); return ok }, func(l *List) {
			rel, _ := l.Find(tag)
			for _, a := range rel.assets {
				if a.Name == asset {
					digest = a.Digest
					return
				}
			}
		})
		return digest, err
	}
	key := strings.ToLower(c.rest(r)+"/repos/"+r.Path()) + "@" + tag + "/" + asset
	c.mu.Lock()
	e := c.assets[key]
	if e == nil {
		e = &assetEntry{}
		c.assets[key] = e
	}
	c.mu.Unlock()
	e.once.Do(func() {
		var data struct {
			Repository *struct {
				Release *struct {
					ReleaseAssets struct {
						Nodes []struct {
							Name   string  `json:"name"`
							Digest *string `json:"digest"`
						} `json:"nodes"`
					} `json:"releaseAssets"`
				} `json:"release"`
			} `json:"repository"`
		}
		err := graphql(ctx, c, r, assetQuery, map[string]any{"owner": r.Owner, "name": r.Name, "tag": tag, "asset": asset}, &data)
		if err != nil {
			e.err = fmt.Errorf("release %s of %s: asset %s: %w", tag, r.Path(), asset, err)
			return
		}
		if data.Repository == nil || data.Repository.Release == nil {
			return
		}
		for _, a := range data.Repository.Release.ReleaseAssets.Nodes {
			if a.Name == asset && a.Digest != nil {
				e.digest = *a.Digest
			}
		}
		slog.DebugContext(ctx, "github asset digest", "repo", r.Path(), "tag", tag, "asset", asset, "digest", e.digest)
	})
	return e.digest, e.err
}

const assetsQuery = `query($owner:String!,$name:String!,$tag:String!){repository(owner:$owner,name:$name){` +
	`release(tagName:$tag){releaseAssets(first:100){nodes{name digest}}}}}`

// AssetByDigest returns the name of the asset of the release of tag whose API digest is
// digest ("sha256:<hex>", compared case-insensitively), or "" when none has it (first 100
// assets over GraphQL). Renovate's github-release-attachments digests name no asset; this
// finds it.
func (c *Client) AssetByDigest(ctx context.Context, r Repo, tag, digest string) (string, error) {
	if !c.GraphQL(r) {
		var name string
		err := c.Releases(ctx, r, func(l *List) bool { _, ok := l.Find(tag); return ok }, func(l *List) {
			rel, _ := l.Find(tag)
			for _, a := range rel.assets {
				if strings.EqualFold(a.Digest, digest) {
					name = a.Name
					return
				}
			}
		})
		return name, err
	}
	var data struct {
		Repository *struct {
			Release *struct {
				ReleaseAssets struct {
					Nodes []struct {
						Name   string  `json:"name"`
						Digest *string `json:"digest"`
					} `json:"nodes"`
				} `json:"releaseAssets"`
			} `json:"release"`
		} `json:"repository"`
	}
	if err := graphql(ctx, c, r, assetsQuery, map[string]any{"owner": r.Owner, "name": r.Name, "tag": tag}, &data); err != nil {
		return "", fmt.Errorf("release %s of %s: assets: %w", tag, r.Path(), err)
	}
	if data.Repository == nil || data.Repository.Release == nil {
		return "", nil
	}
	for _, a := range data.Repository.Release.ReleaseAssets.Nodes {
		if a.Digest != nil && strings.EqualFold(*a.Digest, digest) {
			return a.Name, nil
		}
	}
	return "", nil
}

// Note is a release with its notes, for pull request bodies.
type Note struct {
	Tag, URL, Body    string
	Draft, Prerelease bool
}

const notesQuery = `query($owner:String!,$name:String!,$first:Int!){repository(owner:$owner,name:$name){` +
	`releases(first:$first,orderBy:{field:CREATED_AT,direction:DESC}){nodes{tagName url description isDraft isPrerelease}}}}`

// Notes calls each for the newest first releases of r (drafts included), newest first, with
// their bodies: one GraphQL query when a token for r's host is known, else one REST page that
// is decoded release by release. Results are not cached.
func (c *Client) Notes(ctx context.Context, r Repo, first int, each func(Note)) error {
	var err error
	if c.GraphQL(r) {
		err = c.graphqlNotes(ctx, r, first, each)
	} else {
		err = c.restNotes(ctx, r, first, each)
	}
	if err != nil {
		return fmt.Errorf("release notes of %s: %w", r.Path(), err)
	}
	return nil
}

func (c *Client) graphqlNotes(ctx context.Context, r Repo, first int, each func(Note)) error {
	var data struct {
		Repository *struct {
			Releases struct {
				Nodes []struct {
					TagName      string `json:"tagName"`
					URL          string `json:"url"`
					Description  string `json:"description"`
					IsDraft      bool   `json:"isDraft"`
					IsPrerelease bool   `json:"isPrerelease"`
				} `json:"nodes"`
			} `json:"releases"`
		} `json:"repository"`
	}
	if err := graphql(ctx, c, r, notesQuery, map[string]any{"owner": r.Owner, "name": r.Name, "first": first}, &data); err != nil {
		return err
	}
	if data.Repository == nil {
		return fmt.Errorf("repository: %w", ErrNotFound)
	}
	for _, n := range data.Repository.Releases.Nodes {
		each(Note{Tag: n.TagName, URL: n.URL, Body: n.Description, Draft: n.IsDraft, Prerelease: n.IsPrerelease})
	}
	return nil
}

func (c *Client) restNotes(ctx context.Context, r Repo, first int, each func(Note)) error {
	return c.restDo(ctx, r, "/repos/"+r.Path()+"/releases?per_page="+strconv.Itoa(first), false, func(body io.Reader) error {
		return decodeNotes(body, each)
	})
}

// decodeNotes decodes a REST release list release by release.
func decodeNotes(body io.Reader, each func(Note)) error {
	dec := jsontext.NewDecoder(body, jsontext.AllowDuplicateNames(true)) // no per-object name sets
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return errors.New("malformed response: not a release list")
	}
	for dec.PeekKind() != ']' {
		var raw struct {
			TagName    string `json:"tag_name"`
			HTMLURL    string `json:"html_url"`
			Body       string `json:"body"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.UnmarshalDecode(dec, &raw); err != nil {
			return fmt.Errorf("malformed response: %w", err)
		}
		each(Note{Tag: raw.TagName, URL: raw.HTMLURL, Body: raw.Body, Draft: raw.Draft, Prerelease: raw.Prerelease})
	}
	return nil
}
