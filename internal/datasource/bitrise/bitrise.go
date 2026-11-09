// Package bitrise lists the versions of Bitrise steps from a step library: a GitHub repository
// (bitrise-io/bitrise-steplib by default) with a directory steps/<id>/<version>/ per version.
package bitrise

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
)

// Name is the datasource name (Renovate's).
const Name = "bitrise"

// DefaultSteplib is Bitrise's public step library, the steplib of a dep without Source.
const DefaultSteplib = "https://github.com/bitrise-io/bitrise-steplib.git"

// versionRe is a step version directory: MAJOR.MINOR.PATCH with an optional prerelease;
// submatches 1-3 are the parts, 4 the prerelease.
var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

// Source looks up each step once per run, and each step.yml once when its release time or
// source repository is asked for.
type Source struct {
	gh    *githubapi.Client
	steps datasource.Memo[stepKey, *step]
	files datasource.Memo[fileKey, *stepFile]
}

type stepKey struct{ lib, id string }

type fileKey struct {
	stepKey
	version string
}

// step is the published versions of a step: as listed, then a derived "X.Y" and "X" per
// release line, so partial versions (`script@1`, Bitrise's "latest 1.x") have candidates of
// their precision under tag versioning. first maps a derived line to its first release, whose
// step.yml gives the line's release time.
type step struct {
	repo  githubapi.Repo
	rels  []datasource.Release
	first map[string]string
}

// stepFile is what deps reads from steps/<id>/<version>/step.yml.
type stepFile struct {
	published time.Time
	source    string
}

// New returns the bitrise datasource reading step libraries through gh (shared GitHub
// client: its token, request slots and GitHub Enterprise Server endpoints).
func New(gh *githubapi.Client) *Source { return &Source{gh: gh} }

// Releases lists the versions of step d.Name in the step library d.Source (DefaultSteplib
// when empty).
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	st, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return st.rels, nil
}

// ReleaseTime implements datasource.Timer with `published_at` of the version's step.yml; a
// derived "X.Y"/"X" has the time of the line's first release. Zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	f, err := s.file(ctx, d, version)
	if err != nil {
		return time.Time{}, err
	}
	return f.published, nil
}

// SourceRepo implements datasource.SourceRepo with `source_code_url` of the version's
// step.yml.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	f, err := s.file(ctx, d, version)
	if err != nil {
		return "", err
	}
	return f.source, nil
}

// Digest is unsupported: steps are referenced by version.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

func (s *Source) lookup(ctx context.Context, d dep.Dep) (*step, error) {
	key := stepKey{cmp.Or(d.Source, DefaultSteplib), d.Name}
	return s.steps.Do(ctx, key, func() (*step, error) {
		repo, err := githubapi.ParseRepo(key.lib)
		if err != nil {
			return nil, fmt.Errorf("step library %s: only GitHub repositories are supported: %w", key.lib, err)
		}
		if key.id == "" || strings.ContainsAny(key.id, "/\\") || key.id == "." || key.id == ".." {
			return nil, fmt.Errorf("invalid step id %q", key.id)
		}
		entries, err := s.gh.Dir(ctx, repo, "steps/"+key.id, "")
		if err != nil {
			return nil, fmt.Errorf("step %s in %s: %w", key.id, key.lib, err)
		}
		st := &step{repo: repo, first: map[string]string{}}
		var derived []datasource.Release
		lowest := map[string][3]int{}
		for _, e := range entries {
			m := versionRe.FindStringSubmatch(e.Name)
			if e.Type != "dir" || m == nil {
				continue
			}
			st.rels = append(st.rels, datasource.Release{Version: e.Name})
			if m[4] != "" {
				continue
			}
			v := [3]int{atoi(m[1]), atoi(m[2]), atoi(m[3])}
			for _, line := range []string{m[1] + "." + m[2], m[1]} {
				low, seen := lowest[line]
				if !seen {
					derived = append(derived, datasource.Release{Version: line})
				}
				if !seen || less(v, low) {
					lowest[line], st.first[line] = v, e.Name
				}
			}
		}
		if len(st.rels) == 0 {
			return nil, fmt.Errorf("step %s in %s has no versions", key.id, key.lib)
		}
		st.rels = append(st.rels, derived...)
		return st, nil
	})
}

// file reads the step.yml of version (of the first release of a derived line).
func (s *Source) file(ctx context.Context, d dep.Dep, version string) (*stepFile, error) {
	st, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	version = cmp.Or(st.first[version], version)
	key := fileKey{stepKey{cmp.Or(d.Source, DefaultSteplib), d.Name}, version}
	return s.files.Do(ctx, key, func() (*stepFile, error) {
		b, err := s.gh.File(ctx, st.repo, "steps/"+key.id+"/"+version+"/step.yml", "")
		if err != nil {
			return nil, fmt.Errorf("step %s %s in %s: %w", key.id, version, key.lib, err)
		}
		var raw struct {
			PublishedAt   string `yaml:"published_at"`
			SourceCodeURL string `yaml:"source_code_url"`
		}
		if err := yaml.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("step %s %s in %s: malformed step.yml: %w", key.id, version, key.lib, err)
		}
		f := &stepFile{source: raw.SourceCodeURL}
		if t, err := time.Parse(time.RFC3339Nano, raw.PublishedAt); err == nil {
			f.published = t
		}
		return f, nil
	})
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
