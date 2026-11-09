// Package jenkinsplugins lists the versions of Jenkins plugins from the Jenkins update center
// (updates.jenkins.io, or an update site with its layout): its plugin-versions.json is read once
// per run, keeping only the plugins the run asks for, and shared by every plugin list.
package jenkinsplugins

import (
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
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "jenkins-plugins"

// DefaultURL is the Jenkins update center; DEPS_JENKINS_PLUGINS_URL overrides it (a mirror or
// an update site with its layout).
const DefaultURL = "https://updates.jenkins.io"

// The files of an update site (update-center2's LAYOUT.md), below its URL.
const (
	versionsPath = "/current/plugin-versions.json"      // every version of every plugin
	infoPath     = "/current/update-center.actual.json" // the newest version of every plugin with its `scm`
)

// buildDateLayout is the layout of a version's `buildDate` (`Feb 15, 2011`).
const buildDateLayout = "Jan 2, 2006"

// Source is the jenkins-plugins datasource.
type Source struct {
	getter httpx.Getter
	base   string // update site URL without a trailing slash

	mu     sync.Mutex
	wanted map[string]bool // the plugins Plan recorded; nil = Plan was not called

	index datasource.Once[*index]            // one scan of plugin-versions.json
	late  datasource.Memo[string, *plugin]   // plugins asked for after the scan started
	scms  datasource.Once[map[string]string] // plugin → `scm` of update-center.actual.json
}

// index is the scan of plugin-versions.json.
type index struct {
	wanted  map[string]bool // the plugins the scan kept; nil = all
	plugins map[string]*plugin
}

// plugin is the versions of a plugin in file order and their release times.
type plugin struct {
	rels  []datasource.Release
	times map[string]time.Time
}

// New returns the jenkins-plugins datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_JENKINS_PLUGINS_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{
		// plugin-versions.json runs to tens of MB and is decoded as it arrives.
		getter: httpx.Getter{Client: client, Sem: sem, MaxBody: -1},
		base:   strings.TrimSuffix(cmp.Or(getenv("DEPS_JENKINS_PLUGINS_URL"), DefaultURL), "/"),
	}
}

// Plan records the plugins the run will ask for, so the one scan keeps all of them and no
// other (datasource.Planner).
func (s *Source) Plan(deps []dep.Dep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wanted == nil {
		s.wanted = make(map[string]bool, len(deps))
	}
	for _, d := range deps {
		s.wanted[d.Name] = true
	}
}

// Releases lists the versions of plugin d.Name in the update site's order. A plugin the site
// does not have (one whose distribution stopped) is skipped. The result is shared and must
// not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the version's `releaseTimestamp`, else its
// `buildDate`; zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d.Name)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// SourceRepo implements datasource.SourceRepo with the plugin's `scm` in the site's
// update-center.actual.json, read once and only for plugins that end up in a PR.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	scms, err := s.scms.Do(ctx, func() (map[string]string, error) {
		scms := map[string]string{}
		err := s.getter.Stream(ctx, s.base+infoPath, func(r io.Reader) error {
			return eachPlugin(r, func(name string, dec *jsontext.Decoder) error {
				var info struct {
					SCM string `json:"scm"`
				}
				if err := json.UnmarshalDecode(dec, &info); err != nil {
					return err
				}
				if info.SCM != "" {
					scms[name] = info.SCM
				}
				return nil
			})
		})
		return scms, err
	})
	if err != nil {
		return "", err
	}
	return scms[d.Name], nil
}

