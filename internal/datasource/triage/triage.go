// Package triage annotates the advisories of another security source with the data that says
// how urgent a security update is:
//
//   - the CVSS v3 base score of deps.dev (https://api.deps.dev/v3/advisories/<GHSA id>), which
//     publishes the score itself, so no CVSS vector has to be parsed; its aliases also name
//     CVE IDs that the advisory itself may not list;
//   - the EPSS probability that the vulnerability is exploited within 30 days
//     (https://api.first.org/data/v1/epss), queried for all CVE aliases at once;
//   - whether CISA lists the vulnerability in its Known Exploited Vulnerabilities catalog
//     (one JSON document per run).
//
// The data is cosmetic: it is rendered with the advisory in PRs, `deps plan` and
// `deps explain` and never changes which update is proposed. Every lookup is therefore best
// effort: a failing source is logged at debug level and leaves its fields unset, and the
// advisories are returned as the wrapped source produced them.
package triage

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"encoding/json/v2"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Default endpoints.
const (
	DefaultDepsDevURL = "https://api.deps.dev"
	DefaultEPSSURL    = "https://api.first.org"
	DefaultKEVURL     = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"
)

// epssBatch is the number of CVE IDs per EPSS request; the API's own page limit is 100.
const epssBatch = 100

// Source wraps a security source and fills the risk fields of every advisory it returns.
type Source struct {
	src    datasource.SecuritySource
	getter httpx.Getter

	depsDevURL string
	epssURL    string
	kevURL     string

	// scores caches one deps.dev lookup per advisory ID, kev the catalog.
	scores datasource.Memo[string, advisory]
	kevOne datasource.Once[map[string]bool]
}

// New returns src with its advisories annotated. sem bounds the concurrent requests; it is
// OSV's pool (httpx.Limits.OSV), since the lookups run in the same phase as the OSV queries.
func New(src datasource.SecuritySource, client *http.Client, sem chan struct{}) datasource.SecuritySource {
	return newSource(src, client, sem, DefaultDepsDevURL, DefaultEPSSURL, DefaultKEVURL)
}

func newSource(src datasource.SecuritySource, client *http.Client, sem chan struct{}, depsDev, epss, kev string) *Source {
	accept := func(req *http.Request) error {
		req.Header.Set("Accept", "application/json")
		return nil
	}
	return &Source{
		src:        src,
		getter:     httpx.Getter{Client: client, Sem: sem, Prepare: accept},
		depsDevURL: depsDev,
		epssURL:    epss,
		kevURL:     kev,
	}
}

