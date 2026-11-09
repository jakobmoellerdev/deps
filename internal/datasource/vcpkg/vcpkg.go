// Package vcpkg lists the versions of vcpkg ports from the versions database of a vcpkg
// registry (`versions/<first letter>-/<port>.json`) at one registry commit, the baseline the
// manifest pins. Lookups run once per (registry commit, port) and are shared by every dep of a
// run.
package vcpkg

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name.
const Name = "vcpkg"

// Source is the vcpkg datasource.
type Source struct {
	getter httpx.Getter
	ports  datasource.Memo[string, []datasource.Release] // by version file URL
	repos  datasource.Memo[string, string]               // by port manifest URL
}

// New returns the vcpkg datasource. sem bounds concurrent requests (nil = unbounded).
func New(client *http.Client, sem chan struct{}) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}}
}

// RawBase returns the base URL serving the files of the git repository at https URL repo at
// commit (dep.Dep.Source of a port): raw.githubusercontent.com for github.com, `<repo>/raw/<commit>`
// (Gitea, Forgejo, GitLab) otherwise.
func RawBase(repo, commit string) string {
	repo = strings.TrimSuffix(strings.TrimSuffix(repo, "/"), ".git")
	if p, ok := strings.CutPrefix(repo, "https://github.com/"); ok {
		return "https://raw.githubusercontent.com/" + p + "/" + commit
	}
	return repo + "/raw/" + commit
}

// FileURL returns the URL of the versions file of port in the registry tree at base.
func FileURL(base, port string) string {
	if port == "" {
		return ""
	}
	return strings.TrimSuffix(base, "/") + "/versions/" + port[:1] + "-/" + port + ".json"
}

// versionsFile is a port's versions file; each entry has exactly one of the version fields.
type versionsFile struct {
	Versions []struct {
		Version     string `json:"version"`
		Semver      string `json:"version-semver"`
		Date        string `json:"version-date"`
		String      string `json:"version-string"`
		PortVersion int    `json:"port-version"`
	} `json:"versions"`
}

// Releases lists the versions of port d.Name in the versions database at d.Source (see
// RawBase), as `<version>[#<port-version>]` (the `version>=` form; port-version 0 is
// omitted). vcpkg orders versions of one scheme only, so only those of the newest entry's
// scheme are listed: relaxed `version` and `version-semver`, `version-date`, or
// `version-string` (a port that moved from dates to versions drops its dates). The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	target := FileURL(d.Source, d.Name)
	if target == "" || d.Source == "" {
		return nil, fmt.Errorf("vcpkg: port %q without a registry", d.Name)
	}
	return s.ports.Do(ctx, target, func() ([]datasource.Release, error) { return s.fetch(ctx, target, d.Name) })
}

// Digest is unsupported: port versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// SourceRepo implements datasource.SourceRepo with the `homepage` of the port's manifest
// (`ports/<port>/vcpkg.json`) in the registry tree at d.Source when it is a repository on
// github.com, gitlab.com or codeberg.org; "" otherwise (most homepages of other hosts are
// project sites) and when the registry has no manifest for the port. Once per manifest URL.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	if d.Source == "" || d.Name == "" {
		return "", nil
	}
	target := strings.TrimSuffix(d.Source, "/") + "/ports/" + d.Name + "/vcpkg.json"
	return s.repos.Do(ctx, target, func() (string, error) {
		var m struct {
			Homepage string `json:"homepage"`
		}
		err := s.getter.Stream(ctx, target, func(r io.Reader) error { return json.UnmarshalRead(r, &m) })
		if errors.Is(err, httpx.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if onForge(m.Homepage) {
			return m.Homepage, nil
		}
		return "", nil
	})
}

// onForge reports whether raw is an http(s) URL on github.com, gitlab.com or codeberg.org.
func onForge(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "gitlab.com", "codeberg.org":
		return true
	}
	return false
}

func (s *Source) fetch(ctx context.Context, target, port string) ([]datasource.Release, error) {
	var f versionsFile
	err := s.getter.Stream(ctx, target, func(r io.Reader) error { return json.UnmarshalRead(r, &f) })
	if errors.Is(err, httpx.ErrNotFound) {
		return nil, fmt.Errorf("vcpkg port %s not found in the registry (%s)", port, target)
	}
	if err != nil {
		return nil, err
	}
	rels := make([]datasource.Release, 0, len(f.Versions))
	seen := make(map[string]bool, len(f.Versions))
	scheme := -1 // of the newest entry
	for _, v := range f.Versions {
		var s string
		var c int
		switch {
		case v.Version != "":
			s = v.Version
		case v.Semver != "":
			s = v.Semver
		case v.Date != "":
			s, c = v.Date, 1
		case v.String != "":
			s, c = v.String, 2
		default:
			continue
		}
		if scheme < 0 {
			scheme = c
		}
		if c != scheme {
			continue
		}
		if v.PortVersion > 0 {
			s += "#" + strconv.Itoa(v.PortVersion)
		}
		if !seen[s] {
			seen[s] = true
			rels = append(rels, datasource.Release{Version: s})
		}
	}
	return rels, nil
}
