// Package osv looks up security advisories for dependencies in the OSV database
// (https://osv.dev): one `/v1/querybatch` round per 1000 distinct packages, then one
// `/v1/vulns/{id}` fetch per distinct advisory.
//
// Ecosystems:
//   - gomod deps with DepType "direct" or "indirect" are queried as ecosystem "Go" with the
//     module path and version. OSV matches the version server-side. Fixed versions are
//     returned with the "v" prefix Go modules use ("0.23.0" → "v0.23.0").
//   - github-actions deps hosted on github.com are queried as ecosystem "GitHub Actions" with
//     package "owner/repo" (any subpath is dropped; names are case-sensitive in OSV and sent as
//     written). OSV does not evaluate ECOSYSTEM ranges for this ecosystem (a versioned query
//     returns nothing, verified 2026-11-03), so the package is queried without a version and
//     the ranges are evaluated locally. OSV writes GHA versions without "v" ("4.1.3", or even
//     "41"). The dep version is compared numerically. A floating tag with fewer than three
//     parts ("v4", "v4.1") stands for the newest release of its line, so it is only affected
//     when every possible release of the line is. Fixed versions are rendered in the dep's
//     style: its "v" prefix and its part count (padded with 0 or truncated, e.g. "41" →
//     "v41.0.0" for "v35.0.0", "46.0.1" → "v46" for "v35").
//   - npm deps are queried as ecosystem "npm" with the package name and the bare version;
//     OSV matches server-side. Fixed versions are bare semver ("4.17.21").
//
// Withdrawn advisories are skipped. Advisories that alias each other ("GO-2024-2687" and
// "GHSA-4v7x-pqxf-cx7m") are reported once, preferring the ecosystem's native ID.
package osv

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

const (
	defaultBaseURL = "https://api.osv.dev"
	maxBatch       = 1000 // OSV querybatch limit
	maxFetchers    = 16

	ecoGo      = "Go"
	ecoActions = "GitHub Actions"
	ecoNpm     = "npm"
)

// Source queries OSV. Advisory documents are fetched once per ID per run.
type Source struct {
	client  *http.Client
	sem     chan struct{}
	baseURL string
	batch   int

	mu    sync.Mutex
	vulns map[string]*vulnEntry
}

type vulnEntry struct {
	once sync.Once
	v    *vuln
	err  error
}

// New returns the OSV security source. sem bounds concurrent requests and is shared with the
// datasources.
func New(client *http.Client, sem chan struct{}) datasource.SecuritySource {
	return newSource(client, sem, defaultBaseURL)
}

func newSource(client *http.Client, sem chan struct{}, baseURL string) *Source {
	return &Source{client: client, sem: sem, baseURL: baseURL, batch: maxBatch, vulns: make(map[string]*vulnEntry)}
}

type pkg struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type query struct {
	Package   pkg    `json:"package"`
	Version   string `json:"version,omitempty"`
	PageToken string `json:"page_token,omitempty"`
}

// target is what one dep is checked against: the query and, for GitHub Actions, the version
// evaluated locally.
type target struct {
	q   query
	tag versioning.Tag // GitHub Actions: the dep version
	cmp [3]int         // GitHub Actions: tag padded for range evaluation
}

