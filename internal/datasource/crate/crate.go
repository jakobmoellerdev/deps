// Package crate lists the versions of Rust crates from the crates.io sparse index
// (https://doc.rust-lang.org/cargo/reference/registry-index.html#sparse-protocol), not the
// rate-limited crates.io API, which is only asked for the source repository of a version.
package crate

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "crate"

// Default endpoints; DEPS_CRATES_INDEX_URL and DEPS_CRATES_API_URL override them (mirrors).
const (
	DefaultIndexURL = "https://index.crates.io"
	DefaultAPIURL   = "https://crates.io"
)

// Source fetches each crate's index file once per run; concurrent callers share the result.
type Source struct {
	client     *http.Client
	sem        chan struct{}
	index, api string

	mu     sync.Mutex
	crates map[string]*entry // lowercase name
	repos  map[string]*repoEntry
}

type entry struct {
	once sync.Once
	// rels are the published versions, lines the same followed by the derived release lines.
	rels, lines []datasource.Release
	times       map[string]time.Time // version or release line -> publication time
	latest      map[string]string    // release line -> newest version of the line
	newest      string               // the last published version
	err         error
}

type repoEntry struct {
	once sync.Once
	repo string
	err  error
}

// New returns the crate datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies DEPS_CRATES_INDEX_URL and DEPS_CRATES_API_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	index, api := DefaultIndexURL, DefaultAPIURL
	if v := getenv("DEPS_CRATES_INDEX_URL"); v != "" {
		index = v
	}
	if v := getenv("DEPS_CRATES_API_URL"); v != "" {
		api = v
	}
	return &Source{
		client: client, sem: sem,
		index: strings.TrimSuffix(index, "/"), api: strings.TrimSuffix(api, "/"),
		crates: make(map[string]*entry), repos: make(map[string]*repoEntry),
	}
}

// Releases returns the non-yanked versions of crate d.Name. For a partial requirement
// (`1`, `0.4`) the derived release lines "X.Y" and "X" of the stable versions follow, so
// the precision-keeping tag versioning has candidates (`0.4` -> `0.5`). The slice is shared
// between callers and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	e := s.load(ctx, d.Name)
	if strings.Count(d.Version, ".") < 2 {
		return e.lines, e.err
	}
	return e.rels, e.err
}

// ReleaseTime implements datasource.Timer with the index's `pubtime`; a release line has the
// time of its first release. Zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	e := s.load(ctx, d.Name)
	return e.times[version], e.err
}

// SourceRepo implements datasource.SourceRepo with the `repository` of version from
// `GET <api>/api/v1/crates/<name>/<version>`; a release line asks for its newest version.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	e := s.load(ctx, d.Name)
	if e.err != nil {
		return "", e.err
	}
	if v, ok := e.latest[version]; ok {
		version = v
	}
	if _, ok := e.times[version]; !ok {
		version = e.newest
	}
	key := strings.ToLower(d.Name) + "@" + version
	s.mu.Lock()
	r := s.repos[key]
	if r == nil {
		r = &repoEntry{}
		s.repos[key] = r
	}
	s.mu.Unlock()
	r.once.Do(func() { r.repo, r.err = s.fetchRepo(ctx, d.Name, version) })
	return r.repo, r.err
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", fmt.Errorf("%s: digests are not supported for %s", Name, d.Name)
}

func (s *Source) load(ctx context.Context, name string) *entry {
	key := strings.ToLower(name)
	s.mu.Lock()
	e := s.crates[key]
	if e == nil {
		e = &entry{}
		s.crates[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		start := time.Now()
		e.err = s.fetch(ctx, key, e)
		slog.DebugContext(ctx, "crate index", "name", name, "releases", len(e.rels), "duration", time.Since(start), "err", e.err)
	})
	return e
}

