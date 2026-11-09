// Package clojure is the clojure datasource: Maven artifacts of Clojure projects, looked up in
// Clojars and Maven Central through the maven datasource.
package clojure

import (
	"context"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's).
const Name = "clojure"

// Clojars is the Clojars repository; DEPS_CLOJARS_URL overrides it (a mirror).
const Clojars = "https://repo.clojars.org/"

// Source is the clojure datasource: the maven datasource with Clojars and Maven Central as the
// repositories of a dep without its own.
type Source struct {
	maven   *mavends.Source
	clojars string // Clojars or its DEPS_CLOJARS_URL override
}

// New returns the clojure datasource reading through maven (its caches, Maven Central
// override and release times apply); getenv supplies DEPS_CLOJARS_URL.
func New(maven *mavends.Source, getenv func(string) string) *Source {
	clojars := Clojars
	if v := getenv("DEPS_CLOJARS_URL"); v != "" {
		clojars = strings.TrimSuffix(v, "/") + "/"
	}
	return &Source{maven: maven, clojars: clojars}
}

// withRepos returns d with Clojars and Maven Central as its repositories when its Source lists
// none; Clojars is replaced by its override.
func (s *Source) withRepos(d dep.Dep) dep.Dep {
	if strings.TrimSpace(d.Source) == "" {
		d.Source = s.clojars + " " + mavends.Central
	} else if s.clojars != Clojars {
		fields := strings.Fields(d.Source)
		for i, f := range fields {
			if f == Clojars {
				fields[i] = s.clojars
			}
		}
		d.Source = strings.Join(fields, " ")
	}
	return d
}

// Releases merges the versions of every repository of d (Clojars and Maven Central by default).
func (s *Source) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	return s.maven.Releases(ctx, s.withRepos(d), dir)
}

// Digest is unsupported.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// ReleaseTime implements datasource.Timer as the maven datasource does (POM Last-Modified).
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	return s.maven.ReleaseTime(ctx, s.withRepos(d), version)
}

// SourceRepo implements datasource.SourceRepo from the version's POM, as the maven datasource.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	return s.maven.SourceRepo(ctx, s.withRepos(d), version)
}
