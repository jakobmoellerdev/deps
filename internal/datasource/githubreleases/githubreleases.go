// Package githubreleases lists the published releases of a GitHub (or GitHub Enterprise Server)
// repository through the REST API.
package githubreleases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name.
const Name = "github-releases"

const (
	perPage  = 100
	maxPages = 5
)

// Source fetches releases once per repository per run; concurrent callers share the result.
type Source struct {
	client    *http.Client
	auth      *httpx.Auth
	sem       chan struct{}
	githubAPI string // API base for github.com repositories

	mu    sync.Mutex
	cache map[string]*entry
}

// entry is the release list of one repository, fetched page by page on demand.
type entry struct {
	mu    sync.Mutex
	pages int  // pages fetched
	done  bool // last page seen, maxPages reached, or an error
	rels  []datasource.Release
	byTag map[string]tagInfo
	err   error
}

// tagInfo is what deps keeps of a published release besides its tag name.
type tagInfo struct {
	assets    []Asset
	published time.Time
}

// Asset is a release asset; Digest is the API `digest` ("sha256:<hex>"), "" if GitHub has none.
type Asset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	URL    string `json:"browser_download_url"`
}

// New returns the github-releases datasource. sem bounds concurrent requests and is shared with
// other datasources; auth may be nil for anonymous access.
func New(client *http.Client, auth *httpx.Auth, sem chan struct{}) *Source {
	return &Source{client: client, auth: auth, sem: sem, githubAPI: "https://api.github.com", cache: make(map[string]*entry)}
}

// repo is a parsed repository reference.
type repo struct {
	api, host, path string // path = "owner/repo"
}

// parseRepo accepts `owner/repo` (github.com) or `http(s)://host/owner/repo` (GitHub Enterprise
// Server, API at `<scheme>://host/api/v3`; github.com URLs use the public API).
func (s *Source) parseRepo(name string) (repo, error) {
	r := repo{api: s.githubAPI, host: "github.com", path: name}
	if strings.Contains(name, "://") {
		u, err := url.Parse(name)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return repo{}, fmt.Errorf("invalid github repository %q", name)
		}
		r.host, r.path = u.Hostname(), strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if !strings.EqualFold(r.host, "github.com") {
			r.api = u.Scheme + "://" + u.Host + "/api/v3"
		}
	}
	owner, rest, ok := strings.Cut(r.path, "/")
	if !ok || owner == "" || rest == "" || strings.Contains(rest, "/") {
		return repo{}, fmt.Errorf("invalid github repository %q", name)
	}
	return r, nil
}

// Releases lists the non-draft, non-prerelease releases of d.Name, newest first. Pages are
// fetched only until d.Version's release is listed (everything newer precedes it; at most
// 500 releases); Version is the tag name and Digest is empty. The slice must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	var rels []datasource.Release
	err := s.with(ctx, d.Name, d.Version, func(e *entry) { rels = slices.Clip(e.rels) })
	return rels, err
}

// with runs fn on the repository's entry after fetching pages until version's release is
// listed or no pages are left; fn runs under the entry lock.
func (s *Source) with(ctx context.Context, name, version string, fn func(e *entry)) error {
	r, err := s.parseRepo(name)
	if err != nil {
		return err
	}
	key := strings.ToLower(r.api + "/repos/" + r.path) // GitHub names are case-insensitive
	s.mu.Lock()
	e := s.cache[key]
	if e == nil {
		e = &entry{byTag: make(map[string]tagInfo)}
		s.cache[key] = e
	}
	s.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	for !e.done && (e.pages == 0 || !e.has(version)) {
		s.next(ctx, r, e)
	}
	if e.err != nil {
		return e.err
	}
	fn(e)
	return nil
}

func (e *entry) has(version string) bool {
	_, _, ok := e.release(version)
	return ok
}

// next fetches the entry's next page; a short page, maxPages or an error ends the list.
func (s *Source) next(ctx context.Context, r repo, e *entry) {
	start := time.Now()
	_, token, authed := s.auth.Static(r.host)
	n, err := s.page(ctx, r, e.pages+1, token, authed, &e.rels, e.byTag)
	e.pages++
	slog.DebugContext(ctx, "github releases page", "repo", r.path, "api", r.api, "page", e.pages,
		"releases", len(e.rels), "duration", time.Since(start), "err", err)
	switch {
	case err != nil:
		e.err, e.done = err, true
	case n < perPage || e.pages >= maxPages:
		e.done = true
	}
}

