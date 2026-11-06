// Package dart lists the versions of Dart and Flutter packages from a pub repository (pub.dev,
// or a hosted repository a pubspec names): one request per package and repository and run.
package dart

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
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's).
const Name = "dart"

// DefaultURL is pub.dev; PUB_HOSTED_URL, which the pub client reads too, overrides it.
const DefaultURL = "https://pub.dev"

// Source is the dart datasource.
type Source struct {
	client *http.Client
	sem    chan struct{}
	base   string // default repository base URL without a trailing slash

	pkgs datasource.Memo[string, *pkg] // by repository base + "\x00" + package name
}

// pkg is the version list of a package, the publication time of each version and the
// repository the newest version's pubspec names.
type pkg struct {
	rels  []datasource.Release
	times map[string]time.Time
	repo  string
}

// New returns the dart datasource. sem bounds concurrent requests (nil = unbounded); getenv
// supplies PUB_HOSTED_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{client: client, sem: sem, base: strings.TrimSuffix(cmp.Or(getenv("PUB_HOSTED_URL"), DefaultURL), "/")}
}

// Releases lists every version of the package d.Name that is not retracted, as the repository
// lists them (oldest first). d.Source is the hosted repository of the dep ("" = the default
// one). The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// ReleaseTime implements datasource.Timer with the `published` time of version; zero when
// unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	return p.times[version], nil
}

// SourceRepo implements datasource.SourceRepo with the `repository` of the newest version's
// pubspec, else its `homepage`.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return "", err
	}
	return p.repo, nil
}

// Digest is unsupported: pub versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// response is the package listing of the pub repository API (`application/vnd.pub.v2+json`);
// other fields are skipped.
type response struct {
	Latest struct {
		Pubspec struct {
			Repository string `json:"repository"`
			Homepage   string `json:"homepage"`
		} `json:"pubspec"`
	} `json:"latest"`
	Versions []struct {
		Version   string `json:"version"`
		Published string `json:"published"`
		Retracted bool   `json:"retracted"`
	} `json:"versions"`
}

// lookup fetches `GET <base>/api/packages/<name>` once per repository and package.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	if d.Name == "" || strings.ContainsAny(d.Name, "/?#") {
		return nil, fmt.Errorf("invalid pub package name %q", d.Name)
	}
	base := strings.TrimSuffix(cmp.Or(d.Source, s.base), "/")
	return s.pkgs.Do(ctx, base+"\x00"+d.Name, func() (*pkg, error) {
		start := time.Now()
		u := base + "/api/packages/" + url.PathEscape(d.Name)
		p := &pkg{times: map[string]time.Time{}}
		err := datasource.GetStream(ctx, s.client, s.sem, u, func(r io.Reader) error {
			var res response
			if err := json.UnmarshalRead(r, &res); err != nil {
				return err
			}
			for _, v := range res.Versions {
				if v.Retracted || v.Version == "" {
					continue
				}
				p.rels = append(p.rels, datasource.Release{Version: v.Version})
				if t, err := time.Parse(time.RFC3339Nano, v.Published); err == nil {
					p.times[v.Version] = t.UTC()
				}
			}
			if len(p.rels) == 0 {
				return errors.New("no versions")
			}
			p.repo = cmp.Or(res.Latest.Pubspec.Repository, res.Latest.Pubspec.Homepage)
			return nil
		})
		slog.DebugContext(ctx, "pub package versions", "url", u, "versions", len(p.rels), "duration", time.Since(start), "err", err)
		if err != nil {
			return nil, err
		}
		return p, nil
	})
}
