// Package rubygems lists the releases of Ruby gems: from the rubygems.org API, and from other
// gem servers (a Gemfile `source`) through Bundler's compact index. Lookups run once per
// (server, gem) and are shared by every dep and Gemfile of a run.
package rubygems

import (
	"bufio"
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
)

// Name is the datasource name (Renovate's).
const Name = "rubygems"

// DefaultURL is rubygems.org; DEPS_RUBYGEMS_URL overrides it (a mirror serving its API).
const DefaultURL = "https://rubygems.org"

// errNotFound marks a server that does not have the gem (HTTP 404).
var errNotFound = errors.New("not found")

// Source is the rubygems datasource.
type Source struct {
	getter httpx.Getter
	getenv func(string) string
	base   string // the rubygems.org API base, without a trailing slash

	gems  datasource.Memo[string, *gem]   // by server + "\x00" + name
	repos datasource.Memo[string, string] // by name
}

// gem is what a server lists for a gem: every version with a Ruby platform build once (the
// other builds of it merged in) and, from the rubygems.org API, the earliest build time of
// each version.
type gem struct {
	rels  []datasource.Release
	times map[string]time.Time
}

// New returns the rubygems datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies DEPS_RUBYGEMS_URL and the BUNDLE_<HOST> credentials of gem servers.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	s := &Source{
		getter: httpx.Getter{Client: client, Sem: sem}, getenv: getenv,
		base: strings.TrimSuffix(cmp.Or(getenv("DEPS_RUBYGEMS_URL"), DefaultURL), "/"),
	}
	s.getter.Prepare = s.credentials
	return s
}

// Releases lists every published version of d (yanked versions are not listed by either
// API). d.Source holds the gem servers of the dep in lookup order, whitespace-separated ("" =
// rubygems.org); the first one that has the gem answers. The result is shared and must not be
// modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	g, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return g.rels, nil
}

// ReleaseTime implements datasource.Timer with the earliest build time of version on
// rubygems.org (`created_at`); zero for other servers, whose compact index has none.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	g, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return g.times[version], nil
}

// Digest is unsupported: gem versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// SourceRepo implements datasource.SourceRepo for gems of rubygems.org: the gem's
// `source_code_uri`, else its `homepage_uri` when that is on a forge. Both describe the latest
// release whatever the version; other servers publish no metadata API, so their gems have none.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	if slices.ContainsFunc(servers(d), func(srv string) bool { return !s.isDefault(srv) }) {
		return "", nil
	}
	return s.repos.Do(ctx, d.Name, func() (string, error) {
		var meta struct {
			SourceCodeURI string `json:"source_code_uri"`
			HomepageURI   string `json:"homepage_uri"`
		}
		err := s.get(ctx, s.base+"/api/v1/gems/"+url.PathEscape(d.Name)+".json", func(r io.Reader) error {
			return json.UnmarshalRead(r, &meta)
		})
		switch {
		case errors.Is(err, errNotFound):
			return "", nil
		case err != nil:
			return "", err
		case meta.SourceCodeURI != "":
			return meta.SourceCodeURI, nil
		case onForge(meta.HomepageURI):
			return meta.HomepageURI, nil
		}
		return "", nil
	})
}

// servers returns the gem servers of d in lookup order; "" is rubygems.org.
func servers(d dep.Dep) []string {
	if f := strings.Fields(d.Source); len(f) > 0 {
		return f
	}
	return []string{""}
}

// lookup returns the listing of d's gem on the first of its servers that has it.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*gem, error) {
	for _, srv := range servers(d) {
		g, err := s.gems.Do(ctx, srv+"\x00"+d.Name, func() (*gem, error) { return s.fetch(ctx, srv, d.Name) })
		if !errors.Is(err, errNotFound) {
			return g, err
		}
	}
	return nil, fmt.Errorf("gem %s not found", d.Name)
}

// isDefault reports whether srv is rubygems.org ("" or its URL, as Gemfiles write it).
func (s *Source) isDefault(srv string) bool {
	if srv == "" {
		return true
	}
	u, err := url.Parse(srv)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil &&
		(strings.EqualFold(u.Host, "rubygems.org") || strings.EqualFold(u.Host, "www.rubygems.org")) && strings.Trim(u.Path, "/") == ""
}