// Advisories implements datasource.SecuritySource. Deps without a supported ecosystem get nil.
func (s *Source) Advisories(ctx context.Context, deps []dep.Dep) ([][]datasource.Vuln, error) {
	out := make([][]datasource.Vuln, len(deps))
	targets := make([]*target, len(deps))
	index := make(map[query]int)
	var queries []query
	depQuery := make([]int, len(deps))
	for i, d := range deps {
		depQuery[i] = -1
		t := targetOf(d)
		if t == nil {
			continue
		}
		targets[i] = t
		k, ok := index[t.q]
		if !ok {
			k = len(queries)
			index[t.q] = k
			queries = append(queries, t.q)
		}
		depQuery[i] = k
	}
	if len(queries) == 0 {
		return out, nil
	}

	ids, err := s.queryAll(ctx, queries)
	if err != nil {
		return nil, err
	}
	docs, err := s.fetchAll(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i, k := range depQuery {
		if k >= 0 {
			out[i] = match(targets[i], ids[k], docs)
		}
	}
	return out, nil
}

// targetOf returns the OSV query for d, or nil when d has no supported ecosystem.
func targetOf(d dep.Dep) *target {
	switch d.Manager {
	case "gomod":
		if (d.DepType != "direct" && d.DepType != "indirect") || d.Name == "" || d.Version == "" {
			return nil
		}
		return &target{q: query{Package: pkg{Name: d.Name, Ecosystem: ecoGo}, Version: d.Version}}
	case "npm":
		if d.Name == "" || d.Version == "" {
			return nil
		}
		return &target{q: query{Package: pkg{Name: d.Name, Ecosystem: ecoNpm}, Version: d.Version}}
	case "github-actions":
		u, err := url.Parse(d.Source)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
			return nil
		}
		owner, rest, ok := strings.Cut(d.Name, "/")
		repo, _, _ := strings.Cut(rest, "/")
		if !ok || owner == "" || repo == "" {
			return nil
		}
		tag, ok := versioning.ParseTag(d.Version)
		if !ok {
			return nil
		}
		t := &target{q: query{Package: pkg{Name: owner + "/" + repo, Ecosystem: ecoActions}}, tag: tag}
		for i := range t.cmp {
			// A floating tag stands for the newest release of its line.
			t.cmp[i] = math.MaxInt
			if i < len(tag.Parts) {
				t.cmp[i] = tag.Parts[i]
			}
		}
		return t
	}
	return nil
}

// queryAll runs querybatch over queries in chunks, following per-result page tokens, and
// returns the advisory IDs per query.
func (s *Source) queryAll(ctx context.Context, queries []query) ([][]string, error) {
	ids := make([][]string, len(queries))
	pending := make([]int, len(queries))
	for i := range pending {
		pending[i] = i
	}
	for len(pending) > 0 {
		var next []int
		for start := 0; start < len(pending); start += s.batch {
			chunk := pending[start:min(start+s.batch, len(pending))]
			req := make([]query, len(chunk))
			for j, k := range chunk {
				req[j] = queries[k]
			}
			results, err := s.queryBatch(ctx, req)
			if err != nil {
				return nil, err
			}
			for j, r := range results {
				k := chunk[j]
				for _, v := range r.Vulns {
					ids[k] = append(ids[k], v.ID)
				}
				if r.NextPageToken == "" {
					continue
				}
				if r.NextPageToken == queries[k].PageToken {
					return nil, fmt.Errorf("querybatch: %s: repeated page token", queries[k].Package.Name)
				}
				queries[k].PageToken = r.NextPageToken
				next = append(next, k)
			}
		}
		pending = next
	}
	return ids, nil
}

type batchResult struct {
	Vulns []struct {
		ID string `json:"id"`
	} `json:"vulns"`
	NextPageToken string `json:"next_page_token"`
}

func (s *Source) queryBatch(ctx context.Context, queries []query) ([]batchResult, error) {
	body, err := json.Marshal(struct {
		Queries []query `json:"queries"`
	}{queries})
	if err != nil {
		return nil, err
	}
	u := s.baseURL + "/v1/querybatch"
	var res struct {
		Results []batchResult `json:"results"`
	}
	err = s.do(httpx.Idempotent(ctx), http.MethodPost, u, body, &res) // a read-only query: retry like a GET
	if err == nil && len(res.Results) != len(queries) {
		err = fmt.Errorf("POST %s: got %d results for %d queries", u, len(res.Results), len(queries))
	}
	slog.DebugContext(ctx, "osv querybatch", "queries", len(queries), "results", len(res.Results), "err", err)
	if err != nil {
		return nil, err
	}
	return res.Results, nil
}

// fetchAll fetches every distinct advisory in ids concurrently.
func (s *Source) fetchAll(ctx context.Context, ids [][]string) (map[string]*vuln, error) {
	seen := make(map[string]bool)
	var uniq []string
	for _, l := range ids {
		for _, id := range l {
			if !seen[id] {
				seen[id] = true
				uniq = append(uniq, id)
			}
		}
	}
	slices.Sort(uniq)
	work := make(chan string)
	var (
		mu   sync.Mutex
		docs = make(map[string]*vuln, len(uniq))
		errs []error
		wg   sync.WaitGroup
	)
	for range min(len(uniq), maxFetchers) {
		wg.Go(func() {
			for id := range work {
				v, err := s.vuln(ctx, id)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				} else {
					docs[id] = v
				}
				mu.Unlock()
			}
		})
	}
	for _, id := range uniq {
		work <- id
	}
	close(work)
	wg.Wait()
	if len(errs) > 0 {
		slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
		return nil, errors.Join(errs...)
	}
	return docs, nil
}

