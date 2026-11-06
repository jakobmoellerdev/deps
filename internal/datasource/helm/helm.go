// Package helmds lists Helm chart versions from classic chart repositories and OCI registries.
//
// A classic repository (`https://…`) is read from its index.yaml once per run. The index is
// stream-scanned and only the charts some dep asks for are decoded and kept: the engine hands
// the datasource every helm dep before the first lookup (datasource.Planner), and a chart
// asked for later costs one more scan. Release times are the index's `created`.
//
// An OCI repository (`oci://host/path`) holds one repository per chart (`host/path/<chart>`);
// its tags are listed with the docker datasource (same credentials, token cache and Docker Hub
// handling), with Helm's `_` for `+` mapped back. Release times are the manifest's
// `org.opencontainers.image.created` annotation, which `helm push` sets.
package helmds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name.
const Name = "helm"

// ociScheme prefixes OCI repositories.
const ociScheme = "oci://"

// Release is one chart version. Created is zero when unknown; Digest is the chart archive's
// sha256 from a classic index ("" for OCI).
type Release struct {
	Version, AppVersion string
	Created             time.Time
	Digest              string
}

// Source looks up chart versions; every index and tag list is fetched once per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	auth   *httpx.Auth           // nil = anonymous
	oci    datasource.Datasource // lists OCI tags (the docker datasource)

	mu      sync.Mutex
	wanted  map[string]map[string]bool       // classic repository URL -> charts asked for so far
	indexes datasource.Memo[string, *index]  // classic repository URL
	charts  datasource.Memo[string, *result] // repository + "\x00" + chart, for OCI and late classic lookups
}

// index is one scan of a classic repository's index.yaml.
type index struct {
	wanted map[string]bool // the charts the scan kept
	charts map[string]*result
}

// result is the releases of one chart, also converted for the engine.
type result struct {
	rels []Release
	out  []datasource.Release
	err  error
}

func newResult(rels []Release, err error) *result {
	r := &result{rels: rels, err: err}
	if err == nil {
		r.out = make([]datasource.Release, len(rels))
		for i, rel := range rels {
			r.out[i] = datasource.Release{Version: rel.Version}
		}
	}
	return r
}

// New returns the helm datasource. Requests go through client; sem bounds concurrent requests
// and is shared with the other datasources; auth supplies Basic credentials for classic
// repositories on the forge's host; oci lists OCI tags (the docker datasource).
func New(client *http.Client, sem chan struct{}, auth *httpx.Auth, oci datasource.Datasource) *Source {
	return &Source{
		client: client, sem: sem, auth: auth, oci: oci,
		wanted: map[string]map[string]bool{},
	}
}

// Plan records the charts of the classic repositories the run will ask for, so one index scan
// keeps all of them (datasource.Planner).
func (s *Source) Plan(deps []dep.Dep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range deps {
		if d.Source != "" && !strings.HasPrefix(d.Source, ociScheme) {
			s.want(d.Source, d.Name)
		}
	}
}

// want registers chart of repo; s.mu must be held.
func (s *Source) want(repo, chart string) {
	w := s.wanted[repo]
	if w == nil {
		w = map[string]bool{}
		s.wanted[repo] = w
	}
	w[chart] = true
}

// Chart returns the versions of chart in repo (`https://…` or `oci://host/path`), in
// repository order. The slice is shared between callers and must not be modified.
func (s *Source) Chart(ctx context.Context, repo, chart string) ([]Release, error) {
	r := s.lookup(ctx, repo, chart)
	return r.rels, r.err
}

// Releases lists the versions of chart d.Name in repository d.Source (datasource.Datasource).
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	r := s.lookup(ctx, d.Source, d.Name)
	return r.out, r.err
}

// Digest is unsupported: chart versions are not digest-pinned.
func (*Source) Digest(context.Context, dep.Dep, string) (string, error) {
	return "", errors.New("helm: digests are not supported")
}

// ReleaseTime implements datasource.Timer: the index's `created` for classic repositories, the
// manifest's creation annotation for OCI. A version matches with or without its "v" prefix.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	r := s.lookup(ctx, d.Source, d.Name)
	if r.err != nil {
		return time.Time{}, r.err
	}
	rel, ok := find(r.rels, version)
	if !ok {
		return time.Time{}, nil
	}
	if !strings.HasPrefix(d.Source, ociScheme) {
		return rel.Created, nil
	}
	timer, ok := s.oci.(datasource.Timer)
	if !ok {
		return time.Time{}, nil
	}
	return timer.ReleaseTime(ctx, ociDep(d.Source, d.Name), ociTag(rel.Version))
}

// find returns the release of version, comparing without "v" prefixes.
func find(rels []Release, version string) (Release, bool) {
	v := strings.TrimPrefix(version, "v")
	for _, r := range rels {
		if strings.TrimPrefix(r.Version, "v") == v {
			return r, true
		}
	}
	return Release{}, false
}

func (s *Source) lookup(ctx context.Context, repo, chart string) *result {
	switch {
	case repo == "":
		return &result{err: errors.New("no chart repository")}
	case strings.HasPrefix(repo, ociScheme):
		return s.once(ctx, repo, chart, func() *result { return s.ociChart(ctx, repo, chart) })
	}
	s.mu.Lock()
	s.want(repo, chart)
	s.mu.Unlock()
	ix, err := s.indexes.Do(ctx, repo, func() (*index, error) {
		s.mu.Lock()
		wanted := maps.Clone(s.wanted[repo])
		s.mu.Unlock()
		charts, err := s.scan(ctx, repo, wanted)
		return &index{wanted: wanted, charts: charts}, err
	})
	if !ix.wanted[chart] {
		// Asked for after the scan started: scan again for this chart alone.
		return s.once(ctx, repo, chart, func() *result {
			charts, err := s.scan(ctx, repo, map[string]bool{chart: true})
			if err != nil {
				return &result{err: err}
			}
			return charts[chart]
		})
	}
	if err != nil {
		return &result{err: err}
	}
	return ix.charts[chart]
}

// once runs fetch once per (repo, chart).
func (s *Source) once(ctx context.Context, repo, name string, fetch func() *result) *result {
	r, _ := s.charts.Do(ctx, repo+"\x00"+name, func() (*result, error) {
		r := fetch()
		return r, r.err
	})
	return r
}

// scan fetches repo's index.yaml and returns the results of the wanted charts; a wanted chart
// missing from the index gets a not-found error.
func (s *Source) scan(ctx context.Context, repo string, wanted map[string]bool) (charts map[string]*result, err error) {
	start := time.Now()
	target := repo + "/index.yaml"
	var n countingReader
	defer func() {
		slog.DebugContext(ctx, "chart index scanned", "repo", repo, "wanted", len(wanted), "bytes", n.n, "duration", time.Since(start), "err", err)
	}()
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return nil, err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("chart repository %s: %w", repo, err)
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	if u, err := url.Parse(target); err == nil && s.auth != nil {
		if user, pass, ok := s.auth.Static(u.Hostname()); ok {
			req.SetBasicAuth(user, pass)
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chart repository %s: %w", repo, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("chart repository %s: GET index.yaml: HTTP %d", repo, resp.StatusCode)
	}
	n.r = resp.Body
	found, err := scanIndex(&n, wanted)
	if err != nil {
		return nil, fmt.Errorf("chart repository %s: index.yaml: %w", repo, err)
	}
	charts = make(map[string]*result, len(wanted))
	for name := range wanted {
		rels, ok := found[name]
		if !ok {
			charts[name] = &result{err: fmt.Errorf("chart %s not found in %s", name, repo)}
			continue
		}
		charts[name] = newResult(rels, nil)
	}
	return charts, nil
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ociChart lists the tags of the chart's OCI repository as versions ("_" → "+").
func (s *Source) ociChart(ctx context.Context, repo, chart string) *result {
	if s.oci == nil {
		return &result{err: errors.New("helm: no OCI datasource")}
	}
	tags, err := s.oci.Releases(ctx, ociDep(repo, chart), "")
	if err != nil {
		return &result{err: err}
	}
	rels := make([]Release, len(tags))
	for i, t := range tags {
		rels[i] = Release{Version: strings.ReplaceAll(t.Version, "_", "+")}
	}
	return newResult(rels, nil)
}

// ociDep is the docker-datasource dep of chart in OCI repository repo. Docker Hub's registry
// hosts are written as docker.io, so Docker Hub credentials and paging apply.
func ociDep(repo, chart string) dep.Dep {
	ref := strings.TrimPrefix(repo, ociScheme) + "/" + chart
	for _, h := range []string{"registry-1.docker.io/", "index.docker.io/"} {
		if rest, ok := strings.CutPrefix(ref, h); ok {
			ref = "docker.io/" + rest
		}
	}
	return dep.Dep{Datasource: "docker", Name: ref, Source: ref}
}

// ociTag is the OCI tag of chart version v: OCI tags cannot contain "+".
func ociTag(v string) string { return strings.ReplaceAll(v, "+", "_") }
