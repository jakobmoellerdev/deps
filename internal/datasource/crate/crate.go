// Package crate lists the versions of Rust crates from a Cargo registry index: the crates.io
// sparse index (https://doc.rust-lang.org/cargo/reference/registry-index.html#sparse-protocol)
// rather than the rate-limited crates.io API, which is only asked for the source repository of
// a version, and alternative registries (sparse or git) from Cargo's configuration.
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
	"slices"
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

// Source fetches each crate's index file once per run and registry; concurrent callers share
// the result.
type Source struct {
	client     *http.Client
	sem        chan struct{}
	auth       *httpx.Auth // credentials for registry hosts without a Cargo token; nil = none
	getenv     func(string) string
	index, api string // crates.io sparse index and web API

	mu         sync.Mutex
	crates     map[string]*entry       // index + "\x00" + lowercase name
	repos      map[string]*repoEntry   // api + "\x00" + name@version
	registries map[string]*registry    // index URL + "\x00" + token
	configs    map[string]*configEntry // manifest directory -> Cargo configuration
	located    map[string]*registry    // file + "\x00" + name -> registry of the Releases lookup
	clones     []string                // git index clones, removed by Close
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

// New returns the crate datasource. sem bounds concurrent requests (nil = unbounded); auth
// supplies the static token and git credential helper for registries without a Cargo token
// (nil = none); getenv supplies DEPS_CRATES_INDEX_URL, DEPS_CRATES_API_URL, CARGO_HOME and the
// CARGO_REGISTRIES_<NAME>_INDEX/_TOKEN variables.
func New(client *http.Client, sem chan struct{}, auth *httpx.Auth, getenv func(string) string) *Source {
	index, api := DefaultIndexURL, DefaultAPIURL
	if v := getenv("DEPS_CRATES_INDEX_URL"); v != "" {
		index = v
	}
	if v := getenv("DEPS_CRATES_API_URL"); v != "" {
		api = v
	}
	return &Source{
		client: client, sem: sem, auth: auth, getenv: getenv,
		index: strings.TrimSuffix(index, "/"), api: strings.TrimSuffix(api, "/"),
		crates: make(map[string]*entry), repos: make(map[string]*repoEntry),
		registries: make(map[string]*registry), configs: make(map[string]*configEntry), located: make(map[string]*registry),
	}
}

// Releases returns the non-yanked versions of crate d.Name in its registry (registryOf; dir
// is the manifest's absolute directory). For a partial requirement (`1`, `0.4`) the derived
// release lines "X.Y" and "X" of the stable versions follow, so the precision-keeping cargo
// versioning has candidates (`0.4` -> `0.5`). The slice is shared between callers and must
// not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	r, err := s.registryOf(d, dir)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.located[d.File+"\x00"+d.Name] = r
	s.mu.Unlock()
	e := s.load(ctx, r, d.Name)
	if strings.Count(d.Version, ".") < 2 {
		return e.lines, e.err
	}
	return e.rels, e.err
}

// locate returns the registry of d's Releases lookup, else crates.io.
func (s *Source) locate(d dep.Dep) *registry {
	s.mu.Lock()
	r := s.located[d.File+"\x00"+d.Name]
	s.mu.Unlock()
	if r == nil {
		r = s.registry("crates-io", "sparse+"+s.index, "", s.api)
	}
	return r
}

// ReleaseTime implements datasource.Timer with the index's `pubtime`; a release line has the
// time of its first release. Zero when unknown (registries that do not write pubtime).
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	e := s.load(ctx, s.locate(d), d.Name)
	return e.times[version], e.err
}

// SourceRepo implements datasource.SourceRepo with the `repository` of version from
// `GET <api>/api/v1/crates/<name>/<version>` (the registry's `api` from its config.json;
// "" without one); a release line asks for its newest version.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	r := s.locate(d)
	e := s.load(ctx, r, d.Name)
	if e.err != nil {
		return "", e.err
	}
	if v, ok := e.latest[version]; ok {
		version = v
	}
	if _, ok := e.times[version]; !ok && !slices.ContainsFunc(e.rels, func(rel datasource.Release) bool { return rel.Version == version }) {
		version = e.newest
	}
	api, err := s.apiOf(ctx, r)
	if err != nil || api == "" {
		return "", err
	}
	key := api + "\x00" + strings.ToLower(d.Name) + "@" + version
	s.mu.Lock()
	re := s.repos[key]
	if re == nil {
		re = &repoEntry{}
		s.repos[key] = re
	}
	s.mu.Unlock()
	re.once.Do(func() { re.repo, re.err = s.fetchRepo(ctx, r, api, d.Name, version) })
	return re.repo, re.err
}