type event struct {
	Introduced   string `json:"introduced"`
	Fixed        string `json:"fixed"`
	LastAffected string `json:"last_affected"`
}

type affected struct {
	Package pkg `json:"package"`
	Ranges  []struct {
		Type   string  `json:"type"`
		Events []event `json:"events"`
	} `json:"ranges"`
	Versions []string `json:"versions"`
}

type vuln struct {
	ID         string   `json:"id"`
	Summary    string   `json:"summary"`
	Details    string   `json:"details"`
	Aliases    []string `json:"aliases"`
	Withdrawn  string   `json:"withdrawn"`
	References []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
	Affected []affected `json:"affected"`
}

// vuln fetches one advisory, once per ID.
func (s *Source) vuln(ctx context.Context, id string) (*vuln, error) {
	s.mu.Lock()
	e := s.vulns[id]
	if e == nil {
		e = &vulnEntry{}
		s.vulns[id] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		var v vuln
		e.err = s.do(ctx, http.MethodGet, s.baseURL+"/v1/vulns/"+url.PathEscape(id), nil, &v)
		if e.err == nil {
			e.v = &v
		}
		slog.DebugContext(ctx, "osv vuln", "id", id, "withdrawn", v.Withdrawn != "", "err", e.err)
	})
	return e.v, e.err
}

// do sends one request (holding a sem slot) and decodes the JSON response into out.
func (s *Source) do(ctx context.Context, method, u string, body []byte, out any) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	var r io.Reader = http.NoBody
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: HTTP %d", method, u, resp.StatusCode)
	}
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		return fmt.Errorf("%s %s: %w", method, u, err)
	}
	return nil
}

