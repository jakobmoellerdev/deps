// Package endoflife lists the release cycles of products tracked by endoflife.date: one
// request per product and run, shared by every dep naming it.
package endoflife

import (
	"cmp"
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
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "endoflife-date"

// DefaultURL is the endoflife.date API; DEPS_ENDOFLIFE_DATE_URL overrides it (a mirror
// serving the same `<product>.json` files), and a dep's Source (Renovate `registryUrl=`)
// overrides both.
const DefaultURL = "https://endoflife.date/api"

// Source is the endoflife-date datasource.
type Source struct {
	getter httpx.Getter
	base   string           // API base URL without a trailing slash
	now    func() time.Time // decides whether an eol or discontinued date has passed

	products datasource.Memo[string, *product] // by product URL
}

// product is the cycle list of a product.
type product struct {
	rels   []datasource.Release // supported cycles, newest first as listed
	times  map[string]time.Time // by release version
	cycles []cycle              // every cycle, end-of-life ones included
}

// cycle is a release cycle (`24` of nodejs) and the version it releases.
type cycle struct {
	name    string // the cycle, e.g. 24 or 22.04
	version string // its latest release, else the cycle
	ended   string // why the cycle should no longer be used, "" while supported
}

// New returns the endoflife-date datasource. sem bounds concurrent requests (nil =
// unbounded); getenv supplies DEPS_ENDOFLIFE_DATE_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, now: time.Now,
		base: strings.TrimSuffix(cmp.Or(getenv("DEPS_ENDOFLIFE_DATE_URL"), DefaultURL), "/")}
}

// Releases lists the latest release of every cycle of the product d.Name (`nodejs`,
// `ubuntu`) that has not reached its end of life or been discontinued, as Renovate drops
// deprecated releases; a cycle without `latest` is listed as the cycle itself. An unknown
// product is a dep.SkipError. The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer: the cycle's `latestReleaseDate` for its latest
// release, else its `releaseDate`; zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// Deprecation implements datasource.DeprecationSource: version belongs to a cycle that
// reached its end of life or was discontinued. The cycle of version is the one whose latest
// release it is, else the longest cycle it equals or starts with followed by a dot (`22.04.1`
// is in `22.04`, `3.12.4` in `3.12`).
func (s *Source) Deprecation(d dep.Dep, version string) string {
	p, err := s.lookup(context.Background(), d) // built by the earlier Releases call
	if err != nil {
		return ""
	}
	version = strings.TrimPrefix(version, "v")
	var best *cycle
	for i := range p.cycles {
		c := &p.cycles[i]
		if c.version == version {
			best = c
			break
		}
		if (version == c.name || strings.HasPrefix(version, c.name+".")) && (best == nil || len(c.name) > len(best.name)) {
			best = c
		}
	}
	if best == nil || best.ended == "" {
		return ""
	}
	return d.Name + " " + best.name + " " + best.ended
}

// Digest is unsupported: endoflife.date lists versions only.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// entry is a cycle of `GET <base>/<product>.json`. cycle and latest are strings, though
// older data had numbers; eol and discontinued are a date or a boolean.
type entry struct {
	Cycle             jsontext.Value `json:"cycle"`
	Latest            jsontext.Value `json:"latest"`
	ReleaseDate       string         `json:"releaseDate"`
	LatestReleaseDate string         `json:"latestReleaseDate"`
	EOL               jsontext.Value `json:"eol"`
	Discontinued      jsontext.Value `json:"discontinued"`
}

// lookup fetches the cycles of d's product once per product URL.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*product, error) {
	if d.Name == "" || strings.ContainsAny(d.Name, "/?#") {
		return nil, fmt.Errorf("invalid endoflife.date product %q", d.Name)
	}
	base := s.base
	if f := strings.Fields(d.Source); len(f) > 0 {
		base = f[0]
	}
	u := strings.TrimSuffix(base, "/") + "/" + url.PathEscape(d.Name) + ".json"
	return s.products.Do(ctx, u, func() (*product, error) {
		start := time.Now()
		p, err := s.fetch(ctx, u)
		slog.DebugContext(ctx, "endoflife.date cycles", "url", u, "releases", len(p.rels), "duration", time.Since(start), "err", err)
		if err != nil && errors.Is(err, httpx.ErrNotFound) {
			return nil, &dep.SkipError{Reason: fmt.Sprintf("unknown endoflife.date product %q", d.Name)}
		}
		if err != nil {
			return nil, err
		}
		return p, nil
	})
}

func (s *Source) fetch(ctx context.Context, u string) (*product, error) {
	p := &product{times: map[string]time.Time{}}
	now := s.now()
	err := s.getter.Stream(ctx, u, func(r io.Reader) error {
		var es []entry
		if err := json.UnmarshalRead(r, &es); err != nil {
			return err
		}
		for _, e := range es {
			name := scalar(e.Cycle)
			if name == "" {
				continue
			}
			c := cycle{name: name, version: cmp.Or(scalar(e.Latest), name)}
			if t, ok := passed(e.EOL, now); ok {
				c.ended = "reached its end of life" + t
			} else if t, ok := passed(e.Discontinued, now); ok {
				c.ended = "was discontinued" + t
			}
			p.cycles = append(p.cycles, c)
			if t, err := time.Parse(time.DateOnly, cmp.Or(e.LatestReleaseDate, e.ReleaseDate)); err == nil && c.version != name {
				p.times[c.version] = t
			} else if t, err := time.Parse(time.DateOnly, e.ReleaseDate); err == nil {
				p.times[c.version] = t
			}
			if c.ended == "" {
				p.rels = append(p.rels, datasource.Release{Version: c.version})
			}
		}
		if len(p.cycles) == 0 {
			return errors.New("no cycles")
		}
		return nil
	})
	return p, err
}

// scalar returns a JSON string or number as text, "" for anything else.
func scalar(v jsontext.Value) string {
	switch v.Kind() {
	case '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return ""
		}
		return s
	case '0':
		return string(v)
	}
	return ""
}

// passed reports whether an eol or discontinued field marks the cycle as ended at now: true,
// or a date at or before now (then the suffix " on <date>").
func passed(v jsontext.Value, now time.Time) (string, bool) {
	switch v.Kind() {
	case 't':
		return "", true
	case '"':
		var s string
		if json.Unmarshal(v, &s) != nil {
			return "", false
		}
		t, err := time.Parse(time.DateOnly, s)
		if err != nil || t.After(now) {
			return "", false
		}
		return " on " + s, true
	}
	return "", false
}
