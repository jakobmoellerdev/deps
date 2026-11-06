// Package githubrunners lists the GitHub-hosted runner images (`ubuntu-24.04`, `macos-15`,
// `windows-2025`) from a table kept in the code, as Renovate's github-runners datasource does.
package githubrunners

import (
	"context"
	"fmt"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Name is the datasource name (Renovate's).
const Name = "github-runners"

// SourceURL is the repository the runner images are built from.
const SourceURL = "https://github.com/actions/runner-images"

// image is one runner image version of a runner name: released is the day GitHub announced it
// generally available (https://github.blog/changelog/label/actions/), deprecated is set once
// the runner-images readme tags it `[deprecated]` or drops it.
type image struct {
	version    string
	released   string
	deprecated bool
}

// images are the runner images by runner name (the label before the first "-"), mirroring
// Renovate's table (lib/modules/datasource/github-runners/index.ts). A new image or a
// deprecation is a code change, as in Renovate.
var images = map[string][]image{
	"ubuntu": {
		{"26.04", "2026-09-17", false},
		{"26.04-arm", "2026-09-17", false},
		{"24.04", "2024-09-25", false},
		{"24.04-arm", "2025-08-07", false},
		{"22.04", "2022-08-09", false},
		{"22.04-arm", "2022-08-09", false},
		{"20.04", "2020-10-29", true},
		{"18.04", "2019-11-13", true},
		{"16.04", "2019-08-08", true},
	},
	"macos": {
		{"26", "2026-02-26", false},
		{"26-intel", "2026-02-26", false},
		{"26-xlarge", "2026-02-26", false},
		{"15", "2025-04-10", false},
		{"15-intel", "2025-09-19", false},
		{"15-large", "2025-04-10", false},
		{"15-xlarge", "2025-04-10", false},
		{"14", "2024-04-01", false},
		{"14-large", "2024-04-01", false},
		{"14-xlarge", "2024-04-01", false},
		{"13", "2024-01-30", true},
		{"13-large", "2024-01-30", true},
		{"13-xlarge", "2024-01-30", true},
		{"12-large", "2022-06-13", true},
		{"12", "2022-06-13", true},
		{"11", "2021-08-16", true},
		{"10.15", "2019-11-04", true},
	},
	"windows": {
		{"2025", "2025-04-10", false},
		{"2022", "2021-11-15", false},
		{"11-arm", "2025-08-07", false},
		{"2019", "2019-11-13", true},
		{"2016", "2019-08-08", true},
	},
	"xcode": {
		{"27", "2026-10-15", false},
		{"27-xlarge", "2026-10-15", false},
	},
}

// releases are the images that are not deprecated, by runner name: updates never move to a
// deprecated image, and every deprecated one is older than a supported image of its line.
var releases = func() map[string][]datasource.Release {
	m := make(map[string][]datasource.Release, len(images))
	for name, imgs := range images {
		rels := make([]datasource.Release, 0, len(imgs))
		for _, img := range imgs {
			if !img.deprecated {
				rels = append(rels, datasource.Release{Version: img.version})
			}
		}
		m[name] = rels
	}
	return m
}()

// lookup returns the image version of runner name, false if the table has none.
func lookup(name, version string) (image, bool) {
	for _, img := range images[name] {
		if img.version == version {
			return img, true
		}
	}
	return image{}, false
}

// Known reports whether `<name>-<version>` is a GitHub-hosted runner label: a version in
// the table, or `latest`.
func Known(name, version string) bool {
	if _, ok := images[name]; !ok {
		return false
	}
	if version == "latest" {
		return true
	}
	_, ok := lookup(name, version)
	return ok
}

// Source is the github-runners datasource; it makes no request.
type Source struct{}

// New returns the github-runners datasource.
func New() Source { return Source{} }

// Releases returns the images of runner d.Name that are not deprecated.
func (Source) Releases(_ context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	rels, ok := releases[d.Name]
	if !ok {
		return nil, fmt.Errorf("%s: unknown runner %q", Name, d.Name)
	}
	return rels, nil
}

// ReleaseTime implements datasource.Timer with the day GitHub announced the image generally
// available; zero when the table has no such image.
func (Source) ReleaseTime(_ context.Context, d dep.Dep, version string) (time.Time, error) {
	img, ok := lookup(d.Name, version)
	if !ok {
		return time.Time{}, nil
	}
	return time.Parse(time.DateOnly, img.released)
}

// Deprecation implements datasource.DeprecationSource for a deprecated image.
func (Source) Deprecation(d dep.Dep, version string) string {
	if img, ok := lookup(d.Name, version); ok && img.deprecated {
		return "runner image " + d.Name + "-" + version + " deprecated"
	}
	return ""
}

// Digest is unsupported: runner labels have no digests.
func (Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}
