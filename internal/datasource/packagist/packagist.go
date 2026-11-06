// Package packagist lists the releases of Composer packages: from Packagist (repo.packagist.org,
// the Composer 2 metadata API) and from other Composer repositories (a composer.json
// `repositories` entry of type composer: Private Packagist, Satis, packages.drupal.org).
// Lookups run once per (repository, package) and are shared by every dep and composer.json of
// a run.
package packagist

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
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "packagist"

// DefaultURL is Packagist's metadata host; DEPS_PACKAGIST_URL overrides it (a mirror serving
// its `/p2/` API).
const DefaultURL = "https://repo.packagist.org"

// errNotFound marks a repository that does not have the package (HTTP 404, or not in its
// listing).
var errNotFound = errors.New("not found")

// Source is the packagist datasource.
type Source struct {
	client *http.Client
	sem    chan struct{}
	getenv func(string) string
	base   string // the Packagist metadata base, without a trailing slash

	authOnce sync.Once
	auth     composerAuth

	repos datasource.Memo[string, *repository] // by repository URL
	pkgs  datasource.Memo[string, *pkg]        // by metadata URL
}

// pkg is what a repository lists for a package: its tagged versions (branches such as
// `dev-main` and `1.x-dev` left out), their release times and the source repository of the
// newest one.
type pkg struct {
	rels   []datasource.Release
	times  map[string]time.Time
	source string
}

// New returns the packagist datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_PACKAGIST_URL and COMPOSER_AUTH.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{
		client: client, sem: sem, getenv: getenv,
		base: strings.TrimSuffix(cmp.Or(getenv("DEPS_PACKAGIST_URL"), DefaultURL), "/"),
	}
}

// Releases lists the tagged versions of d. d.Source holds the Composer repositories of the
// dep in lookup order, whitespace-separated ("" = Packagist); the first one that has the
// package answers, as Composer's canonical repositories do. The result is shared and must not
// be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the version's `time` (its commit or tag
// date as the repository records it); zero when the repository records none.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// Digest is unsupported: package versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// SourceRepo implements datasource.SourceRepo: the http(s) `source.url` of the newest version
// (`https://github.com/Seldaek/monolog.git`).
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return "", err
	}
	return p.source, nil
}

// repositories returns the Composer repositories of d in lookup order; "" is Packagist.
func repositories(d dep.Dep) []string {
	if f := strings.Fields(d.Source); len(f) > 0 {
		return f
	}
	return []string{""}
}

// lookup returns the listing of d's package in the first of its repositories that has it.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	name := strings.ToLower(d.Name)
	for _, repo := range repositories(d) {
		p, err := s.fetch(ctx, repo, name)
		if !errors.Is(err, errNotFound) {
			return p, err
		}
	}
	return nil, fmt.Errorf("package %s not found", d.Name)
}

// isDefault reports whether repo is Packagist ("" or its URL, as composer.json writes it).
func isDefault(repo string) bool {
	if repo == "" {
		return true
	}
	u, err := url.Parse(repo)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || strings.Trim(u.Path, "/") != "" {
		return false
	}
	switch strings.ToLower(u.Host) {
	case "packagist.org", "repo.packagist.org", "www.packagist.org":
		return true
	}
	return false
}

// fetch returns the listing of name in repo: Packagist's `GET <base>/p2/<name>.json`, another
// repository's metadata-url, or the packages its packages.json lists inline or includes.
func (s *Source) fetch(ctx context.Context, repo, name string) (*pkg, error) {
	if isDefault(repo) {
		return s.metadata(ctx, s.base+"/p2/"+name+".json", name)
	}
	r, err := s.repos.Do(ctx, repo, func() (*repository, error) { return s.index(ctx, repo) })
	if err != nil {
		return nil, err
	}
	if !r.offers(name) {
		return nil, errNotFound
	}
	if r.metadata != "" {
		return s.metadata(ctx, strings.ReplaceAll(r.metadata, "%package%", name), name)
	}
	if p := r.inline[name]; p != nil {
		return p, nil
	}
	return nil, errNotFound
}

// metadata reads a Composer 2 metadata file (`p2/<name>.json`) once per URL.
func (s *Source) metadata(ctx context.Context, target, name string) (*pkg, error) {
	return s.pkgs.Do(ctx, target, func() (p *pkg, err error) {
		start := time.Now()
		defer func() {
			n := 0
			if p != nil {
				n = len(p.rels)
			}
			slog.DebugContext(ctx, "packagist package fetched", "name", name, "url", httpx.Redact(target), "versions", n, "duration", time.Since(start), "err", err)
		}()
		err = s.get(ctx, target, func(r io.Reader) error {
			pkgs, err := readPackages(jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true)), name)
			p = pkgs[name]
			return err
		})
		if err == nil && p == nil {
			err = errNotFound
		}
		return p, err
	})
}

// entry is one version of a package listing; other fields are skipped. Time and Source stay
// nil when the entry has none (in a minified listing: the previous entry's apply).
type entry struct {
	Version string         `json:"version"`
	Time    *string        `json:"time"`
	Source  jsontext.Value `json:"source"`
}