func (s *Source) acquire(ctx context.Context) error {
	if s.sem == nil {
		return ctx.Err()
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

// match turns the advisories found for t into Vulns: withdrawn ones and (GitHub Actions)
// ones not affecting the dep version are dropped, aliases are collapsed.
func match(t *target, ids []string, docs map[string]*vuln) []datasource.Vuln {
	cands := make([]*vuln, 0, len(ids))
	for _, id := range ids {
		if v := docs[id]; v != nil && v.Withdrawn == "" && !slices.Contains(cands, v) {
			cands = append(cands, v)
		}
	}
	native := "GO-"
	if t.q.Package.Ecosystem != ecoGo {
		native = "GHSA-"
	}
	slices.SortFunc(cands, func(a, b *vuln) int {
		if an, bn := strings.HasPrefix(a.ID, native), strings.HasPrefix(b.ID, native); an != bn {
			if an {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	var out []datasource.Vuln
	seen := make(map[string]bool)
	for _, v := range cands {
		if seen[v.ID] {
			continue
		}
		entries := v.entries(t.q.Package)
		var fixed []string
		switch t.q.Package.Ecosystem {
		case ecoActions:
			if !t.affectedBy(entries) {
				continue
			}
			fixed = t.actionsFixed(entries)
		case ecoNpm:
			fixed = npmFixed(entries)
		default:
			fixed = goFixed(entries)
		}
		seen[v.ID] = true
		for _, a := range v.Aliases {
			seen[a] = true
		}
		out = append(out, datasource.Vuln{Advisory: v.advisory(), Fixed: fixed})
	}
	return out
}

// entries returns the affected entries for p. For Go, entries naming a package inside the
// module (GHSA lists "golang.org/x/net/http2") count when none names the module itself.
func (v *vuln) entries(p pkg) []affected {
	var exact, sub []affected
	for _, a := range v.Affected {
		switch {
		case a.Package.Ecosystem != p.Ecosystem:
		case a.Package.Name == p.Name:
			exact = append(exact, a)
		case p.Ecosystem == ecoGo && strings.HasPrefix(a.Package.Name, p.Name+"/"):
			sub = append(sub, a)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return sub
}

func (v *vuln) advisory() dep.Advisory {
	a := dep.Advisory{ID: v.ID, Summary: v.Summary, URL: "https://osv.dev/vulnerability/" + url.PathEscape(v.ID)}
	if a.Summary == "" {
		line, _, _ := strings.Cut(strings.TrimSpace(v.Details), "\n")
		if len(line) > 120 {
			line = line[:117] + "..."
		}
		a.Summary = line
	}
	for _, r := range v.References {
		if r.Type == "ADVISORY" || r.Type == "WEB" {
			a.URL = r.URL
			break
		}
	}
	return a
}

// fixedEvents calls f for every fixed event of the SEMVER/ECOSYSTEM ranges in entries.
func fixedEvents(entries []affected, f func(string)) {
	for _, a := range entries {
		for _, r := range a.Ranges {
			if r.Type != "SEMVER" && r.Type != "ECOSYSTEM" {
				continue
			}
			for _, e := range r.Events {
				if e.Fixed != "" {
					f(e.Fixed)
				}
			}
		}
	}
}

func goFixed(entries []affected) []string {
	var out []string
	fixedEvents(entries, func(f string) {
		if f = "v" + strings.TrimPrefix(f, "v"); !slices.Contains(out, f) {
			out = append(out, f)
		}
	})
	return out
}

func npmFixed(entries []affected) []string {
	var out []string
	fixedEvents(entries, func(f string) {
		if f = strings.TrimPrefix(f, "v"); !slices.Contains(out, f) {
			out = append(out, f)
		}
	})
	return out
}

// actionsFixed renders the fixed versions in the dep's prefix style and part count.
func (t *target) actionsFixed(entries []affected) []string {
	var out []string
	fixedEvents(entries, func(f string) {
		ft, ok := versioning.ParseTag(f)
		if !ok {
			return
		}
		var b strings.Builder
		b.WriteString(t.tag.Prefix)
		for i := range t.tag.Parts {
			if i > 0 {
				b.WriteByte('.')
			}
			n := 0
			if i < len(ft.Parts) {
				n = ft.Parts[i]
			}
			b.WriteString(strconv.Itoa(n))
		}
		if s := b.String(); !slices.Contains(out, s) {
			out = append(out, s)
		}
	})
	return out
}

// affectedBy evaluates the entries' explicit versions and SEMVER/ECOSYSTEM ranges against the
// dep version, following the OSV spec's evaluation algorithm. Ranges with versions that are
// not numeric tags are ignored.
func (t *target) affectedBy(entries []affected) bool {
	for _, a := range entries {
		if len(t.tag.Parts) == 3 {
			for _, v := range a.Versions {
				if p, ok := parts(v); ok && p == t.cmp {
					return true
				}
			}
		}
		for _, r := range a.Ranges {
			if (r.Type == "SEMVER" || r.Type == "ECOSYSTEM") && t.inRange(r.Events) {
				return true
			}
		}
	}
	return false
}

func (t *target) inRange(events []event) bool {
	type ev struct {
		v    [3]int
		kind byte // 'i' introduced, 'f' fixed, 'l' last_affected
	}
	evs := make([]ev, 0, len(events))
	for _, e := range events {
		s, kind := e.Introduced, byte('i')
		switch {
		case e.Fixed != "":
			s, kind = e.Fixed, 'f'
		case e.LastAffected != "":
			s, kind = e.LastAffected, 'l'
		case s == "":
			continue // limit or unknown event
		}
		p, ok := parts(s)
		if !ok {
			return false
		}
		evs = append(evs, ev{p, kind})
	}
	slices.SortStableFunc(evs, func(a, b ev) int { return cmp3(a.v, b.v) })
	in := false
	for _, e := range evs {
		c := cmp3(t.cmp, e.v)
		switch {
		case e.kind == 'i' && c >= 0:
			in = true
		case e.kind == 'f' && c >= 0, e.kind == 'l' && c > 0:
			in = false
		}
	}
	return in
}

// parts parses an OSV GitHub Actions version ("4.1.3", "v4", "0") padded to three parts.
func parts(s string) ([3]int, bool) {
	var p [3]int
	t, ok := versioning.ParseTag(s)
	if !ok {
		return p, false
	}
	copy(p[:], t.Parts)
	return p, true
}

func cmp3(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
