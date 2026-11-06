package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pub"
)

const pubspec = `name: app
environment:
  sdk: ^3.5.0
dependencies:
  http: ^1.2.0
  path: 1.9.0
  web: '>=0.5.0 <2.0.0'
  args: <3.0.0
dev_dependencies:
  test: ^1.25.0
`

const pubspecLock = `packages:
  args:
    dependency: "direct main"
    source: hosted
    version: "2.5.0"
  http:
    dependency: "direct main"
    source: hosted
    version: "1.2.2"
  path:
    dependency: "direct main"
    source: hosted
    version: "1.9.0"
  test:
    dependency: "direct dev"
    source: hosted
    version: "1.25.8"
  web:
    dependency: "direct main"
    source: hosted
    version: "1.1.0"
sdks:
  dart: ">=3.5.0 <4.0.0"
`

var pubReleases = fakeGo{
	"http": {"1.2.0", "1.2.2", "1.3.0", "2.0.0"},
	"path": {"1.9.0", "1.9.1"},
	"web":  {"1.1.0", "1.1.1", "2.0.0"},
	"args": {"2.5.0", "2.6.0", "3.0.0"},
	"test": {"1.25.8", "1.26.0"},
}

var dartReleases = fakeGo{"dart": {"3.5.0", "3.6.0", "4.0.0-1.0.dev"}}

// TestPubRangeStrategy resolves a pubspec.yaml with its pubspec.lock under rangeStrategy auto
// (replace, as in Renovate), update-lockfile, bump and widen, and builds the pubspec changes.
func TestPubRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		want      []string
		lines     []string // the changed lines
	}{
		{
			name:  "auto",
			want:  []string{"args 2.5.0->3.0.0 major", "http 1.2.0->2.0.0 major", "path 1.9.0->1.9.1 patch", "web 0.5.0->2.0.0 major"},
			lines: []string{"http: ^2.0.0", "path: 1.9.1", "web: '>=2.0.0 <3.0.0'", "args: <4.0.0"},
		},
		{
			name: "update-lockfile", cfg: "rangeStrategy: update-lockfile\n",
			want: []string{
				"args 2.5.0->2.6.0 minor lockfile", "args 2.5.0->3.0.0 major",
				"http 1.2.0->2.0.0 major", "http 1.2.2->1.3.0 minor lockfile", "path 1.9.0->1.9.1 patch",
				"test 1.25.8->1.26.0 minor lockfile", "web 0.5.0->2.0.0 major", "web 1.1.0->1.1.1 patch lockfile",
			},
			lines: []string{"http: ^2.0.0", "path: 1.9.1", "web: '>=2.0.0 <3.0.0'", "args: <4.0.0"},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n",
			want: []string{
				"args 2.5.0->2.6.0 minor lockfile", "args 2.5.0->3.0.0 major", "dart 3.5.0->3.6.0 minor",
				"http 1.2.0->1.3.0 minor", "http 1.2.0->2.0.0 major", "path 1.9.0->1.9.1 patch",
				"test 1.25.0->1.26.0 minor", "web 0.5.0->2.0.0 major", // 1.1.1 shares the major PR bucket
			},
			lines: []string{"sdk: ^3.6.0", "http: ^1.3.0", "path: 1.9.1", "web: '>=2.0.0 <3.0.0'", "args: <4.0.0", "test: ^1.26.0"},
		},
		{
			name: "widen", cfg: "rangeStrategy: widen\n",
			want:  []string{"args 2.5.0->3.0.0 major", "http 1.2.0->2.0.0 major", "path 1.9.0->1.9.1 patch", "web 0.5.0->2.0.0 major"},
			lines: []string{"http: '>=1.2.0 <3.0.0'", "path: 1.9.1", "web: '>=0.5.0 <3.0.0'", "args: <4.0.0"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"app/pubspec.yaml": pubspec, "app/pubspec.lock": pubspecLock})
			o := Options{Managers: []manager.Manager{pub.New()}, Datasources: map[string]Datasource{"dart": pubReleases, "dart-version": dartReleases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			var updates []dep.Update
			for _, u := range res.Updates {
				if !u.Lockfile {
					updates = append(updates, u)
				}
			}
			local, _ := Local(updates)
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			orig := map[string]bool{}
			for l := range strings.SplitSeq(pubspec, "\n") {
				orig[l] = true
			}
			var lines []string
			for _, c := range changes {
				for l := range strings.SplitSeq(string(c.New), "\n") {
					if !orig[l] {
						lines = append(lines, strings.TrimSpace(l))
					}
				}
			}
			if strings.Join(lines, "\n") != strings.Join(tt.lines, "\n") {
				t.Errorf("pubspec.yaml changes\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(tt.lines, "\n"))
			}
		})
	}
}
