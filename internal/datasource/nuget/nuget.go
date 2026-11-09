// Package nuget lists the releases of NuGet packages from NuGet v3 feeds (nuget.org by
// default, or the package sources of a nuget.config): the feed's service index names the
// registration and flat container resources, the registration index lists every version with
// its listing state and publish time. Lookups run once per (feed, package) and are shared by
// every dep of a run.
package nuget

import (
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json/v2"
	"encoding/xml"
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
const Name = "nuget"

// DefaultURL is the service index of nuget.org; DEPS_NUGET_URL overrides it (a mirror serving
// the v3 API).
const DefaultURL = "https://api.nuget.org/v3/index.json"

// CredentialsEnv prefixes NuGet's own credential variable of a package source:
// `NuGetPackageSourceCredentials_<key>` holds `Username=<user>;Password=<password>`.
const CredentialsEnv = "NuGetPackageSourceCredentials_"

// errNotFound marks a feed that does not have the package (HTTP 404).
var errNotFound = errors.New("not found")

// Source is the nuget datasource.
type Source struct {
	getter httpx.Getter
	getenv func(string) string
	def    string // the default feed's service index

	services datasource.Memo[string, *service] // by feed URL
	pkgs     datasource.Memo[string, *pkg]     // by feed URL + "\x00" + lower-case id
	merged   datasource.Memo[string, *pkg]     // by dep source + "\x00" + lower-case id
	repos    datasource.Memo[string, string]   // by feed URL + "\x00" + lower-case id + "\x00" + version
}

// feed is one package source of a dep: its nuget.config key ("" when none) and URL.
type feed struct{ key, url string }

// service is what a feed's service index names: the registration base (the SemVer 2.0 one
// when the feed has it) and the flat container base, each "" when absent.
type service struct{ registration, flat string }

// pkg is what feeds list for a package: every listed version once, with its publish time, and
// the project URL and flat container base of the feed that answered first.
type pkg struct {
	rels    []datasource.Release
	times   map[string]time.Time
	project string
	feed    feed
	flat    string
}

// New returns the nuget datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies DEPS_NUGET_URL and the NuGetPackageSourceCredentials_<key> credentials of feeds.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, getenv: getenv, def: cmp.Or(getenv("DEPS_NUGET_URL"), DefaultURL)}
}

// Releases lists every listed version of d on its feeds (d.Source, see feeds; "" = nuget.org),
// merged in feed order, as NuGet restores from all of them. Unlisted versions are left out. A
// package none of the feeds has fails with `package <id> not found`; a dep whose feeds are all
// v2 feeds is skipped. The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the registration's `published` time.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[strings.ToLower(version)], nil
}

// Digest is unsupported: NuGet versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// SourceRepo implements datasource.SourceRepo: the `<repository url>` of version's nuspec on
// the flat container of the feed that has the package, else its project URL when that is on a
// forge.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	p, err := s.lookup(ctx, d)
	if err != nil || p.flat == "" {
		return "", err
	}
	id := strings.ToLower(d.Name)
	v := strings.ToLower(version)
	repo, err := s.repos.Do(ctx, p.feed.url+"\x00"+id+"\x00"+v, func() (string, error) {
		var spec struct {
			Repository struct {
				URL string `xml:"url,attr"`
			} `xml:"metadata>repository"`
		}
		err := s.get(ctx, p.feed, p.flat+url.PathEscape(id)+"/"+url.PathEscape(v)+"/"+url.PathEscape(id)+".nuspec", func(r io.Reader) error {
			return xml.NewDecoder(r).Decode(&spec)
		})
		if errors.Is(err, errNotFound) {
			return "", nil
		}
		return spec.Repository.URL, err
	})
	if err != nil {
		return "", err
	}
	if repo == "" && onForge(p.project) {
		repo = p.project
	}
	return repo, nil
}