// IndexPath returns the sparse index path of the crate: `1/<n>`, `2/<n>`, `3/<c>/<n>` or
// `<ab>/<cd>/<n>` for the lowercase name n. ok is false for names Cargo does not allow.
func IndexPath(name string) (string, bool) {
	n := strings.ToLower(name)
	if n == "" || len(n) > 64 {
		return "", false
	}
	for i := range len(n) {
		if c := n[i]; !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	switch len(n) {
	case 1:
		return "1/" + n, true
	case 2:
		return "2/" + n, true
	case 3:
		return "3/" + n[:1] + "/" + n, true
	}
	return n[:2] + "/" + n[2:4] + "/" + n, true
}

// indexLine is the part of an index entry deps reads.
type indexLine struct {
	Vers    string `json:"vers"`
	Yanked  bool   `json:"yanked"`
	Pubtime string `json:"pubtime"`
}

func (s *Source) fetch(ctx context.Context, name string, e *entry) error {
	p, ok := IndexPath(name)
	if !ok {
		return fmt.Errorf("invalid crate name %q", name)
	}
	u := s.index + "/" + p
	body, err := s.get(ctx, u)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return fmt.Errorf("crate %s not found", name)
		}
		return err
	}
	defer body.Close()
	e.times, e.latest = map[string]time.Time{}, map[string]string{}
	var derived []datasource.Release
	dec := jsontext.NewDecoder(body, jsontext.AllowDuplicateNames(true))
	for {
		var l indexLine
		if err := json.UnmarshalDecode(dec, &l); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("GET %s: %w", u, err)
		}
		if l.Yanked || l.Vers == "" {
			continue
		}
		e.rels = append(e.rels, datasource.Release{Version: l.Vers})
		e.newest = l.Vers
		t, terr := time.Parse(time.RFC3339, l.Pubtime)
		if terr == nil {
			e.times[l.Vers] = t
		}
		if strings.ContainsAny(l.Vers, "-+") || strings.Count(l.Vers, ".") != 2 {
			continue // prereleases belong to no release line
		}
		minor := l.Vers[:strings.LastIndexByte(l.Vers, '.')]
		for _, line := range []string{minor, l.Vers[:strings.IndexByte(l.Vers, '.')]} {
			prev, seen := e.latest[line]
			if !seen {
				derived = append(derived, datasource.Release{Version: line})
				if terr == nil {
					e.times[line] = t // the index is in publication order: the line's first release
				}
			}
			if !seen || newer(l.Vers, prev) {
				e.latest[line] = l.Vers
			}
		}
	}
	if len(e.rels) == 0 {
		return fmt.Errorf("crate %s has no releases", name)
	}
	e.lines = append(append(make([]datasource.Release, 0, len(e.rels)+len(derived)), e.rels...), derived...)
	return nil
}

// newer compares two stable MAJOR.MINOR.PATCH versions numerically.
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range pa {
		if len(pa[i]) != len(pb[i]) {
			return len(pa[i]) > len(pb[i])
		}
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func (s *Source) fetchRepo(ctx context.Context, name, version string) (string, error) {
	u := s.api + "/api/v1/crates/" + url.PathEscape(name) + "/" + url.PathEscape(version)
	body, err := s.get(ctx, u)
	if errors.Is(err, errNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer body.Close()
	var doc struct {
		Version struct {
			Repository string `json:"repository"`
		} `json:"version"`
	}
	if err := json.UnmarshalRead(body, &doc); err != nil {
		return "", fmt.Errorf("GET %s: %w", u, err)
	}
	return doc.Version.Repository, nil
}

var errNotFound = errors.New("not found")

// get sends a GET holding a semaphore slot until the body is closed. 404 and 410 (the sparse
// index's answer for unknown crates) are errNotFound.
func (s *Source) get(ctx context.Context, u string) (io.ReadCloser, error) {
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	release := func() {
		if s.sem != nil {
			<-s.sem
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		release()
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		release()
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return releaseOnClose{resp.Body, release}, nil
	case http.StatusNotFound, http.StatusGone:
		err = errNotFound
	default:
		err = fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	resp.Body.Close()
	release()
	return nil, err
}

type releaseOnClose struct {
	io.ReadCloser
	release func()
}

func (r releaseOnClose) Close() error {
	err := r.ReadCloser.Close()
	r.release()
	return err
}