// Digest is unsupported: plugin versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// lookup returns plugin name, a *dep.SkipError when the site does not have it.
func (s *Source) lookup(ctx context.Context, name string) (*plugin, error) {
	ix, err := s.index.Do(ctx, func() (*index, error) {
		s.mu.Lock()
		wanted := maps.Clone(s.wanted)
		s.mu.Unlock()
		plugins, err := s.scan(ctx, func(n string) bool { return wanted == nil || wanted[n] })
		return &index{wanted: wanted, plugins: plugins}, err
	})
	if err != nil {
		return nil, err
	}
	p := ix.plugins[name]
	if ix.wanted != nil && !ix.wanted[name] {
		// Asked for after the scan started (Plan did not name it): scan again for it alone.
		p, err = s.late.Do(ctx, name, func() (*plugin, error) {
			plugins, err := s.scan(ctx, func(n string) bool { return n == name })
			return plugins[name], err
		})
		if err != nil {
			return nil, err
		}
	}
	if p == nil {
		return nil, &dep.SkipError{Reason: "plugin not found on " + s.base}
	}
	return p, nil
}

// scan reads the site's plugin-versions.json, keeping the plugins keep accepts.
func (s *Source) scan(ctx context.Context, keep func(string) bool) (map[string]*plugin, error) {
	start := time.Now()
	plugins := map[string]*plugin{}
	err := s.getter.Stream(ctx, s.base+versionsPath, func(r io.Reader) error {
		return eachPlugin(r, func(name string, dec *jsontext.Decoder) error {
			if !keep(name) {
				return dec.SkipValue()
			}
			p, err := readVersions(dec)
			plugins[name] = p
			return err
		})
	})
	slog.DebugContext(ctx, "jenkins plugin versions", "site", s.base, "plugins", len(plugins), "duration", time.Since(start), "err", err)
	if err != nil {
		return nil, err
	}
	return plugins, nil
}

// eachPlugin calls each with the name of every member of the top-level `plugins` object of
// an update site file; each must read the member's value from dec. Other members are skipped
// unread.
func eachPlugin(r io.Reader, each func(name string, dec *jsontext.Decoder) error) error {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true)) // no per-object name sets
	if err := openObject(dec); err != nil {
		return err
	}
	found := false
	for dec.PeekKind() == '"' {
		key, err := dec.ReadToken()
		if err != nil {
			return err
		}
		if key.String() != "plugins" {
			if err := dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		found = true
		if err := openObject(dec); err != nil {
			return err
		}
		for dec.PeekKind() == '"' {
			name, err := dec.ReadToken()
			if err != nil {
				return err
			}
			if err := each(name.String(), dec); err != nil {
				return err
			}
		}
		if _, err := dec.ReadToken(); err != nil { // the plugins' '}'
			return err
		}
	}
	if _, err := dec.ReadToken(); err != nil { // the document's '}'
		return err
	}
	if !found {
		return errors.New("no plugins object")
	}
	return nil
}

// openObject reads the '{' of an object.
func openObject(dec *jsontext.Decoder) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '{' {
		return fmt.Errorf("want an object, got %v", tok.Kind())
	}
	return nil
}

// readVersions reads a plugin's object of version → release (`releaseTimestamp`, `buildDate`).
func readVersions(dec *jsontext.Decoder) (*plugin, error) {
	p := &plugin{times: map[string]time.Time{}}
	if err := openObject(dec); err != nil {
		return p, err
	}
	for dec.PeekKind() == '"' {
		tok, err := dec.ReadToken()
		if err != nil {
			return p, err
		}
		v := tok.String() // the token is voided by the next read
		var rel struct {
			ReleaseTimestamp string `json:"releaseTimestamp"`
			BuildDate        string `json:"buildDate"`
		}
		if err := json.UnmarshalDecode(dec, &rel); err != nil {
			return p, err
		}
		p.rels = append(p.rels, datasource.Release{Version: v})
		if t, ok := releaseTime(rel.ReleaseTimestamp, rel.BuildDate); ok {
			p.times[v] = t
		}
	}
	_, err := dec.ReadToken() // '}'
	return p, err
}

// releaseTime parses a release's `releaseTimestamp` (`2011-02-15T02:53:18.00Z`), else its
// `buildDate` (`Feb 15, 2011`), as Renovate does.
func releaseTime(timestamp, buildDate string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, timestamp); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(buildDateLayout, buildDate); err == nil {
		return t, true
	}
	return time.Time{}, false
}