// feeds returns the package sources of d.Source in lookup order: whitespace-separated entries
// `<key>=<url>` (the key query-escaped) or `<url>`; "" is nuget.org. nuget.org's service index
// is read from DEPS_NUGET_URL when that is set.
func (s *Source) feeds(d dep.Dep) []feed {
	var out []feed
	for e := range strings.FieldsSeq(d.Source) {
		f := feed{url: e}
		if k, u, ok := strings.Cut(e, "="); ok && !strings.Contains(k, "/") {
			key, err := url.QueryUnescape(k)
			if err == nil {
				f = feed{key: key, url: u}
			}
		}
		if f.url == DefaultURL {
			f.url = s.def
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		out = []feed{{url: s.def}}
	}
	return out
}

// v3 reports whether the feed URL is a v3 service index (`…/index.json`); v2 feeds
// (`https://www.powershellgallery.com/api/v2`) serve the OData API, which is not read.
func v3(u string) bool {
	u, _, _ = strings.Cut(u, "?")
	u, _, _ = strings.Cut(u, "#")
	return strings.HasSuffix(strings.ToLower(u), ".json")
}

// lookup returns the listing of d's package merged over its feeds.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	id := strings.ToLower(d.Name)
	return s.merged.Do(ctx, d.Source+"\x00"+id, func() (*pkg, error) {
		var out *pkg
		v2 := 0
		feeds := s.feeds(d)
		for _, f := range feeds {
			if !v3(f.url) {
				v2++
				slog.DebugContext(ctx, "nuget v2 feed not read", "feed", httpx.Redact(f.url), "name", d.Name)
				continue
			}
			p, err := s.pkgs.Do(ctx, f.url+"\x00"+id, func() (*pkg, error) { return s.fetch(ctx, f, id) })
			if errors.Is(err, errNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if out == nil {
				out = &pkg{times: map[string]time.Time{}, project: p.project, feed: p.feed, flat: p.flat}
			}
			for _, r := range p.rels {
				k := strings.ToLower(r.Version)
				if _, seen := out.times[k]; !seen {
					out.rels = append(out.rels, r)
					out.times[k] = p.times[k]
				}
			}
		}
		switch {
		case out != nil:
			return out, nil
		case v2 == len(feeds):
			return nil, &dep.SkipError{Reason: "NuGet v2 feed (only v3 feeds are read)"}
		}
		return nil, fmt.Errorf("package %s not found", d.Name)
	})
}

// index is the part of a service index that is read.
type index struct {
	Resources []struct {
		ID   string `json:"@id"`
		Type string `json:"@type"`
	} `json:"resources"`
}

// registrationTypes are the registration resource types in order of preference: SemVer 2.0
// versions are only listed by 3.6.0.
var registrationTypes = []string{"RegistrationsBaseUrl/3.6.0", "RegistrationsBaseUrl/Versioned", "RegistrationsBaseUrl/3.4.0", "RegistrationsBaseUrl/3.0.0-rc", "RegistrationsBaseUrl"}

// service reads the service index of f once.
func (s *Source) service(ctx context.Context, f feed) (*service, error) {
	return s.services.Do(ctx, f.url, func() (*service, error) {
		var ix index
		err := s.get(ctx, f, f.url, func(r io.Reader) error { return json.UnmarshalRead(r, &ix) })
		if errors.Is(err, errNotFound) {
			return nil, fmt.Errorf("nuget %s: service index not found (HTTP 404)", httpx.Redact(f.url))
		}
		if err != nil {
			return nil, err
		}
		svc := &service{}
		rank := len(registrationTypes)
		for _, r := range ix.Resources {
			base := r.ID
			if base != "" && !strings.HasSuffix(base, "/") {
				base += "/"
			}
			if i := slices.Index(registrationTypes, r.Type); i >= 0 && i < rank {
				svc.registration, rank = base, i
			}
			if r.Type == "PackageBaseAddress/3.0.0" && svc.flat == "" {
				svc.flat = base
			}
		}
		if svc.registration == "" && svc.flat == "" {
			return nil, fmt.Errorf("nuget %s: service index has no registration or package base address", httpx.Redact(f.url))
		}
		return svc, nil
	})
}

// registration is a registration index or page; leaf items carry the catalog entry.
type registration struct {
	Items []page `json:"items"`
}

type page struct {
	ID    string `json:"@id"`
	Items []leaf `json:"items"` // nil = not inlined: GET the page
}

type leaf struct {
	Entry struct {
		Version    string `json:"version"`
		Listed     *bool  `json:"listed"`
		Published  string `json:"published"`
		ProjectURL string `json:"projectUrl"`
	} `json:"catalogEntry"`
}

// fetch lists id (lower-case) on f: through the registration index
// (`<registration>/<id>/index.json`, pages not inlined fetched concurrently), else through the
// flat container (`<flat>/<id>/index.json`, versions only).
func (s *Source) fetch(ctx context.Context, f feed, id string) (p *pkg, err error) {
	start := time.Now()
	defer func() {
		n := 0
		if p != nil {
			n = len(p.rels)
		}
		slog.DebugContext(ctx, "nuget package fetched", "name", id, "feed", httpx.Redact(f.url), "versions", n, "duration", time.Since(start), "err", err)
	}()
	svc, err := s.service(ctx, f)
	if err != nil {
		return nil, err
	}
	if svc.registration == "" {
		return s.flatVersions(ctx, f, svc.flat, id)
	}
	var reg registration
	if err := s.get(ctx, f, svc.registration+url.PathEscape(id)+"/index.json", func(r io.Reader) error {
		return json.UnmarshalRead(r, &reg)
	}); err != nil {
		return nil, err
	}
	pages := reg.Items
	var wg sync.WaitGroup
	errs := make([]error, len(pages))
	for i := range pages {
		if pages[i].Items != nil {
			continue
		}
		wg.Go(func() {
			var pg page
			errs[i] = s.get(ctx, f, pages[i].ID, func(r io.Reader) error { return json.UnmarshalRead(r, &pg) })
			pages[i].Items = pg.Items
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	p = &pkg{times: map[string]time.Time{}, feed: f, flat: svc.flat}
	for _, pg := range pages {
		for _, l := range pg.Items {
			e := l.Entry
			v, _, _ := strings.Cut(e.Version, "+") // build metadata does not count
			t, terr := time.Parse(time.RFC3339, e.Published)
			// nuget.org marks unlisted versions with the publish year 1900 besides listed: false.
			if v == "" || e.Listed != nil && !*e.Listed || terr == nil && t.Year() <= 1900 {
				continue
			}
			k := strings.ToLower(v)
			if _, seen := p.times[k]; seen {
				continue
			}
			p.rels = append(p.rels, datasource.Release{Version: v})
			p.times[k] = t // zero when unparsable
			if e.ProjectURL != "" {
				p.project = e.ProjectURL // the newest version's: pages and items are ascending
			}
		}
	}
	return p, nil
}

// flatVersions lists id on a feed without registration through the flat container: every
// version (lower-case, normalized), unlisted ones included, without times.
func (s *Source) flatVersions(ctx context.Context, f feed, base, id string) (*pkg, error) {
	var list struct {
		Versions []string `json:"versions"`
	}
	if err := s.get(ctx, f, base+url.PathEscape(id)+"/index.json", func(r io.Reader) error {
		return json.UnmarshalRead(r, &list)
	}); err != nil {
		return nil, err
	}
	p := &pkg{times: map[string]time.Time{}, feed: f, flat: base}
	for _, v := range list.Versions {
		v, _, _ = strings.Cut(v, "+")
		if _, seen := p.times[v]; v != "" && !seen {
			p.rels = append(p.rels, datasource.Release{Version: v})
			p.times[v] = time.Time{}
		}
	}
	return p, nil
}

// get GETs target and passes the body to read (gunzipped when the server compressed it
// unasked, as registration5-gz does). A 404 is errNotFound. Credentials, on requests to the
// feed's host only: the target's userinfo (as basic auth, by net/http), else NuGet's
// NuGetPackageSourceCredentials_<key> variable of the feed (`Username=<user>;Password=<password>`).
func (s *Source) get(ctx context.Context, f feed, target string, read func(io.Reader) error) error {
	g := s.getter
	if f.key != "" {
		g.Prepare = func(req *http.Request) error {
			if req.URL.User == nil && sameHost(f.url, req.URL) {
				if user, pass, ok := credentials(s.getenv(CredentialsEnv + f.key)); ok {
					req.SetBasicAuth(user, pass)
				}
			}
			return nil
		}
	}
	resp, err := g.Open(ctx, target)
	switch {
	case errors.Is(err, httpx.ErrNotFound):
		return errNotFound
	case err != nil:
		return err
	}
	defer resp.Body.Close()
	body := io.Reader(resp.Body)
	if !resp.Uncompressed && strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("GET %s: %w", httpx.Redact(target), err)
		}
		defer zr.Close()
		body = zr
	}
	if err := read(body); err != nil {
		return fmt.Errorf("GET %s: %w", httpx.Redact(target), err)
	}
	return nil
}

// sameHost reports whether u is on the host of the feed URL raw.
func sameHost(raw string, u *url.URL) bool {
	f, err := url.Parse(raw)
	return err == nil && strings.EqualFold(f.Host, u.Host)
}

// credentials parses NuGet's `Username=<user>;Password=<password>` credential value (keys
// case-insensitive, surrounding quotes dropped).
func credentials(v string) (user, pass string, ok bool) {
	v = strings.Trim(strings.TrimSpace(v), `"`)
	for part := range strings.SplitSeq(v, ";") {
		k, val, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "username":
			user = val
		case "password":
			pass, ok = val, true
		}
	}
	return user, pass, ok
}

// onForge reports whether raw is an http(s) URL on github.com, gitlab.com or codeberg.org.
func onForge(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "gitlab.com", "codeberg.org":
		return strings.Count(strings.Trim(u.Path, "/"), "/") >= 1
	}
	return false
}
