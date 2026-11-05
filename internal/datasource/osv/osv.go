// Package osv looks up security advisories for dependencies in the OSV database
// (https://osv.dev): `/v1/querybatch` requests of 100 distinct packages each, sent
// concurrently, then one `/v1/vulns/{id}` fetch per distinct advisory, started as soon as the
// batch naming it returns.
//
// Ecosystems:
//   - gomod deps with DepType "direct" or "indirect" are queried as ecosystem "Go" with the
//     module path and version. OSV matches the version server-side. Fixed versions are
//     returned with the "v" prefix Go modules use ("0.23.0" → "v0.23.0").
//   - gomod `go` and `toolchain` directives are queried as ecosystem "Go" with the packages
//     "stdlib" (the standard library) and "toolchain" (the go command) and the directive's
//     version, without the "go" prefix OSV does not parse; OSV matches server-side. A
//     directive with fewer than three parts ("1.22") is queried as its lowest version
//     ("1.22.0") and its fixed versions are cut to its precision ("1.22.2" → "1.22"), so only
//     fixes outside its line count. Fixed versions are bare ("1.22.2"); prereleases are dropped.
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
//   - npm deps are queried as ecosystem "npm" with the package name and the locked version
//     (dep.Dep.Locked), else the bare version; OSV matches server-side. Fixed versions are
//     bare semver ("4.17.21").
//   - cargo deps of crates.io (datasource crate, no Source) are queried as ecosystem
//     "crates.io" with the crate name and the version Cargo.lock locks (dep.Dep.Locked), else
//     the requirement's version; OSV matches server-side. Without a lock a partial requirement
//     ("1", "0.4") is queried as its lowest version ("1.0.0", "0.4.0") and its fixed versions
//     are cut to its precision ("0.5.2" → "0.5"), so only fixes outside its range count.
//   - pypi deps (the pip_requirements, pep621, poetry and pipenv managers, and custom deps of
//     the datasource) are queried as ecosystem "PyPI" with the PEP 503-normalized name and the
//     locked version (dep.Dep.Locked), else the range's anchor version; OSV matches
//     server-side. Affected entries are matched by normalized name.
//     Fixed versions are bare, as OSV lists them ("2.32.0").
//   - maven-datasource deps (maven and gradle managers, custom entries) named
//     `group:artifact` are queried as ecosystem "Maven" with that name and the locked version
//     (dep.Dep.Locked), else the version; OSV matches server-side. Gradle plugin ids (no ":")
//     are not queried. Fixed versions are returned as published.
//   - rubygems-datasource deps (the bundler manager's gems of Gemfiles, gems.rb and gemspecs,
//     custom entries) are queried as ecosystem "RubyGems" with the gem name (case-sensitive)
//     and the locked version (dep.Dep.Locked), else the requirement's floor; OSV matches
//     server-side. Fixed versions are returned as published ("7.1.3.4").
//   - packagist-datasource deps (the composer manager's packages, custom entries) are queried as
//     ecosystem "Packagist" with the lower-cased package name and the locked version
//     (dep.Dep.Locked), else the constraint's anchor, without a "v" prefix; OSV matches
//     server-side, affected entries by case-insensitive name. Fixed versions are returned as
//     published ("6.4.2").
//
// Withdrawn advisories are skipped. Advisories that alias each other ("GO-2024-2687" and
// "GHSA-4v7x-pqxf-cx7m") are reported once, preferring the ecosystem's native ID.
package osv

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

const (
	defaultBaseURL = "https://api.osv.dev"
	batchSize      = 100 // queries per querybatch request (OSV allows 1000); small batches return sooner
	maxFetchers    = 16  // advisory fetchers without a semaphore; with one, its capacity

	ecoGo      = "Go"
	ecoActions = "GitHub Actions"
	ecoNpm     = "npm"
	ecoCrates  = "crates.io"
	ecoPyPI    = "PyPI"
	ecoMaven   = "Maven"
	ecoRuby    = "RubyGems"
	ecoPHP     = "Packagist"
)