// apiOf returns the web API base of r: crates.io's, else the `api` of the index's
// config.json, read once.
func (s *Source) apiOf(ctx context.Context, r *registry) (string, error) {
	if r.api != "" {
		return r.api, nil
	}
	s.mu.Lock()
	e := s.repos["config\x00"+r.index]
	if e == nil {
		e = &repoEntry{}
		s.repos["config\x00"+r.index] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		body, err := s.open(ctx, r, "config.json")
		if errors.Is(err, errNotFound) {
			return
		}
		if err != nil {
			e.err = err
			return
		}
		defer body.Close()
		var cfg struct {
			API string `json:"api"`
		}
		if err := json.UnmarshalRead(body, &cfg); err != nil {
			e.err = fmt.Errorf("%s config.json: %w", r.index, err)
			return
		}
		e.repo = strings.TrimSuffix(cfg.API, "/")
	})
	return e.repo, e.err
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

func (s *Source) load(ctx context.Context, r *registry, name string) *entry {
	key := r.index + "\x00" + strings.ToLower(name)
	s.mu.Lock()
	e := s.crates[key]
	if e == nil {
		e = &entry{}
		s.crates[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		start := time.Now()
		e.err = s.fetch(ctx, r, strings.ToLower(name), e)
		slog.DebugContext(ctx, "crate index", "registry", r.index, "name", name, "releases", len(e.rels), "duration", time.Since(start), "err", e.err)
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

func (s *Source) fetch(ctx context.Context, r *registry, name string, e *entry) error {
	p, ok := IndexPath(name)
	if !ok {
		return fmt.Errorf("invalid crate name %q", name)
	}
	u := r.index + "/" + p
	body, err := s.open(ctx, r, p)
	if err != nil {
		if errors.Is(err, errNotFound) {
			if r.name != "crates-io" {
				return fmt.Errorf("crate %s not found in registry %s", name, r.name)
			}
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
			return fmt.Errorf("%s: %w", u, err)
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

func (s *Source) fetchRepo(ctx context.Context, r *registry, api, name, version string) (string, error) {
	u := api + "/api/v1/crates/" + url.PathEscape(name) + "/" + url.PathEscape(version)
	body, err := s.get(ctx, r, u)
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

// get sends a GET holding a semaphore slot until the body is closed. A request to r's index
// host carries r's Cargo token as `Authorization` (as Cargo sends it); without a token, the
// static token for the host as Basic auth, and after a 401 the git credential helper's. Other
// hosts (a config.json `api` elsewhere) get no credentials. 404 and 410 (the sparse index's
// answer for unknown crates) are errNotFound.
func (s *Source) get(ctx context.Context, r *registry, u string) (io.ReadCloser, error) {
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
	var user, pass string // the git credential helper's, after a 401
	for helped := false; ; {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
		if err != nil {
			release()
			return nil, err
		}
		req.Header.Set("User-Agent", httpx.UserAgent())
		own := r.owns(req.URL)
		switch {
		case !own:
		case r.token != "":
			req.Header.Set("Authorization", r.token) // Cargo sends registry tokens as is
		case helped:
			req.SetBasicAuth(user, pass)
		default:
			if su, sp, ok := s.auth.Static(req.URL.Hostname()); ok {
				req.SetBasicAuth(su, sp)
			}
		}
		resp, err := s.client.Do(req)
		if err != nil {
			release()
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusOK:
			return releaseOnClose{resp.Body, release}, nil
		case http.StatusUnauthorized:
			if own && r.token == "" && !helped {
				if user, pass, helped = s.auth.Helper(ctx, req.URL.Host); helped {
					resp.Body.Close()
					continue
				}
			}
			err = fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
		case http.StatusNotFound, http.StatusGone:
			err = errNotFound
		default:
			err = fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
		}
		resp.Body.Close()
		release()
		return nil, err
	}
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