// Advisories implements datasource.SecuritySource.
func (s *Source) Advisories(ctx context.Context, deps []dep.Dep) ([][]datasource.Vuln, error) {
	vulns, err := s.src.Advisories(ctx, deps)
	if err != nil {
		return nil, err
	}
	// One Advisory value per distinct ID: the same advisory is reported for many deps, and
	// every copy gets the same risk data.
	index := make(map[string][]*dep.Advisory)
	for _, vs := range vulns {
		for i := range vs {
			a := &vs[i].Advisory
			index[a.ID] = append(index[a.ID], a)
		}
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	s.scoreAll(ctx, ids, index)
	s.exploitAll(ctx, ids, index)
	return vulns, nil
}

// scoreAll fills CVSS (and adds the aliases deps.dev knows) for every advisory, one request
// per advisory ID, bounded by the request pool.
func (s *Source) scoreAll(ctx context.Context, ids []string, index map[string][]*dep.Advisory) {
	var wg sync.WaitGroup
	for _, id := range ids {
		key := ghsaID(id, index[id][0].Aliases)
		if key == "" { // deps.dev keys its advisories by GHSA ID only
			continue
		}
		wg.Go(func() {
			adv, err := s.score(ctx, key)
			if err != nil {
				slog.DebugContext(ctx, "deps.dev advisory", "id", key, "err", err)
				return
			}
			for _, a := range index[id] {
				if a.CVSS == 0 { // the record's own vector, when it has one, already scored it
					a.CVSS = adv.CVSS3Score
				}
				for _, alias := range adv.Aliases {
					if !slices.Contains(a.Aliases, alias) && alias != a.ID {
						a.Aliases = append(a.Aliases, alias)
					}
				}
			}
		})
	}
	wg.Wait()
}

// exploitAll fills EPSS and KEV from the CVE aliases of the advisories.
func (s *Source) exploitAll(ctx context.Context, ids []string, index map[string][]*dep.Advisory) {
	cves := make(map[string][]string) // CVE ID -> advisory IDs
	for _, id := range ids {
		for _, c := range cveIDs(index[id][0]) {
			cves[c] = append(cves[c], id)
		}
	}
	if len(cves) == 0 {
		return
	}
	list := slices.Sorted(maps.Keys(cves))
	var (
		scores map[string]float64
		kev    map[string]bool
		wg     sync.WaitGroup
	)
	wg.Go(func() { scores = s.epss(ctx, list) })
	wg.Go(func() {
		set, err := s.kev(ctx)
		if err != nil {
			slog.DebugContext(ctx, "cisa kev catalog", "err", err)
			return
		}
		kev = set
	})
	wg.Wait()
	for c, advIDs := range cves {
		for _, id := range advIDs {
			for _, a := range index[id] {
				a.EPSS = max(a.EPSS, scores[c]) // the worst alias decides
				a.KEV = a.KEV || kev[c]
			}
		}
	}
}

// ghsaID returns the GHSA ID of an advisory (its own ID or an alias), "" when it has none.
func ghsaID(id string, aliases []string) string {
	if strings.HasPrefix(id, "GHSA-") {
		return id
	}
	for _, a := range aliases {
		if strings.HasPrefix(a, "GHSA-") {
			return a
		}
	}
	return ""
}

// cveIDs returns the CVE IDs of an advisory: its own ID when it is one, and its CVE aliases.
func cveIDs(a *dep.Advisory) []string {
	var out []string
	for _, id := range append([]string{a.ID}, a.Aliases...) {
		if strings.HasPrefix(id, "CVE-") && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// advisory is the part of a deps.dev advisory document deps reads.
type advisory struct {
	CVSS3Score float64  `json:"cvss3Score"`
	Aliases    []string `json:"aliases"`
}

// score fetches one deps.dev advisory, once per ID per run.
func (s *Source) score(ctx context.Context, id string) (advisory, error) {
	return s.scores.Do(ctx, id, func() (advisory, error) {
		var a advisory
		u := s.depsDevURL + "/v3/advisories/" + url.PathEscape(id)
		err := s.getter.Stream(ctx, u, func(r io.Reader) error { return json.UnmarshalRead(r, &a) })
		return a, err
	})
}

// epss returns the EPSS probability per CVE ID, leaving out the ones FIRST does not score
// (most CVEs younger than a day). A failed batch is logged and left out.
func (s *Source) epss(ctx context.Context, cves []string) map[string]float64 {
	out := make(map[string]float64, len(cves))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for chunk := range slices.Chunk(cves, epssBatch) {
		wg.Go(func() {
			u := s.epssURL + "/data/v1/epss?limit=" + strconv.Itoa(epssBatch) + "&cve=" + url.QueryEscape(strings.Join(chunk, ","))
			var res struct {
				Data []struct {
					CVE  string `json:"cve"`
					EPSS string `json:"epss"` // a decimal string ("0.018120000")
				} `json:"data"`
			}
			err := s.getter.Stream(ctx, u, func(r io.Reader) error { return json.UnmarshalRead(r, &res) })
			if err != nil {
				slog.DebugContext(ctx, "epss scores", "cves", len(chunk), "err", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range res.Data {
				if p, err := strconv.ParseFloat(d.EPSS, 64); err == nil {
					out[d.CVE] = p
				}
			}
		})
	}
	wg.Wait()
	return out
}

// kev fetches CISA's Known Exploited Vulnerabilities catalog once per run and returns its CVE
// IDs.
func (s *Source) kev(ctx context.Context) (map[string]bool, error) {
	return s.kevOne.Do(ctx, func() (map[string]bool, error) {
		var cat struct {
			Vulnerabilities []struct {
				CVEID string `json:"cveID"`
			} `json:"vulnerabilities"`
		}
		get := s.getter
		get.MaxBody = -1 // the catalog is a few MB and grows
		err := get.Stream(ctx, s.kevURL, func(r io.Reader) error { return json.UnmarshalRead(r, &cat) })
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(cat.Vulnerabilities))
		for _, v := range cat.Vulnerabilities {
			set[v.CVEID] = true
		}
		slog.DebugContext(ctx, "cisa kev catalog", "entries", len(set))
		return set, nil
	})
}
