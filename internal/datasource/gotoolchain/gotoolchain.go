// Package gotoolchain lists Go toolchain releases from go.dev for go/toolchain directives.
package gotoolchain

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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name.
const Name = "go-toolchain"

const (
	defaultURL = "https://go.dev/dl/?mode=json&include=all"
	// defaultHistoryURL is Go's release history with release dates (the go.dev list has none),
	// the source of Renovate's golang-version release timestamps.
	defaultHistoryURL = "https://raw.githubusercontent.com/golang/website/HEAD/internal/history/release.go"
)

// Source fetches the release list once per run, and the release dates once on first use.
type Source struct {
	client     *http.Client
	sem        chan struct{}
	url        string
	historyURL string

	once     sync.Once
	releases []datasource.Release
	err      error

	historyOnce sync.Once
	dates       map[string]time.Time
	historyErr  error
}

// New returns the go-toolchain datasource. sem bounds concurrent requests (nil = unbounded).
func New(client *http.Client, sem chan struct{}) datasource.Datasource {
	return &Source{client: client, sem: sem, url: defaultURL, historyURL: defaultHistoryURL}
}

// Releases returns every stable "X.Y.Z" plus a derived "X.Y" per stable minor, so 2-part
// go directives have 2-part candidates under tag versioning.
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	s.once.Do(func() {
		s.releases, s.err = s.fetch(ctx)
		slog.DebugContext(ctx, "go toolchain releases", "url", s.url, "releases", len(s.releases), "err", s.err)
	})
	return s.releases, s.err
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// ReleaseTime implements datasource.Timer with the release date (midnight UTC) of version
// from Go's release history; a derived "X.Y" has the date of X.Y.0. A version missing from the
// history is zero.
func (s *Source) ReleaseTime(ctx context.Context, _ dep.Dep, version string) (time.Time, error) {
	s.historyOnce.Do(func() {
		s.dates, s.historyErr = s.history(ctx)
		slog.DebugContext(ctx, "go release history", "url", s.historyURL, "releases", len(s.dates), "err", s.historyErr)
	})
	return s.dates[version], s.historyErr
}

// historyRe matches an entry of the history's Releases table, e.g.
// `Date: Date{2026, 8, 19}, Version: Version{1, 27, 0},`.
var historyRe = regexp.MustCompile(`Date:\s*Date\{(\d+),\s*(\d+),\s*(\d+)\},\s*Version:\s*Version\{(\d+),\s*(\d+),\s*(\d+)\}`)

// history fetches the release history: "X.Y.Z" -> date, plus "X.Y" for X.Y.0, the name of
// releases before Go 1.21 and the derived minors of Releases.
func (s *Source) history(ctx context.Context) (map[string]time.Time, error) {
	var dates map[string]time.Time
	err := s.get(ctx, s.historyURL, func(r io.Reader) error {
		body, err := io.ReadAll(io.LimitReader(r, 4<<20))
		if err != nil {
			return err
		}
		ms := historyRe.FindAllSubmatch(body, -1)
		dates = make(map[string]time.Time, 2*len(ms))
		for _, m := range ms {
			var n [6]int
			for i := range n {
				n[i], _ = strconv.Atoi(string(m[i+1])) // \d+ of a Go source file: small
			}
			t := time.Date(n[0], time.Month(n[1]), n[2], 0, 0, 0, 0, time.UTC)
			dates[fmt.Sprintf("%d.%d.%d", n[3], n[4], n[5])] = t
			if n[5] == 0 {
				dates[fmt.Sprintf("%d.%d", n[3], n[4])] = t
			}
		}
		if len(dates) == 0 {
			return errors.New("no releases")
		}
		return nil
	})
	return dates, err
}

// get GETs url and passes the body of a 200 response to read.
func (s *Source) get(ctx context.Context, url string, read func(io.Reader) error) error {
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	if err := read(resp.Body); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return nil
}

func (s *Source) fetch(ctx context.Context) ([]datasource.Release, error) {
	var out []datasource.Release
	err := s.get(ctx, s.url, func(r io.Reader) error {
		var err error
		out, err = parseReleases(r)
		return err
	})
	return out, err
}

// parseReleases decodes the go.dev release list.
func parseReleases(r io.Reader) ([]datasource.Release, error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true)) // no per-object name sets
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return nil, cmp.Or(err, errors.New("not an array"))
	}
	var out []datasource.Release
	minors := map[string]bool{}
	for dec.PeekKind() != ']' {
		var rel struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		}
		if err := json.UnmarshalDecode(dec, &rel); err != nil {
			return nil, err
		}
		v, ok := strings.CutPrefix(rel.Version, "go")
		if !ok || !rel.Stable {
			continue
		}
		if strings.Count(v, ".") == 1 {
			if minors[v] {
				continue
			}
			minors[v] = true
		}
		out = append(out, datasource.Release{Version: v})
		if i := strings.LastIndexByte(v, '.'); i > 0 && strings.Count(v, ".") == 2 && !minors[v[:i]] {
			minors[v[:i]] = true
			out = append(out, datasource.Release{Version: v[:i]})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no stable releases")
	}
	return out, nil
}
