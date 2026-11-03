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

type entry struct {
	once sync.Once
	rels []datasource.Release
	err  error
}

// New returns the github-releases datasource. sem bounds concurrent requests and is shared with
// other datasources; auth may be nil for anonymous access.
func New(client *http.Client, auth *httpx.Auth, sem chan struct{}) datasource.Datasource {
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

// Releases lists the non-draft, non-prerelease releases of d.Name (newest first, at most
// 500); Version is the tag name and Digest is empty. The slice is shared between callers and
// must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	r, err := s.parseRepo(d.Name)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(r.api + "/repos/" + r.path) // GitHub names are case-insensitive
	s.mu.Lock()
	e := s.cache[key]
	if e == nil {
		e = &entry{}
		s.cache[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.rels, e.err = s.fetch(ctx, r) })
	return e.rels, e.err
}

// Digest is unsupported: releases carry no digest.
func (*Source) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

func (s *Source) fetch(ctx context.Context, r repo) (rels []datasource.Release, err error) {
	start, pages := time.Now(), 0
	defer func() {
		slog.DebugContext(ctx, "github releases listed", "repo", r.path, "api", r.api, "pages", pages,
			"releases", len(rels), "duration", time.Since(start), "err", err)
	}()
	_, token, authed := s.auth.Static(r.host)
	for page := 1; page <= maxPages; page++ {
		n, err := s.page(ctx, r, page, token, authed, &rels)
		pages = page
		if err != nil {
			return nil, err
		}
		if n < perPage {
			break
		}
	}
	return rels, nil
}

type release struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// page fetches one page, appends its published releases to rels and returns the number of
// entries on the page. The array is decoded element by element so only one release is buffered.
func (s *Source) page(ctx context.Context, r repo, page int, token string, authed bool, rels *[]datasource.Release) (int, error) {
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