// fetch lists name on srv: rubygems.org through `GET /api/v1/versions/<name>.json`, any other
// server through the compact index (`GET <server>/info/<name>`), falling back to the versions
// API when the server has no compact index entry.
func (s *Source) fetch(ctx context.Context, srv, name string) (g *gem, err error) {
	start, api := time.Now(), "versions"
	defer func() {
		n := 0
		if g != nil {
			n = len(g.rels)
		}
		slog.DebugContext(ctx, "rubygems gem fetched", "name", name, "server", httpx.Redact(cmp.Or(srv, s.base)), "api", api,
			"versions", n, "duration", time.Since(start), "err", err)
	}()
	if s.isDefault(srv) {
		return s.versions(ctx, s.base, name)
	}
	u, err := url.Parse(srv)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		// The URL is not part of the error: it may hold credentials and does not parse to redact them.
		return nil, fmt.Errorf("rubygems: invalid source URL for gem %s", name)
	}
	base := strings.TrimSuffix(srv, "/")
	api = "compact index"
	g, err = s.compact(ctx, base, name)
	if errors.Is(err, errNotFound) {
		api = "versions"
		g, err = s.versions(ctx, base, name)
	}
	return g, err
}

// version is an element of the versions API response; other fields are skipped.
type version struct {
	Number    string `json:"number"`
	Platform  string `json:"platform"`
	CreatedAt string `json:"created_at"`
}

// rubyPlatform reports whether platform is the plain Ruby platform, the only build an MRI
// `gem install` of a version takes. A server that publishes no platform at all (an empty
// field or no `-<platform>` suffix) counts as Ruby.
func rubyPlatform(platform string) bool {
	return platform == "" || platform == "ruby"
}

// versions reads `<base>/api/v1/versions/<name>.json`: one entry per platform build, newest
// first. Only versions with a Ruby build are listed; the release time of a version is the
// earliest of all its builds.
func (s *Source) versions(ctx context.Context, base, name string) (*gem, error) {
	g := &gem{times: map[string]time.Time{}}
	listed := map[string]bool{}
	err := s.get(ctx, base+"/api/v1/versions/"+url.PathEscape(name)+".json", func(r io.Reader) error {
		return datasource.DecodeArray(r, func(v version) {
			if v.Number == "" {
				return
			}
			if rubyPlatform(v.Platform) && !listed[v.Number] {
				listed[v.Number] = true
				g.rels = append(g.rels, datasource.Release{Version: v.Number})
			}
			if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
				if old, seen := g.times[v.Number]; !seen || old.IsZero() || t.Before(old) {
					g.times[v.Number] = t
				}
			}
		})
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// compact reads the compact index `<base>/info/<name>`: after the `---` line, one line per
// build, `<version>[-<platform>] <dependencies>|<requirements>`. Only versions with a Ruby
// build (a line without a platform suffix) are listed.
func (s *Source) compact(ctx context.Context, base, name string) (*gem, error) {
	g := &gem{}
	err := s.get(ctx, base+"/info/"+url.PathEscape(name), func(r io.Reader) error {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		seen := map[string]bool{}
		body := false
		for sc.Scan() {
			line := sc.Text()
			if !body {
				body = line == "---"
				continue
			}
			v, _, _ := strings.Cut(line, " ")
			v, platform, _ := strings.Cut(v, "-") // the platform; gem versions have no "-"
			if v != "" && rubyPlatform(platform) && !seen[v] {
				seen[v] = true
				g.rels = append(g.rels, datasource.Release{Version: v})
			}
		}
		if err := sc.Err(); err != nil {
			return err
		}
		if !body {
			return errors.New("not a compact index (no --- line)")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// get GETs target and passes the body to read. A 404 is errNotFound.
func (s *Source) get(ctx context.Context, target string, read func(io.Reader) error) error {
	err := s.getter.Stream(ctx, target, read)
	if errors.Is(err, httpx.ErrNotFound) {
		return errNotFound
	}
	return err
}

// credentials authorizes req: by the target's userinfo (as basic auth, by net/http), else by
// Bundler's BUNDLE_<HOST> setting (`user:password`, or a token as the user).
func (s *Source) credentials(req *http.Request) error {
	if req.URL.User == nil {
		if cred := s.getenv(bundleKey(req.URL.Hostname())); cred != "" {
			user, pass, _ := strings.Cut(cred, ":")
			req.SetBasicAuth(user, pass)
		}
	}
	return nil
}

// bundleKey is the environment variable of Bundler's credentials for host
// (Bundler::Settings.key_for): `BUNDLE_` + the upper-cased host with "." as "__" and "-" as
// "___" (gems.example-corp.com → BUNDLE_GEMS__EXAMPLE___CORP__COM).
func bundleKey(host string) string {
	return "BUNDLE_" + strings.ToUpper(strings.NewReplacer(".", "__", "-", "___").Replace(host))
}

// onForge reports whether raw is an http(s) URL on github.com, gitlab.com or codeberg.org.
func onForge(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "www.github.com", "gitlab.com", "codeberg.org":
		return true
	}
	return false
}