// unset is the value a minified listing gives a field the previous entry has and this one
// does not (composer/metadata-minifier).
const unset = "__unset"

// readPackages reads a metadata or packages.json object from dec and returns the listings
// of its `packages` member: only name's when name is not "", else every package. A listing is
// a list of versions (Composer 2 metadata, expanded when `minified` is `composer/2.0`) or an
// object of versions by version (Composer 1 repositories, Satis includes).
func readPackages(dec *jsontext.Decoder, name string) (map[string]*pkg, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}
	if tok.Kind() != '{' {
		return nil, errors.New("not an object")
	}
	lists := map[string][]entry{}
	minified := false
	for dec.PeekKind() != '}' {
		key, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		switch key.String() {
		case "packages":
			err = readListings(dec, name, lists)
		case "minified":
			var m any
			err = json.UnmarshalDecode(dec, &m)
			minified = m == "composer/2.0"
		default:
			err = dec.SkipValue()
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	out := make(map[string]*pkg, len(lists))
	for n, es := range lists {
		out[n] = newPkg(es, minified)
	}
	return out, nil
}

// readListings reads the `packages` member into lists by lower-cased package name (only
// name's unless name is ""). An empty array (Packagist's packages.json) lists nothing.
func readListings(dec *jsontext.Decoder, name string, lists map[string][]entry) error {
	if dec.PeekKind() != '{' {
		return dec.SkipValue()
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	for dec.PeekKind() != '}' {
		key, err := dec.ReadToken()
		if err != nil {
			return err
		}
		n := strings.ToLower(key.String())
		if name != "" && n != name {
			if err := dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		es, err := readEntries(dec)
		if err != nil {
			return err
		}
		lists[n] = append(lists[n], es...)
	}
	_, err := dec.ReadToken()
	return err
}

// readEntries reads one listing: an array of entries or an object of entries by version.
func readEntries(dec *jsontext.Decoder) ([]entry, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}
	end := byte(']')
	switch tok.Kind() {
	case '[':
	case '{':
		end = '}'
	default:
		return nil, errors.New("package listing is not an array or object")
	}
	var es []entry
	for byte(dec.PeekKind()) != end {
		if end == '}' {
			if _, err := dec.ReadToken(); err != nil { // the version key
				return nil, err
			}
		}
		var e entry
		if err := json.UnmarshalDecode(dec, &e); err != nil {
			return nil, err
		}
		es = append(es, e)
	}
	_, err = dec.ReadToken()
	return es, err
}

// newPkg builds a listing from its entries: in a minified listing an entry without `time`
// has the previous entry's (`__unset` none). Branches are left out; the source repository is
// the first entry's.
func newPkg(es []entry, minified bool) *pkg {
	p := &pkg{times: map[string]time.Time{}}
	var last *string
	for i, e := range es {
		t := e.Time
		if t == nil && minified {
			t = last
		}
		last = t
		if i == 0 {
			p.source = sourceURL(e.Source)
		}
		v := e.Version
		if v == "" || branch(v) {
			continue
		}
		if _, seen := p.times[v]; seen {
			continue
		}
		p.rels = append(p.rels, datasource.Release{Version: v})
		p.times[v] = time.Time{}
		if t != nil && *t != unset {
			if ts, err := time.Parse(time.RFC3339, *t); err == nil {
				p.times[v] = ts
			}
		}
	}
	return p
}

// branch reports whether v is a branch version (`dev-main`, `2.x-dev`, `1.0.0-dev`).
func branch(v string) bool {
	l := strings.ToLower(v)
	return strings.HasPrefix(l, "dev-") || strings.HasSuffix(l, "-dev") || strings.HasSuffix(l, ".x-dev")
}

// sourceURL is the http(s) url of a `source` object, "" otherwise.
func sourceURL(raw jsontext.Value) string {
	if len(raw) == 0 || raw.Kind() != '{' {
		return ""
	}
	var src struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &src) != nil {
		return ""
	}
	if u, err := url.Parse(src.URL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		return src.URL
	}
	return ""
}

// repository is what a Composer repository's packages.json offers.
type repository struct {
	metadata  string          // metadata-url resolved against the repository, `%package%` kept; "" = none
	available map[string]bool // available-packages; nil = no list
	patterns  []string        // available-package-patterns (`drupal/*`)
	inline    map[string]*pkg // packages listed inline or in includes (repositories without metadata-url)
}

// offers reports whether the repository may have name: its available-packages and
// available-package-patterns, when it lists any, name it.
func (r *repository) offers(name string) bool {
	if r.available == nil && r.patterns == nil {
		return true
	}
	if r.available[name] {
		return true
	}
	for _, p := range r.patterns {
		if ok, _ := path.Match(strings.ToLower(p), name); ok {
			return true
		}
	}
	return false
}

// packagesJSON is a repository's packages.json; packages and includes are read separately.
type packagesJSON struct {
	MetadataURL       string                    `json:"metadata-url"`
	AvailablePackages []string                  `json:"available-packages"`
	AvailablePatterns []string                  `json:"available-package-patterns"`
	Includes          map[string]jsontext.Value `json:"includes"`
	ProvidersURL      string                    `json:"providers-url"`
	Packages          jsontext.Value            `json:"packages"`
}

// index reads `<repo>/packages.json` (the repository URL itself when it ends in `.json`):
// its metadata-url, or else the packages it lists inline and in its includes (Satis). A
// repository serving only Composer 1 providers is an error.
func (s *Source) index(ctx context.Context, repo string) (*repository, error) {
	u, err := url.Parse(repo)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		// The URL is not part of the error: it may hold credentials and does not parse to redact them.
		return nil, errors.New("packagist: invalid composer repository URL")
	}
	index := strings.TrimSuffix(repo, "/") + "/packages.json"
	if strings.HasSuffix(u.Path, ".json") {
		index = repo
	}
	base, _ := url.Parse(index)
	var pj packagesJSON
	if err := s.get(ctx, index, func(r io.Reader) error { return json.UnmarshalRead(r, &pj) }); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("packagist %s: no packages.json", httpx.Redact(index))
		}
		return nil, err
	}
	r := &repository{patterns: pj.AvailablePatterns}
	if pj.AvailablePackages != nil {
		r.available = make(map[string]bool, len(pj.AvailablePackages))
		for _, n := range pj.AvailablePackages {
			r.available[strings.ToLower(n)] = true
		}
	}
	if pj.MetadataURL != "" {
		ref, err := url.Parse(strings.ReplaceAll(pj.MetadataURL, "%package%", "__deps_package__"))
		if err != nil {
			return nil, fmt.Errorf("packagist %s: invalid metadata-url", httpx.Redact(index))
		}
		r.metadata = strings.ReplaceAll(base.ResolveReference(ref).String(), "__deps_package__", "%package%")
		return r, nil
	}
	r.inline = map[string]*pkg{}
	if len(pj.Packages) > 0 {
		doc := append(append([]byte(`{"packages":`), pj.Packages...), '}')
		if err := addInline(r.inline, bytes.NewReader(doc)); err != nil {
			return nil, fmt.Errorf("packagist %s: malformed packages: %w", httpx.Redact(index), err)
		}
	}
	for _, inc := range slices.Sorted(maps.Keys(pj.Includes)) {
		ref, err := url.Parse(inc)
		if err != nil {
			return nil, fmt.Errorf("packagist %s: invalid include %q", httpx.Redact(index), inc)
		}
		if err := s.get(ctx, base.ResolveReference(ref).String(), func(body io.Reader) error {
			return addInline(r.inline, body)
		}); err != nil {
			return nil, err
		}
	}
	if len(r.inline) == 0 && pj.ProvidersURL != "" {
		return nil, fmt.Errorf("packagist %s: only the Composer 1 providers API is served (no metadata-url)", httpx.Redact(index))
	}
	return r, nil
}

