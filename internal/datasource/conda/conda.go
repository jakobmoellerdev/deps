// Package conda lists the releases of conda packages from the anaconda.org API, one channel
// after the other. Lookups run once per (channel, package) and are shared by every dep and
// environment file of a run.
package conda

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the datasource name (Renovate's).
const Name = "conda"

// DefaultURL is the anaconda.org API; DEPS_CONDA_URL overrides it (a mirror serving its API).
const DefaultURL = "https://api.anaconda.org"

// DefaultChannel is the channel of a dep without one (Renovate's and Dependabot's default).
const DefaultChannel = "conda-forge"

// mainLabel is the label of a channel's installable files: conda installs from
// `<channel>/label/main` unless told otherwise, so files only under other labels (`rc`, `dev`,
// `broken`, the `cf2019xx` migration labels) are not releases.
const (
	mainLabel   = "main"
	brokenLabel = "broken"
)

// errNotFound marks a channel that does not have the package (HTTP 404).
var errNotFound = errors.New("not found")

var condaScheme, _ = versioning.Get("conda")

// Source is the conda datasource.
type Source struct {
	client *http.Client
	sem    chan struct{}
	base   string // the API base, without a trailing slash

	pkgs datasource.Memo[string, *pkg] // by channel + "\x00" + name
}

// pkg is what a channel lists for a package: every installable version once (the builds of
// every platform merged), the earliest upload time of each, and the development URL.
type pkg struct {
	rels  []datasource.Release
	times map[string]time.Time
	repo  string
}

// New returns the conda datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies DEPS_CONDA_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{client: client, sem: sem, base: strings.TrimSuffix(cmp.Or(getenv("DEPS_CONDA_URL"), DefaultURL), "/")}
}

// Releases lists the versions of d that have a file under the `main` label of the first
// channel that has the package. d.Source holds the channels in lookup order, whitespace-
// separated ("" = conda-forge); a name `<channel>/<name>` (Renovate's packageName form) names
// its channel. The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the earliest `upload_time` of the version's
// main files. A version written at a lower precision than published (`2.3` for 2.3.0) gets the
// time of the release it equals.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	if t, ok := p.times[version]; ok {
		return t, nil
	}
	for _, r := range p.rels {
		if condaScheme.Valid(version) && condaScheme.Valid(r.Version) && condaScheme.Compare(r.Version, version) == 0 {
			return p.times[r.Version], nil
		}
	}
	return time.Time{}, nil
}

// Digest is unsupported: conda packages are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// SourceRepo implements datasource.SourceRepo with the package's `dev_url` (an http(s) URL)
// on the channel that has it.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return "", err
	}
	return p.repo, nil
}

// split returns the channels of d in lookup order and the package name.
func split(d dep.Dep) (channels []string, name string) {
	if f := strings.Fields(d.Source); len(f) > 0 {
		return f, d.Name
	}
	if ch, n, ok := strings.Cut(d.Name, "/"); ok {
		return []string{ch}, n
	}
	return []string{DefaultChannel}, d.Name
}

// lookup returns the listing of d's package on the first of its channels that has it.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	channels, name := split(d)
	for _, ch := range channels {
		p, err := s.pkgs.Do(ctx, ch+"\x00"+name, func() (*pkg, error) { return s.fetch(ctx, ch, name) })
		if !errors.Is(err, errNotFound) {
			return p, err
		}
	}
	return nil, fmt.Errorf("conda package %s not found in %s", name, strings.Join(channels, ", "))
}

// response is the part of `GET /package/<channel>/<name>` deps reads; the files' descriptions
// and dependencies are skipped while decoding.
type response struct {
	Versions []string `json:"versions"`
	DevURL   string   `json:"dev_url"`
	Files    []struct {
		Version    string   `json:"version"`
		Labels     []string `json:"labels"`
		UploadTime string   `json:"upload_time"`
	} `json:"files"`
}

// uploadLayout is the format of `upload_time` (`2026-10-11 00:56:51.847000+00:00`).
const uploadLayout = "2006-01-02 15:04:05.999999-07:00"

// fetch reads `<base>/package/<channel>/<name>`. Versions come from the files under the main
// label that are not marked broken; a listing without files falls back to `versions`.
func (s *Source) fetch(ctx context.Context, channel, name string) (p *pkg, err error) {
	start := time.Now()
	defer func() {
		n := 0
		if p != nil {
			n = len(p.rels)
		}
		slog.DebugContext(ctx, "conda package fetched", "channel", channel, "name", name, "versions", n, "duration", time.Since(start), "err", err)
	}()
	var resp response
	target := s.base + "/package/" + url.PathEscape(channel) + "/" + url.PathEscape(name)
	if err := s.get(ctx, target, func(r io.Reader) error { return json.UnmarshalRead(r, &resp) }); err != nil {
		return nil, err
	}
	p = &pkg{times: map[string]time.Time{}}
	if u, err := url.Parse(resp.DevURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		p.repo = resp.DevURL
	}
	add := func(v string) {
		if _, seen := p.times[v]; !seen && v != "" {
			p.rels = append(p.rels, datasource.Release{Version: v})
			p.times[v] = time.Time{}
		}
	}
	if len(resp.Files) == 0 {
		for _, v := range resp.Versions {
			add(v)
		}
		return p, nil
	}
	for _, f := range resp.Files {
		if !slices.Contains(f.Labels, mainLabel) || slices.Contains(f.Labels, brokenLabel) {
			continue
		}
		add(f.Version)
		if t, err := time.Parse(uploadLayout, f.UploadTime); err == nil {
			if old := p.times[f.Version]; old.IsZero() || t.Before(old) {
				p.times[f.Version] = t
			}
		}
	}
	return p, nil
}

// get GETs target while holding a slot of the semaphore and passes a 200 body to read. A 404
// is errNotFound.
func (s *Source) get(ctx context.Context, target string, read func(io.Reader) error) error {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return fmt.Errorf("conda %s: invalid URL", target)
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("conda: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("conda %s: HTTP %d", target, resp.StatusCode)
	}
	if err := read(resp.Body); err != nil {
		return fmt.Errorf("conda %s: malformed response: %w", target, err)
	}
	return nil
}

// Channel returns the anaconda.org channel name of a channel as an environment file or a
// `<channel>::` prefix writes it: a name (`conda-forge`, `bioconda`, with a `/label/<x>` or
// `/<subdir>` suffix dropped), `defaults` (the `anaconda` channel, which mirrors Anaconda's
// main repository), or a `https://conda.anaconda.org/<channel>` or
// `https://repo.anaconda.com/pkgs/main` URL. ok is false for other URLs and local paths, which
// the anaconda.org API does not serve, and for `nodefaults`.
func Channel(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if u, err := url.Parse(s); err == nil && u.Scheme != "" {
		if u.Scheme != "https" && u.Scheme != "http" {
			return "", false
		}
		path := strings.Trim(u.Path, "/")
		switch strings.ToLower(u.Hostname()) {
		case "conda.anaconda.org", "anaconda.org":
			s = path
		case "repo.anaconda.com":
			if path != "pkgs/main" {
				return "", false
			}
			return "anaconda", true
		default:
			return "", false
		}
	}
	s, _, _ = strings.Cut(s, "/")
	switch s {
	case "", "nodefaults", ".", "..", "~", "local":
		return "", false
	case "defaults":
		return "anaconda", true
	}
	return s, true
}