// AssetDigest returns the hex sha256 of asset in the release of version of repository name
// (see parseRepo), from the cached release list. version matches the tag exactly or with a
// leading "v" added or removed.
func (s *Source) AssetDigest(ctx context.Context, name, version, asset string) (string, error) {
	var tag string
	var info tagInfo
	var ok bool
	if err := s.with(ctx, name, version, func(e *entry) { tag, info, ok = e.release(version) }); err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no release %s in %s", version, name)
	}
	for _, a := range info.assets {
		if a.Name != asset {
			continue
		}
		if algo, hex, ok := strings.Cut(a.Digest, ":"); ok && algo == "sha256" && hex != "" {
			return hex, nil
		}
		break
	}
	return "", fmt.Errorf("release %s has no digest for asset %s", tag, asset)
}

// release returns the published release of version: the tag equal to version, or to version
// with a leading "v" added or removed.
func (e *entry) release(version string) (tag string, info tagInfo, ok bool) {
	if info, ok = e.byTag[version]; ok {
		return version, info, true
	}
	if tag, ok = strings.CutPrefix(version, "v"); !ok {
		tag = "v" + version
	}
	info, ok = e.byTag[tag]
	return tag, info, ok
}

// ReleaseTime implements datasource.Timer with the `published_at` of version's release (see
// AssetDigest for the tag match) from the cached release list; zero when there is none.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	var published time.Time
	err := s.with(ctx, d.Name, version, func(e *entry) {
		_, info, _ := e.release(version)
		published = info.published
	})
	return published, err
}

// Digest is unsupported: releases carry no digest.
func (*Source) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

type release struct {
	TagName     string     `json:"tag_name"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	Assets      []Asset    `json:"assets"`
	PublishedAt *time.Time `json:"published_at"`
}

// page fetches one page, appends its published releases to rels and returns the number of
// entries on the page. The array is decoded element by element so only one release is buffered.
func (s *Source) page(ctx context.Context, r repo, page int, token string, authed bool, rels *[]datasource.Release, byTag map[string]tagInfo) (int, error) {
	if err := s.acquire(ctx); err != nil {
		return 0, err
	}
	defer s.release()
	target := r.api + "/repos/" + r.path + "/releases?per_page=" + strconv.Itoa(perPage) + "&page=" + strconv.Itoa(page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("releases %s: %w", r.path, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", httpx.UserAgent())
	if authed {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("releases %s: %w", r.path, err)
	}
	defer closeBody(resp)
	h := resp.Header
	slog.DebugContext(ctx, "github rate limit", "repo", r.path, "page", page, "authenticated", authed,
		"limit", h.Get("X-RateLimit-Limit"), "remaining", h.Get("X-RateLimit-Remaining"), "reset", h.Get("X-RateLimit-Reset"))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if h.Get("X-RateLimit-Remaining") == "0" {
			return 0, fmt.Errorf("releases %s: HTTP %d: rate limit exceeded (resets %s)", r.path, resp.StatusCode, resetTime(h.Get("X-RateLimit-Reset")))
		}
		return 0, fmt.Errorf("releases %s: HTTP %d", r.path, resp.StatusCode)
	}

	dec := json.NewDecoder(resp.Body)
	tok, err := dec.Token()
	if err == nil && tok != json.Delim('[') {
		err = errors.New("not an array")
	}
	if err != nil {
		return 0, fmt.Errorf("releases %s: malformed response: %w", r.path, err)
	}
	n := 0
	for dec.More() {
		var rel release
		if err := dec.Decode(&rel); err != nil {
			return 0, fmt.Errorf("releases %s: malformed response: %w", r.path, err)
		}
		n++
		if !rel.Draft && !rel.Prerelease && rel.TagName != "" {
			*rels = append(*rels, datasource.Release{Version: rel.TagName})
			info := tagInfo{assets: rel.Assets}
			if rel.PublishedAt != nil {
				info.published = *rel.PublishedAt
			}
			byTag[rel.TagName] = info
		}
	}
	if _, err := dec.Token(); err != nil {
		return 0, fmt.Errorf("releases %s: malformed response: %w", r.path, err)
	}
	return n, nil
}

// resetTime renders the X-RateLimit-Reset epoch seconds as RFC 3339, or the raw value.
func resetTime(v string) string {
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return v
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func (s *Source) acquire(ctx context.Context) error {
	if s.sem == nil {
		return nil
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Source) release() {
	if s.sem != nil {
		<-s.sem
	}
}

// closeBody drains a little of the body so the connection can be reused, then closes it.
func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}