// addInline adds the listings of the packages.json-shaped document in body to inline.
func addInline(inline map[string]*pkg, body io.Reader) error {
	pkgs, err := readPackages(jsontext.NewDecoder(body, jsontext.AllowDuplicateNames(true)), "")
	if err != nil {
		return err
	}
	for n, p := range pkgs {
		if old := inline[n]; old != nil {
			for _, rel := range p.rels {
				if _, seen := old.times[rel.Version]; !seen {
					old.rels = append(old.rels, rel)
					old.times[rel.Version] = p.times[rel.Version]
				}
			}
			continue
		}
		inline[n] = p
	}
	return nil
}

// composerAuth is the part of COMPOSER_AUTH (Composer's auth.json as JSON) deps uses: basic
// credentials and bearer tokens by host.
type composerAuth struct {
	HTTPBasic map[string]struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"http-basic"`
	Bearer map[string]string `json:"bearer"`
}

// credentials sets the COMPOSER_AUTH credentials of req's host on req, unless its URL has
// userinfo (net/http sends that as basic auth). An unreadable COMPOSER_AUTH is logged once.
func (s *Source) credentials(ctx context.Context, req *http.Request) {
	s.authOnce.Do(func() {
		if raw := s.getenv("COMPOSER_AUTH"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &s.auth); err != nil {
				slog.WarnContext(ctx, "COMPOSER_AUTH is not valid JSON; ignored", "err", err)
				s.auth = composerAuth{}
			}
		}
	})
	if req.URL.User != nil {
		return
	}
	host := strings.ToLower(req.URL.Hostname())
	if c, ok := s.auth.HTTPBasic[host]; ok {
		req.SetBasicAuth(c.Username, c.Password)
	} else if t, ok := s.auth.Bearer[host]; ok {
		req.Header.Set("Authorization", "Bearer "+t)
	}
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
		return fmt.Errorf("packagist %s: invalid URL", httpx.Redact(target))
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	s.credentials(ctx, req)
	resp, err := s.client.Do(req) // a *url.Error carries the URL without its password
	if err != nil {
		return fmt.Errorf("packagist %s: %w", httpx.Redact(target), err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("packagist %s: HTTP %d", httpx.Redact(target), resp.StatusCode)
	}
	if err := read(resp.Body); err != nil {
		return fmt.Errorf("packagist %s: malformed response: %w", httpx.Redact(target), err)
	}
	return nil
}
