// Package gotoolchain lists Go toolchain releases from go.dev for go/toolchain directives.
package gotoolchain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
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
	getter     httpx.Getter
	url        string
	historyURL string

	releases datasource.Once[[]datasource.Release]
	dates    datasource.Once[map[string]time.Time]
}

// New returns the go-toolchain datasource. sem bounds concurrent requests (nil = unbounded).
func New(client *http.Client, sem chan struct{}) datasource.Datasource {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, url: defaultURL, historyURL: defaultHistoryURL}
}

// Releases returns every stable "X.Y.Z" plus a derived "X.Y" per stable minor, so 2-part
// go directives have 2-part candidates under tag versioning.
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	return s.releases.Do(ctx, func() ([]datasource.Release, error) {
		rels, err := s.fetch(ctx)
		slog.DebugContext(ctx, "go toolchain releases", "url", s.url, "releases", len(rels), "err", err)
		return rels, err
	})
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// ReleaseTime implements datasource.Timer with the release date (midnight UTC) of version
// from Go's release history; a derived "X.Y" has the date of X.Y.0. A version missing from the
// history is zero.
func (s *Source) ReleaseTime(ctx context.Context, _ dep.Dep, version string) (time.Time, error) {
	dates, err := s.dates.Do(ctx, func() (map[string]time.Time, error) {
		dates, err := s.history(ctx)
		slog.DebugContext(ctx, "go release history", "url", s.historyURL, "releases", len(dates), "err", err)
		return dates, err
	})
	return dates[version], err
}

// historyRe matches an entry of the history's Releases table, e.g.
// `Date: Date{2026, 8, 19}, Version: Version{1, 27, 0},`.
var historyRe = regexp.MustCompile(`Date:\s*Date\{(\d+),\s*(\d+),\s*(\d+)\},\s*Version:\s*Version\{(\d+),\s*(\d+),\s*(\d+)\}`)

// history fetches the release history: "X.Y.Z" -> date, plus "X.Y" for X.Y.0, the name of
// releases before Go 1.21 and the derived minors of Releases.
func (s *Source) history(ctx context.Context) (map[string]time.Time, error) {
	var dates map[string]time.Time
	g := s.getter
	g.MaxBody = 4 << 20
	err := g.Stream(ctx, s.historyURL, func(r io.Reader) error {
		body, err := io.ReadAll(r)
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

func (s *Source) fetch(ctx context.Context) ([]datasource.Release, error) {
	var out []datasource.Release
	err := s.getter.Stream(ctx, s.url, func(r io.Reader) error {
		var err error
		out, err = parseReleases(r)
		return err
	})
	return out, err
}

// release is an element of the go.dev release list.
type release struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// parseReleases decodes the go.dev release list.
func parseReleases(r io.Reader) ([]datasource.Release, error) {
	var out []datasource.Release
	minors := map[string]bool{}
	err := datasource.DecodeArray(r, func(rel release) {
		v, ok := strings.CutPrefix(rel.Version, "go")
		if !ok || !rel.Stable {
			return
		}
		if strings.Count(v, ".") == 1 {
			if minors[v] {
				return
			}
			minors[v] = true
		}
		out = append(out, datasource.Release{Version: v})
		if i := strings.LastIndexByte(v, '.'); i > 0 && strings.Count(v, ".") == 2 && !minors[v[:i]] {
			minors[v[:i]] = true
			out = append(out, datasource.Release{Version: v[:i]})
		}
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no stable releases")
	}
	return out, nil
}
