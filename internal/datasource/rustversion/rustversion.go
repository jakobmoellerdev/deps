// Package rustversion lists Rust releases from the manifest index of static.rust-lang.org, the
// distribution server rustup installs toolchains from.
package rustversion

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "rust-version"

// DefaultURL lists the URL of every channel manifest rustup can install, oldest first
// (`static.rust-lang.org/dist/2024-10-17/channel-rust-1.82.0.toml`); DEPS_RUST_MANIFESTS_URL
// overrides it.
const DefaultURL = "https://static.rust-lang.org/manifests.txt"

// Repo is the source repository of the Rust toolchain, whose GitHub releases carry the
// release notes.
const Repo = "https://github.com/rust-lang/rust"

// manifestRe matches a release manifest URL: group 1 is its date, group 2 a stable `X.Y.Z`, a
// numbered beta `X.Y.Z-beta.N` or `nightly`, the nightly of that date. The stable and beta
// channel manifests (`channel-rust-stable.toml`), release-line aliases (`1.82`, `1.83-beta`)
// and unnumbered betas name no fixed release.
var manifestRe = regexp.MustCompile(`/dist/(\d{4}-\d{2}-\d{2})/channel-rust-(\d+\.\d+\.\d+(?:-beta\.\d+)?|nightly)\.toml$`)

// NightlyVersioning is the versioning of a dated nightly (`nightly-2025-10-10`): a regex scheme
// that orders the dates and makes every newer nightly a patch update, as Renovate's
// rust-release-channel versioning does (a nightly has no major or minor), so a new year does
// not split the update into a major PR. The empty major group never takes part (0). Only
// dated nightlies are valid under it.
const NightlyVersioning = `regex:^nightly-(?<patch>\d{4})-(?<build>\d{2})-(?<revision>\d{2})(?<major>)??$`

// Source fetches the manifest index once per run.
type Source struct {
	getter httpx.Getter
	url    string

	index datasource.Once[index]
}

// index is the parsed manifest index.
type index struct {
	releases []datasource.Release
	times    map[string]time.Time
}

// New returns the rust-version datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_RUST_MANIFESTS_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	return &Source{getter: httpx.Getter{Client: client, Sem: sem}, url: cmp.Or(getenv("DEPS_RUST_MANIFESTS_URL"), DefaultURL)}
}

// Releases returns every stable and numbered beta release (`1.82.0`, `1.83.0-beta.5`; schemes
// drop betas for stable current versions) and every dated nightly (`nightly-2025-10-10`, valid
// only under NightlyVersioning) in index order, then a derived "X.Y" per stable release line,
// so a toolchain pinned to a line (`channel = "1.82"`) has candidates of its precision under
// tag versioning.
func (s *Source) Releases(ctx context.Context, _ dep.Dep, _ string) ([]datasource.Release, error) {
	ix, err := s.load(ctx)
	return ix.releases, err
}

// ReleaseTime implements datasource.Timer with the date of the first manifest of version (a
// manifest re-published later keeps its first date); a derived "X.Y" has the date of X.Y.0.
// Zero when unknown.
func (s *Source) ReleaseTime(ctx context.Context, _ dep.Dep, version string) (time.Time, error) {
	ix, err := s.load(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return ix.times[version], nil
}

// SourceRepo implements datasource.SourceRepo: every release is built from rust-lang/rust.
func (*Source) SourceRepo(context.Context, dep.Dep, string) (string, error) { return Repo, nil }

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
		slog.DebugContext(ctx, "rust releases", "url", s.url, "releases", len(ix.releases), "duration", time.Since(start), "err", err)
		return ix, err
	})
}

// read parses the manifest index, one URL per line.
func (ix *index) read(r io.Reader) error {
	ix.times = map[string]time.Time{}
	var derived []datasource.Release
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		m := manifestRe.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		version := m[2]
		if version == "nightly" {
			version += "-" + m[1]
		}
		if _, seen := ix.times[version]; seen {
			continue // re-published manifest (1.8.0, 1.14.0): the first date is the release
		}
		t, _ := time.Parse(time.DateOnly, m[1])
		ix.releases = append(ix.releases, datasource.Release{Version: version})
		ix.times[version] = t
		if !strings.Contains(version, "-") {
			line := version[:strings.LastIndexByte(version, '.')]
			if _, seen := ix.times[line]; !seen {
				derived = append(derived, datasource.Release{Version: line})
				ix.times[line] = t
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(ix.releases) == 0 {
		return errors.New("no releases")
	}
	ix.releases = append(ix.releases, derived...)
	return nil
}
