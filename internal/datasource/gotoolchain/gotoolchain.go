// Package gotoolchain lists Go toolchain releases from go.dev for go/toolchain directives.
package gotoolchain

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name.
const Name = "go-toolchain"

const defaultURL = "https://go.dev/dl/?mode=json&include=all"

// Source fetches the release list once per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	url    string

	once     sync.Once
	releases []datasource.Release
	err      error
}

// New returns the go-toolchain datasource. sem bounds concurrent requests (nil = unbounded).
func New(client *http.Client, sem chan struct{}) datasource.Datasource {
	return &Source{client: client, sem: sem, url: defaultURL}
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
	return "", fmt.Errorf("%s: digests are not supported for %s", Name, d.Name)
}

func (s *Source) fetch(ctx context.Context) ([]datasource.Release, error) {
	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", s.url, resp.StatusCode)
	}
	dec := jsontext.NewDecoder(resp.Body)
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return nil, fmt.Errorf("GET %s: %w", s.url, cmp.Or(err, errors.New("not an array")))
	}
	var out []datasource.Release
	minors := map[string]bool{}
	for dec.PeekKind() != ']' {
		var r struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		}
		if err := json.UnmarshalDecode(dec, &r); err != nil {
			return nil, fmt.Errorf("GET %s: %w", s.url, err)
		}
		v, ok := strings.CutPrefix(r.Version, "go")
		if !ok || !r.Stable {
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
		return nil, errors.New("GET " + s.url + ": no stable releases")
	}
	return out, nil
}
