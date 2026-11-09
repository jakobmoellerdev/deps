// Package glasskubepackages lists the versions of Glasskube packages from a Glasskube package
// repository (`<repository>/<package>/versions.yaml`), the public one by default.
package glasskubepackages

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "glasskube-packages"

// Versioning is the versioning of Glasskube package versions (`v1.17.0+2`).
const Versioning = "glasskube"

// DefaultURL is Glasskube's public package repository (it redirects to
// https://glasskube.github.io/packages/packages); a dep's Source replaces it.
const DefaultURL = "https://packages.dl.glasskube.dev/packages"

// maxBody bounds a versions.yaml or package.yaml (a few KiB).
const maxBody = 4 << 20

// Source is the glasskube-packages datasource.
type Source struct {
	getter httpx.Getter

	pkgs  datasource.Memo[string, *pkg]   // by versions.yaml URL; nil = HTTP 404
	repos datasource.Memo[string, string] // source repository by package.yaml URL
}

// pkg is the versions.yaml of a package in one repository.
type pkg struct {
	repository string // the repository URL without a trailing slash
	latest     string // latestVersion
	rels       []datasource.Release
}

// New returns the glasskube-packages datasource. sem bounds concurrent requests (nil =
// unbounded).
func New(client *http.Client, sem chan struct{}) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem, MaxBody: maxBody}}
}

// Releases lists the versions of the package d.Name in the first repository of d.Source
// (whitespace-separated repository URLs, "" = DefaultURL) that has it, in versions.yaml
// order. The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// Latest implements datasource.LatestSource with the `latestVersion` of d's package from the
// earlier Releases lookup; "" without one.
func (s *Source) Latest(d dep.Dep) string {
	for _, r := range repositories(d) {
		p, ok := s.pkgs.Peek(versionsURL(r, d.Name))
		switch {
		case !ok:
			return ""
		case p != nil:
			return p.latest
		}
	}
	return ""
}

// SourceRepo implements datasource.SourceRepo with the reference labeled `GitHub` of the
// package.yaml of d's latest version (Renovate's sourceUrl), else the one labeled `Website`
// when it is on a forge; "" without either. The manifest describes every version.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.lookup(ctx, d)
	if err != nil || p.latest == "" {
		return "", err
	}
	u := p.repository + "/" + url.PathEscape(d.Name) + "/" + url.PathEscape(p.latest) + "/package.yaml"
	return s.repos.Do(ctx, u, func() (string, error) {
		var m struct {
			References []struct{ Label, URL string } `yaml:"references"`
		}
		if err := s.get(ctx, u, &m); err != nil {
			return "", err
		}
		var website string
		for _, r := range m.References {
			switch strings.ToLower(r.Label) {
			case "github":
				return r.URL, nil
			case "website":
				website = cmp.Or(website, r.URL)
			}
		}
		if onForge(website) {
			return website, nil
		}
		return "", nil
	})
}

// Digest is unsupported: Glasskube packages are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// repositories returns the repository URLs of d without trailing slashes: d.Source, else
// DefaultURL.
func repositories(d dep.Dep) []string {
	rs := strings.Fields(d.Source)
	if len(rs) == 0 {
		return []string{DefaultURL}
	}
	for i, r := range rs {
		rs[i] = strings.TrimRight(r, "/")
	}
	return rs
}

func versionsURL(repository, name string) string {
	return repository + "/" + url.PathEscape(name) + "/versions.yaml"
}

// lookup returns d's package from the first of its repositories that has it (Renovate's
// first-registry strategy): each versions.yaml is fetched once per run.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	if d.Name == "" || strings.ContainsAny(d.Name, "/?#") {
		return nil, fmt.Errorf("invalid glasskube package name %q", d.Name)
	}
	rs := repositories(d)
	for _, r := range rs {
		u := versionsURL(r, d.Name)
		p, err := s.pkgs.Do(ctx, u, func() (*pkg, error) {
			start := time.Now()
			p, err := s.fetch(ctx, r, u)
			slog.DebugContext(ctx, "glasskube package versions", "url", httpx.Redact(u), "releases", len(p.rels), "duration", time.Since(start), "err", err)
			if errors.Is(err, httpx.ErrNotFound) {
				return nil, nil
			}
			return p, err
		})
		if err != nil {
			return nil, err
		}
		if p != nil {
			return p, nil
		}
	}
	for i, r := range rs {
		rs[i] = httpx.Redact(r)
	}
	return nil, fmt.Errorf("glasskube package %s not found in %s", d.Name, strings.Join(rs, " "))
}

// fetch reads `GET <repository>/<name>/versions.yaml`: `latestVersion` and the `version` of
// each `versions` entry.
func (s *Source) fetch(ctx context.Context, repository, u string) (*pkg, error) {
	var doc struct {
		LatestVersion string `yaml:"latestVersion"`
		Versions      []struct {
			Version string `yaml:"version"`
		} `yaml:"versions"`
	}
	p := &pkg{repository: repository}
	if err := s.get(ctx, u, &doc); err != nil {
		return p, err
	}
	for _, v := range doc.Versions {
		if v.Version != "" {
			p.rels = append(p.rels, datasource.Release{Version: v.Version})
		}
	}
	if len(p.rels) == 0 {
		return p, fmt.Errorf("GET %s: no versions", httpx.Redact(u))
	}
	p.latest = doc.LatestVersion
	return p, nil
}

// get decodes the YAML document at u into v, reading at most maxBody bytes.
func (s *Source) get(ctx context.Context, u string, v any) error {
	return s.getter.Stream(ctx, u, func(r io.Reader) error {
		err := yaml.NewDecoder(r).Decode(v)
		if errors.Is(err, io.EOF) {
			return nil // an empty document
		}
		return err
	})
}

// onForge reports whether u is an http(s) URL on github.com, gitlab.com or codeberg.org.
func onForge(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" && p.Scheme != "http" {
		return false
	}
	switch strings.ToLower(p.Hostname()) {
	case "github.com", "gitlab.com", "codeberg.org":
		return true
	}
	return false
}
