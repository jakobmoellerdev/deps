// Package javaversion lists Java (Eclipse Temurin) releases from the Adoptium API, as
// Renovate's java-version datasource does.
package javaversion

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
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "java-version"

// Dep names (Renovate's): the JDK (`java` is an alias) and the JRE.
const (
	JDK = "java-jdk"
	JRE = "java-jre"
)

// DefaultURL is the Adoptium release version list (Renovate's registry);
// DEPS_JAVA_RELEASES_URL overrides it.
const DefaultURL = "https://api.adoptium.net/v3/info/release_versions"

// pageSize is the Adoptium API's largest page; maxPages bounds the pages read per name, as in
// Renovate (50 pages of 50).
const (
	pageSize = 50
	maxPages = 50
)

// Source fetches the list of each image type (and os/architecture filter) once per run.
type Source struct {
	getter httpx.Getter
	url    string

	lists datasource.Memo[string, []datasource.Release] // by query
}

// New returns the java-version datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_JAVA_RELEASES_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, url: cmp.Or(getenv("DEPS_JAVA_RELEASES_URL"), DefaultURL)}
}

// Query returns the Adoptium filter of a dep name: `java`/`java-jdk` (JDK) or `java-jre`
// (JRE), optionally followed by `?os=<os>&architecture=<arch>` (Renovate's packageName form).
func Query(name string) (url.Values, error) {
	base, rawQuery, _ := strings.Cut(name, "?")
	q := url.Values{}
	switch base {
	case "java", JDK:
		q.Set("image_type", "jdk")
	case JRE:
		q.Set("image_type", "jre")
	default:
		return nil, fmt.Errorf("java-version: unknown name %q (want java, %s or %s)", name, JDK, JRE)
	}
	params, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, fmt.Errorf("java-version: name %q: %w", name, err)
	}
	for k, v := range params {
		if k != "os" && k != "architecture" || len(v) != 1 {
			return nil, fmt.Errorf("java-version: name %q: unsupported parameter %q (want os, architecture)", name, k)
		}
		q.Set(k, v[0])
	}
	q.Set("project", "jdk")
	q.Set("release_type", "ga")
	q.Set("sort_method", "DATE")
	q.Set("sort_order", "DESC")
	q.Set("page_size", strconv.Itoa(pageSize))
	return q, nil
}

// Releases returns the GA versions of d's image type as the API's semver (`21.0.5+11`,
// `8.0.512+9`), newest first, then a derived "X.Y" and "X" per release line, so partial values
// (`java-version: 21`) have candidates of their precision under tag versioning. The derived
// entries come last, so a scheme that compares `21.0` equal to 21.0.0 prefers the published
// version on a tie.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	q, err := Query(d.Name)
	if err != nil {
		return nil, err
	}
	key := q.Encode()
	return s.lists.Do(ctx, key, func() ([]datasource.Release, error) { return s.list(ctx, key) })
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// list reads the pages of query until a short page, a 404 after the first page or maxPages.
func (s *Source) list(ctx context.Context, query string) ([]datasource.Release, error) {
	start := time.Now()
	// About three pages of versions and a few dozen release lines (2026: 132 and 60).
	out, derived := make([]datasource.Release, 0, 3*pageSize), make([]datasource.Release, 0, pageSize)
	seen := map[string]bool{}
	add := func(list *[]datasource.Release, v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			*list = append(*list, datasource.Release{Version: v})
		}
	}
	var err error
	pages := 0
	for page := range maxPages {
		var n int
		n, err = s.page(ctx, fmt.Sprintf("%s?%s&page=%d", s.url, query, page), func(v string) {
			add(&out, v)
			core, _, _ := strings.Cut(v, "+")
			if strings.Contains(core, "-") || strings.Count(core, ".") != 2 {
				return
			}
			minor := core[:strings.LastIndexByte(core, '.')]
			add(&derived, minor)
			add(&derived, core[:strings.IndexByte(core, '.')])
		})
		if errors.Is(err, httpx.ErrNotFound) && page > 0 { // a 404 after the first page: the list has ended
			err = nil
			break
		}
		pages++
		if err != nil || n < pageSize {
			break
		}
	}
	slog.DebugContext(ctx, "java releases", "url", s.url, "query", query, "pages", pages, "releases", len(out), "duration", time.Since(start), "err", err)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("java-version: no releases for %s", query)
	}
	return append(out, derived...), nil
}

// page GETs one page and passes each version's semver to each; it returns the number of
// versions on the page. A 404 matches httpx.ErrNotFound.
func (s *Source) page(ctx context.Context, target string, each func(string)) (int, error) {
	var doc struct {
		Versions []struct {
			Semver string `json:"semver"`
		} `json:"versions"`
	}
	if err := s.getter.Stream(ctx, target, func(r io.Reader) error { return json.UnmarshalRead(r, &doc) }); err != nil {
		return 0, err
	}
	for _, v := range doc.Versions {
		each(v.Semver)
	}
	return len(doc.Versions), nil
}
