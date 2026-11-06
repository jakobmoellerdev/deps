// Package artifactory lists versions from the HTML directory listing of a JFrog Artifactory
// (generic) repository folder, as Renovate's artifactory datasource does: every entry of
// `<registryUrl>/<packageName>/` is a version (folder names without the trailing slash, or
// file names for extractVersion to pick apart), and the date next to it its release time.
package artifactory

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "artifactory"

// maxListing bounds the bytes read of one directory listing.
const maxListing = 32 << 20

var (
	// linkRe is a hyperlink and the text after it up to the next tag or line break: the
	// listing's `<a href="1.0.0/">1.0.0/</a>  21-Jul-2021 20:08    -`.
	linkRe = regexp.MustCompile(`(?is)<a\s[^>]*>([^<]*)</a>([^<\r\n]*)`)
	// columnsRe separates the listing's columns (two or more spaces).
	columnsRe = regexp.MustCompile(`\s{2,}`)
)

// timeLayouts are the date formats of Artifactory listings (and of the nginx/Apache
// autoindex pages Renovate's parser accepts); all are UTC.
var timeLayouts = []string{"02-Jan-2006 15:04", "02-Jan-2006 15:04:05", "2006-01-02 15:04", "2006-01-02 15:04:05", time.RFC3339}

// Source is the artifactory datasource.
type Source struct {
	client *http.Client
	sem    chan struct{}
	auth   *httpx.Auth // nil = anonymous

	folders datasource.Memo[string, *folder] // by folder URL
}

// folder is the release list of a folder and the release time of each version.
type folder struct {
	rels  []datasource.Release
	times map[string]time.Time
}

// New returns the artifactory datasource. sem bounds concurrent requests (nil = unbounded);
// auth supplies Basic credentials for the listing hosts (nil = anonymous).
func New(client *http.Client, sem chan struct{}, auth *httpx.Auth) *Source {
	return &Source{client: client, sem: sem, auth: auth}
}

// Releases lists the entries of the folder of d in every registry URL of d.Source, in listing
// order, the first registry's first; an entry listed by several registries counts once.
// The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	f, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return f.rels, nil
}

// ReleaseTime implements datasource.Timer with the date the listing shows next to version;
// zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	f, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return f.times[version], nil
}

// Digest is unsupported: listing entries have no digest.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// URLs returns the folder URLs of d: each whitespace-separated registry URL of d.Source joined
// with d.Name, or d.Name itself without a registry URL; each ends with '/'.
func URLs(d dep.Dep) []string {
	regs := strings.Fields(d.Source)
	if len(regs) == 0 {
		return []string{strings.TrimRight(d.Name, "/") + "/"}
	}
	out := make([]string, len(regs))
	for i, r := range regs {
		out[i] = strings.TrimRight(r, "/") + "/" + strings.Trim(d.Name, "/") + "/"
	}
	return out
}

// lookup lists the folders of d once each and merges them.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*folder, error) {
	urls := URLs(d)
	if len(urls) == 1 {
		return s.list(ctx, urls[0])
	}
	merged := &folder{times: map[string]time.Time{}}
	var errs []error
	for _, u := range urls {
		f, err := s.list(ctx, u)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, r := range f.rels {
			if _, ok := merged.times[r.Version]; ok {
				continue
			}
			merged.rels = append(merged.rels, r)
			merged.times[r.Version] = f.times[r.Version]
		}
	}
	if len(merged.rels) == 0 {
		return nil, fmt.Errorf("no versions in %s: %w", strings.Join(urls, ", "), joinErrs(errs))
	}
	return merged, nil
}

// joinErrs reads as the first error, or "empty listings" without errors.
func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return errEmpty
	}
	return errs[0]
}

var errEmpty = errors.New("empty listings")

// list fetches and parses the directory listing at u once per run. The platform token of
// u's host is sent as Basic auth; after a 401 the git credential helper is asked once.
func (s *Source) list(ctx context.Context, u string) (*folder, error) {
	return s.folders.Do(u, func() (*folder, error) {
		start := time.Now()
		f, err := s.fetch(ctx, u)
		n := 0
		if f != nil {
			n = len(f.rels)
		}
		slog.DebugContext(ctx, "artifactory listing", "url", u, "versions", n, "duration", time.Since(start), "err", err)
		return f, err
	})
}

func (s *Source) fetch(ctx context.Context, u string) (*folder, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return nil, err
	}
	defer release()
	user, pass, ok := s.auth.Static(parsed.Hostname())
	resp, err := s.get(ctx, u, user, pass, ok)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		if user, pass, ok = s.auth.Helper(ctx, parsed.Hostname()); ok {
			resp.Body.Close()
			resp, err = s.get(ctx, u, user, pass, ok)
		}
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListing))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	f := parse(body)
	if len(f.rels) == 0 {
		return nil, fmt.Errorf("GET %s: no versions listed", u)
	}
	return f, nil
}

// get GETs u, with Basic credentials when auth is set.
func (s *Source) get(ctx context.Context, u, user, pass string, auth bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	if auth {
		req.SetBasicAuth(user, pass)
	}
	return s.client.Do(req)
}

// parse reads the entries of an HTML directory listing: the text of every link but the parent
// folder's, without a trailing slash, and the date in the column after it.
func parse(body []byte) *folder {
	f := &folder{times: map[string]time.Time{}}
	for _, m := range linkRe.FindAllSubmatch(body, -1) {
		name := strings.TrimSpace(html.UnescapeString(string(m[1])))
		if name == ".." || name == "../" {
			continue
		}
		v := strings.TrimSuffix(name, "/")
		if v == "" {
			continue
		}
		if _, dup := f.times[v]; dup {
			continue
		}
		f.rels = append(f.rels, datasource.Release{Version: v})
		f.times[v] = parseTime(string(m[2]))
	}
	return f
}

// parseTime parses the first column of rest, the text after a link; zero when it is no date.
func parseTime(rest string) time.Time {
	col := columnsRe.Split(strings.TrimSpace(rest), 2)[0]
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, col); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