// Source queries OSV. Advisory documents are fetched once per ID per run.
type Source struct {
	client  *http.Client
	sem     chan struct{}
	baseURL string
	batch   int

	vulns datasource.Memo[string, *vuln]
}

// New returns the OSV security source. sem bounds concurrent requests; it is OSV's own pool
// (httpx.Limits.OSV), so advisory fetches don't queue behind the datasources' lookups.
func New(client *http.Client, sem chan struct{}) datasource.SecuritySource {
	return newSource(client, sem, defaultBaseURL)
}

func newSource(client *http.Client, sem chan struct{}, baseURL string) *Source {
	return &Source{client: client, sem: sem, baseURL: baseURL, batch: batchSize}
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

// target is what one dep is checked against: its queries (one per OSV package) and, for GitHub
// Actions, partial crates.io requirements and Go toolchains, the version evaluated locally.
type target struct {
	qs  []query        // backed by buf
	buf [2]query       // Go toolchains query two packages, the rest one
	tag versioning.Tag // GitHub Actions: the dep version; crates.io, Go toolchains: the precision of fixes
	cmp [3]int         // GitHub Actions: tag padded for range evaluation
}

func newTarget(qs ...query) *target {
	t := &target{}
	t.qs = t.buf[:copy(t.buf[:], qs)]
	return t
}

// Advisories implements datasource.SecuritySource. Deps without a supported ecosystem get nil.
func (s *Source) Advisories(ctx context.Context, deps []dep.Dep) ([][]datasource.Vuln, error) {
	out := make([][]datasource.Vuln, len(deps))
	targets := make([]*target, len(deps))
	index := make(map[query]int)
	var queries []query
	// The query indexes of dep i are keys[span[i][0]:span[i][1]].
	keys := make([]int, 0, len(deps))
	span := make([][2]int, len(deps))
	for i, d := range deps {
		t := targetOf(d)
		if t == nil {
			continue
		}
		targets[i] = t
		span[i][0] = len(keys)
		for _, q := range t.qs {
			k, ok := index[q]
			if !ok {
				k = len(queries)
				index[q] = k
				queries = append(queries, q)
			}
			keys = append(keys, k)
		}
		span[i][1] = len(keys)
	}
	if len(queries) == 0 {
		return out, nil
	}

	ids, docs, err := s.lookup(ctx, queries)
	if err != nil {
		return nil, err
	}
	for i, t := range targets {
		if t == nil {
			continue
		}
		ks := keys[span[i][0]:span[i][1]]
		found := ids[ks[0]]
		for _, k := range ks[1:] {
			found = slices.Concat(found, ids[k])
		}
		out[i] = match(t, found, docs)
	}
	return out, nil
}

// goToolchain returns the target of a go.mod `go` or `toolchain` directive at version v: the
// Go standard library and the go command, both released with the toolchain.
func goToolchain(v string) *target {
	tag, ok := versioning.ParseTag(v)
	if !ok || tag.Prefix != "" {
		return nil
	}
	v += strings.Repeat(".0", 3-len(tag.Parts)) // "1.22" means its line from 1.22.0
	t := newTarget(
		query{Package: pkg{Name: "stdlib", Ecosystem: ecoGo}, Version: v},
		query{Package: pkg{Name: "toolchain", Ecosystem: ecoGo}, Version: v},
	)
	t.tag = tag
	return t
}

// targetOf returns the OSV query for d, or nil when d has no supported ecosystem.
func targetOf(d dep.Dep) *target {
	if d.Datasource == "pypi" { // every Python manager, and custom deps of the datasource
		v := cmp.Or(d.Locked, d.Version)
		if d.Name == "" || v == "" {
			return nil
		}
		return newTarget(query{Package: pkg{Name: pyreq.Normalize(d.Name), Ecosystem: ecoPyPI}, Version: v})
	}
	if d.Datasource == "maven" {
		v := cmp.Or(d.Locked, d.Version) // gradle.lockfile pins the resolved version
		if !strings.Contains(d.Name, ":") || v == "" {
			return nil
		}
		return newTarget(query{Package: pkg{Name: d.Name, Ecosystem: ecoMaven}, Version: v})
	}
	if d.Datasource == "rubygems" { // gems of every bundler file, and custom deps of the datasource
		v := cmp.Or(d.Locked, d.Version)
		if d.Name == "" || v == "" {
			return nil
		}
		return newTarget(query{Package: pkg{Name: d.Name, Ecosystem: ecoRuby}, Version: v})
	}
	if d.Datasource == "packagist" { // composer packages, and custom deps of the datasource
		v := strings.TrimLeft(cmp.Or(d.Locked, d.Version), "vV")
		if d.Name == "" || v == "" {
			return nil
		}
		return newTarget(query{Package: pkg{Name: strings.ToLower(d.Name), Ecosystem: ecoPHP}, Version: v})
	}
	switch d.Manager {
	case "gomod":
		switch d.DepType {
		case "direct", "indirect":
			if d.Name == "" || d.Version == "" {
				return nil
			}
			return newTarget(query{Package: pkg{Name: d.Name, Ecosystem: ecoGo}, Version: d.Version})
		case "go", "toolchain":
			return goToolchain(d.Version)
		}
		return nil
	case "npm":
		// The version installs get is the locked one; the range floor only without a lockfile.
		v := cmp.Or(d.Locked, d.Version)
		if d.Name == "" || v == "" {
			return nil
		}
		return newTarget(query{Package: pkg{Name: d.Name, Ecosystem: ecoNpm}, Version: v})
	case "cargo":
		if d.Datasource != "crate" || d.Source != "" || d.Name == "" || d.Version == "" {
			return nil
		}
		if d.Locked != "" {
			return newTarget(query{Package: pkg{Name: d.Name, Ecosystem: ecoCrates}, Version: d.Locked})
		}
		t := newTarget(query{Package: pkg{Name: d.Name, Ecosystem: ecoCrates}, Version: d.Version})
		if tag, ok := versioning.ParseTag(d.Version); ok && len(tag.Parts) < 3 {
			t.tag = tag
			t.qs[0].Version = d.Version + strings.Repeat(".0", 3-len(tag.Parts))
		}
		return t
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
		t := newTarget(query{Package: pkg{Name: owner + "/" + repo, Ecosystem: ecoActions}})
		t.tag = tag
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

// lookup runs querybatch over queries and fetches every distinct advisory found. A fetch starts
// as soon as the batch naming its advisory returns, while other batches and later pages are
// still running. Fetchers are bounded by the capacity of sem (maxFetchers without one).
func (s *Source) lookup(ctx context.Context, queries []query) ([][]string, map[string]*vuln, error) {
	work := make(chan string)
	var (
		mu   sync.Mutex
		docs = make(map[string]*vuln)
		errs []error
		wg   sync.WaitGroup
	)
	n := maxFetchers
	if s.sem != nil {
		n = cap(s.sem)
	}
	for range n {
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
	ids, err := s.queryAll(ctx, queries, work)
	close(work)
	wg.Wait()
	if err != nil {
		return nil, nil, err
	}
	if len(errs) > 0 {
		slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
		return nil, nil, errors.Join(errs...)
	}
	return ids, docs, nil
}

// queryAll runs querybatch over queries in chunks of s.batch, the chunks of one round
// concurrently, following per-result page tokens round by round, and returns the advisory IDs
// per query. Each distinct ID is sent to found once, as soon as its batch returns.
func (s *Source) queryAll(ctx context.Context, queries []query, found chan<- string) ([][]string, error) {
	ids := make([][]string, len(queries))
	pending := make([]int, len(queries))
	for i := range pending {
		pending[i] = i
	}
	var (
		mu   sync.Mutex
		seen = make(map[string]bool)
	)
	for len(pending) > 0 {
		chunks := slices.Collect(slices.Chunk(pending, s.batch))
		next := make([][]int, len(chunks))
		errs := make([]error, len(chunks))
		var wg sync.WaitGroup
		for c, chunk := range chunks {
			wg.Go(func() {
				next[c], errs[c] = s.queryChunk(ctx, queries, chunk, ids, func(id string) bool {
					mu.Lock()
					defer mu.Unlock()
					fresh := !seen[id]
					seen[id] = true
					return fresh
				}, found)
			})
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}
		pending = slices.Concat(next...)
	}
	return ids, nil
}

// queryChunk sends one querybatch for the queries at the indexes in chunk, appends the IDs
// found to ids, sends the IDs fresh reports as new to found, and returns the indexes with
// another page. A query is in one chunk per round, so ids[k] and queries[k] are written by one
// goroutine only.
func (s *Source) queryChunk(ctx context.Context, queries []query, chunk []int, ids [][]string, fresh func(string) bool, found chan<- string) ([]int, error) {
	req := make([]query, len(chunk))
	for j, k := range chunk {
		req[j] = queries[k]
	}
	results, err := s.queryBatch(ctx, req)
	if err != nil {
		return nil, err
	}
	var next []int
	for j, r := range results {
		k := chunk[j]
		for _, v := range r.Vulns {
			ids[k] = append(ids[k], v.ID)
			if fresh(v.ID) {
				found <- v.ID
			}
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
	return next, nil
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
	// Versions is the raw "versions" array, decoded only for GitHub Actions deps with a full
	// tag (affectedBy): other ecosystems are matched server-side and never read it, and
	// PYSEC-style records list every release ever published.
	Versions jsontext.Value `json:"versions"`
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
	return s.vulns.Do(id, func() (*vuln, error) {
		var v vuln
		err := s.do(ctx, http.MethodGet, s.baseURL+"/v1/vulns/"+url.PathEscape(id), nil, &v)
		slog.DebugContext(ctx, "osv vuln", "id", id, "withdrawn", v.Withdrawn != "", "err", err)
		if err != nil {
			return nil, err
		}
		return &v, nil
	})
}

// do sends one request (holding a sem slot) and decodes the JSON response into out.
func (s *Source) do(ctx context.Context, method, u string, body []byte, out any) error {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return err
	}
	defer release()
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

// match turns the advisories found for t into Vulns: withdrawn ones and (GitHub Actions)
// ones not affecting the dep version are dropped, aliases are collapsed.
func match(t *target, ids []string, docs map[string]*vuln) []datasource.Vuln {
	cands := make([]*vuln, 0, len(ids))
	for _, id := range ids {
		if v := docs[id]; v != nil && v.Withdrawn == "" && !slices.Contains(cands, v) {
			cands = append(cands, v)
		}
	}
	eco := t.qs[0].Package.Ecosystem
	native := "GHSA-"
	switch eco {
	case ecoGo:
		native = "GO-"
	case ecoCrates:
		native = "RUSTSEC-"
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
		entries := v.entries(t.qs[0].Package)
		for _, q := range t.qs[1:] {
			entries = append(entries, v.entries(q.Package)...)
		}
		var fixed []string
		switch eco {
		case ecoActions:
			if !t.affectedBy(entries) {
				continue
			}
			fixed = t.actionsFixed(entries)
		case ecoNpm, ecoPyPI:
			fixed = npmFixed(entries)
		case ecoCrates:
			fixed = npmFixed(entries)
			if len(t.tag.Parts) > 0 {
				fixed = t.actionsFixed(entries) // partial requirement: at its precision
			}
		case ecoMaven, ecoRuby, ecoPHP:
			fixed = mavenFixed(entries)
		case ecoGo:
			if len(t.tag.Parts) > 0 {
				fixed = t.actionsFixed(entries) // toolchain: bare, at the directive's precision
			} else {
				fixed = goFixed(entries)
			}
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
		case a.Package.Name == p.Name, p.Ecosystem == ecoPyPI && pyreq.Normalize(a.Package.Name) == p.Name,
			p.Ecosystem == ecoPHP && strings.EqualFold(a.Package.Name, p.Name):
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

func mavenFixed(entries []affected) []string {
	var out []string
	fixedEvents(entries, func(f string) {
		if !slices.Contains(out, f) {
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
			var versions []string
			if len(a.Versions) > 0 {
				// The value was already validated as JSON by the advisory decode; a non-string
				// array decodes partially or not at all, which leaves nothing to match.
				_ = json.Unmarshal(a.Versions, &versions)
			}
			for _, v := range versions {
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
