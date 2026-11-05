// Package githubreleases lists the published releases of a GitHub (or GitHub Enterprise Server)
// repository. The release list is shared with git-tags release times (see githubapi).
package githubreleases

import (
	"context"
	"fmt"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
)

// Name is the datasource name.
const Name = "github-releases"

// Source is the github-releases datasource over a shared githubapi.Client.
type Source struct {
	gh *githubapi.Client
}

// New returns the github-releases datasource.
func New(gh *githubapi.Client) *Source { return &Source{gh: gh} }

// Releases lists the non-draft, non-prerelease releases of d.Name (`owner/repo` on github.com
// or `http(s)://host/owner/repo` on GitHub Enterprise Server), newest first. Pages are fetched
// only until d.Version's release is listed (everything newer precedes it; at most 500
// releases); Version is the tag name and Digest is empty. The slice must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	var rels []datasource.Release
	err := s.with(ctx, d.Name, d.Version, func(l *githubapi.List) { rels = l.Stable() })
	return rels, err
}

// with runs fn on the repository's release list after fetching pages until version's release
// is listed or no pages are left.
func (s *Source) with(ctx context.Context, name, version string, fn func(*githubapi.List)) error {
	r, err := githubapi.ParseRepo(name)
	if err != nil {
		return err
	}
	return s.gh.Releases(ctx, r, func(l *githubapi.List) bool { _, _, ok := release(l, version); return ok }, fn)
}

// release returns the published, non-prerelease release of version: the tag equal to version,
// or to version with a leading "v" added or removed.
func release(l *githubapi.List, version string) (string, githubapi.Release, bool) {
	if rel, ok := l.Find(version); ok && !rel.Prerelease {
		return version, rel, true
	}
	tag, ok := strings.CutPrefix(version, "v")
	if !ok {
		tag = "v" + version
	}
	if rel, ok := l.Find(tag); ok && !rel.Prerelease {
		return tag, rel, true
	}
	return tag, githubapi.Release{}, false
}

// AssetDigest returns the hex sha256 of asset in the release of version of repository name
// (see Releases). version matches the tag exactly or with a leading "v" added or removed.
func (s *Source) AssetDigest(ctx context.Context, name, version, asset string) (string, error) {
	r, err := githubapi.ParseRepo(name)
	if err != nil {
		return "", err
	}
	var tag string
	var ok bool
	if err := s.with(ctx, name, version, func(l *githubapi.List) { tag, _, ok = release(l, version) }); err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no release %s in %s", version, name)
	}
	digest, err := s.gh.AssetDigest(ctx, r, tag, asset)
	if err != nil {
		return "", err
	}
	if algo, hex, ok := strings.Cut(digest, ":"); ok && algo == "sha256" && hex != "" {
		return hex, nil
	}
	return "", fmt.Errorf("release %s has no digest for asset %s", tag, asset)
}

// AssetByDigest returns the name of the asset of version's release (see AssetDigest for the
// tag match) whose sha256 is hex, or "" when there is none.
func (s *Source) AssetByDigest(ctx context.Context, name, version, hex string) (string, error) {
	r, err := githubapi.ParseRepo(name)
	if err != nil {
		return "", err
	}
	var tag string
	var ok bool
	if err := s.with(ctx, name, version, func(l *githubapi.List) { tag, _, ok = release(l, version) }); err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no release %s in %s", version, name)
	}
	return s.gh.AssetByDigest(ctx, r, tag, "sha256:"+hex)
}

// ReleaseTime implements datasource.Timer with the publication time of version's release (see
// AssetDigest for the tag match) from the shared release list; zero when there is none.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	var published time.Time
	err := s.with(ctx, d.Name, version, func(l *githubapi.List) {
		_, rel, _ := release(l, version)
		published = rel.Published
	})
	return published, err
}

// Deprecation implements datasource.DeprecationSource: the repository is archived, as the
// GraphQL release list (with a token) reports at no extra cost; REST lists do not say. The
// list is the one Releases fetched for d, so no page is fetched.
func (s *Source) Deprecation(d dep.Dep, _ string) string {
	archived := false
	if err := s.with(context.Background(), d.Name, d.Version, func(l *githubapi.List) { archived = l.Archived() }); err != nil || !archived {
		return ""
	}
	return "repository archived"
}

// Digest is unsupported: releases carry no digest.
func (*Source) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }
