// Package rubyversion lists Ruby releases from the release list of ruby-lang.org.
package rubyversion

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "ruby-version"

// DefaultURL is the release list the ruby-lang.org downloads pages are generated from (one
// YAML entry per release with its version and date); DEPS_RUBY_RELEASES_URL overrides it.
const DefaultURL = "https://raw.githubusercontent.com/ruby/www.ruby-lang.org/master/_data/releases.yml"

// Source fetches the release list once per run.
type Source struct {
	getter httpx.Getter
	url    string

	index datasource.Once[index]
}

// index is the parsed release list.
type index struct {
	releases []datasource.Release
	times    map[string]time.Time
}

// New returns the ruby-version datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_RUBY_RELEASES_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, url: cmp.Or(getenv("DEPS_RUBY_RELEASES_URL"), DefaultURL)}
}

// Releases returns every release as listed (`3.4.7`, prereleases such as `3.5.0-preview1`
// included; schemes drop them for stable current versions), then a derived "X.Y" and "X" per
// stable release line, so partial values (`.ruby-version` `3.4`) have candidates of their
// precision under tag versioning. The derived entries come last, so a scheme that compares
// `3.4` equal to 3.4.0 (ruby) prefers the published `3.4.0` on a tie.
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	ix, err := s.load(ctx)
	return ix.releases, err
}

// ReleaseTime implements datasource.Timer with the release date of version; a derived
// "X.Y"/"X" has the date of the line's first release. Zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, _ dep.Dep, version string) (time.Time, error) {
	ix, err := s.load(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return ix.times[version], nil
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

func (s *Source) load(ctx context.Context) (index, error) {
	return s.index.Do(ctx, func() (index, error) {
		start := time.Now()
		var ix index
		err := s.getter.Stream(ctx, s.url, ix.read)
		if err != nil {
			ix = index{}
		}
		slog.DebugContext(ctx, "ruby releases", "url", s.url, "releases", len(ix.releases), "duration", time.Since(start), "err", err)
		return ix, err
	})
}

// read parses the release list: a YAML sequence of mappings whose `version` and `date` keys
// are scalars (`- version: 3.4.7` then `  date: 2025-10-07`). Only those two keys are read,
// line by line; the nested url and checksum mappings are skipped.
func (ix *index) read(r io.Reader) error {
	ix.times = map[string]time.Time{}
	var derived []datasource.Release
	var version, date string
	flush := func() {
		if version == "" {
			return
		}
		ix.releases = append(ix.releases, datasource.Release{Version: version})
		t, err := time.Parse(time.DateOnly, date)
		if err == nil {
			ix.times[version] = t
		}
		if !strings.ContainsAny(version, "-+") && strings.Count(version, ".") == 2 {
			minor := version[:strings.LastIndexByte(version, '.')]
			for _, p := range []string{minor, version[:strings.IndexByte(version, '.')]} {
				first, seen := ix.times[p]
				if !seen {
					derived = append(derived, datasource.Release{Version: p})
				}
				if err == nil && (first.IsZero() || t.Before(first)) {
					ix.times[p] = t
				} else if !seen {
					ix.times[p] = time.Time{} // marks the line as seen
				}
			}
		}
		version, date = "", ""
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), " \t\r")
		entry := strings.HasPrefix(l, "- ")
		if entry {
			flush()
			l = "  " + l[2:]
		}
		key, value, ok := mappingEntry(l)
		if !ok {
			continue
		}
		switch key {
		case "version":
			version = value
		case "date":
			date = value
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush()
	if len(ix.releases) == 0 {
		return errors.New("no releases")
	}
	ix.releases = append(ix.releases, derived...)
	return nil
}

// mappingEntry returns the key and scalar value of a `  key: value` line of an entry (indented
// by exactly two blanks), without quotes and a trailing comment.
func mappingEntry(l string) (key, value string, ok bool) {
	rest, ok := strings.CutPrefix(l, "  ")
	if !ok || rest == "" || rest[0] == ' ' || rest[0] == '#' {
		return "", "", false
	}
	key, value, ok = strings.Cut(rest, ":")
	if !ok {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
		if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
			return key, value[1 : end+1], true
		}
	}
	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return key, value, true
}
