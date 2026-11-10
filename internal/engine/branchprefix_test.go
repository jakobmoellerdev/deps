package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// vReleases lists versions in the form of the dep's current version: with a leading "v" for
// v-prefixed deps (an action's tags), without for the others (a formula's versions).
type vReleases []string

func (r vReleases) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	out := make([]Release, len(r))
	for i, v := range r {
		if !strings.HasPrefix(d.Version, "v") {
			v = strings.TrimPrefix(v, "v")
		}
		out[i] = Release{Version: v}
	}
	return out, nil
}

func (vReleases) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

// TestBranchTopics resolves one dependency found by github-actions, homebrew and a custom
// manager (each in its own region of one file) and groups its updates: one PR across
// managers, as in Renovate, unless additionalBranchPrefix tells them apart.
func TestBranchTopics(t *testing.T) {
	const src = "v0.6.0 0.6.0 v0.6.0\n"
	syft := func(ver string, start int) []dep.Dep {
		return []dep.Dep{{Datasource: "github-releases", Versioning: "semver", Name: "anchore/syft", Version: ver, Line: 1, RefSpan: dep.Span{Start: start, End: start + len(ver)}}}
	}
	managers := []manager.Manager{
		stubManager{name: "github-actions", deps: syft("v0.6.0", 0)},
		stubManager{name: "homebrew", deps: syft("0.6.0", 7)},
		stubManager{name: "custom", deps: syft("v0.6.0", 13)},
	}
	minor := vReleases{"v0.6.0", "v0.7.0"}
	withMajor := vReleases{"v0.6.0", "v0.7.0", "v1.0.0"}
	tests := []struct {
		name     string
		cfg      string
		releases vReleases
		want     []string // "<branch> <title> (<updates>)", sorted
	}{
		{"default: one PR across managers", "", minor, []string{
			"deps/anchore-syft chore(deps): update anchore/syft to v0.7.0 (3)",
		}},
		{"buckets stay separate", "", withMajor, []string{
			"deps/anchore-syft chore(deps): update anchore/syft to v0.7.0 (3)",
			"deps/anchore-syft-major chore(deps): update anchore/syft to v1.0.0 (3)",
		}},
		{"{{manager}}/ keeps managers apart", "additionalBranchPrefix: '{{manager}}/'\n", minor, []string{
			"deps/custom/anchore-syft chore(deps): update anchore/syft to v0.7.0 (1)",
			"deps/github-actions/anchore-syft chore(deps): update anchore/syft to v0.7.0 (1)",
			"deps/homebrew/anchore-syft chore(deps): update anchore/syft to 0.7.0 (1)",
		}},
		{"{{{manager}}}-", "additionalBranchPrefix: '{{{manager}}}-'\n", minor, []string{
			"deps/custom-anchore-syft chore(deps): update anchore/syft to v0.7.0 (1)",
			"deps/github-actions-anchore-syft chore(deps): update anchore/syft to v0.7.0 (1)",
			"deps/homebrew-anchore-syft chore(deps): update anchore/syft to 0.7.0 (1)",
		}},
		{"literal prefix", "additionalBranchPrefix: tools-\n", withMajor, []string{
			"deps/tools-anchore-syft chore(deps): update anchore/syft to v0.7.0 (3)",
			"deps/tools-anchore-syft-major chore(deps): update anchore/syft to v1.0.0 (3)",
		}},
		{"a rule sets one manager's prefix", "rules:\n  - managers: [homebrew]\n    additionalBranchPrefix: brew/\n", minor, []string{
			"deps/anchore-syft chore(deps): update anchore/syft to v0.7.0 (2)",
			"deps/brew/anchore-syft chore(deps): update anchore/syft to 0.7.0 (1)",
		}},
		{"policy group", "rules:\n  - group: tools\n", minor, []string{
			"deps/group/tools chore(deps): update anchore/syft to v0.7.0 (3)",
		}},
		{"policy group with a literal prefix", "additionalBranchPrefix: ci-\nrules:\n  - group: tools\n", minor, []string{
			"deps/ci-group/tools chore(deps): update anchore/syft to v0.7.0 (3)",
		}},
		{"policy group split by {{manager}}/", "additionalBranchPrefix: '{{manager}}/'\nrules:\n  - group: tools\n", minor, []string{
			"deps/custom/group/tools chore(deps): update anchore/syft to v0.7.0 (1)",
			"deps/github-actions/group/tools chore(deps): update anchore/syft to v0.7.0 (1)",
			"deps/homebrew/group/tools chore(deps): update anchore/syft to 0.7.0 (1)",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"stub.txt": src})
			res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: managers, Datasources: map[string]Datasource{"github-releases": tt.releases}})
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("err = %v, errors = %v", err, res.Errors)
			}
			var got []string
			for _, g := range GroupUpdates(res.Updates, "") {
				got = append(got, fmt.Sprintf("%s %s (%d)", g.Branch, g.Title, len(g.Updates)))
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("groups:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// TestGroupUpdatesAcrossManagers covers the group key, branch and title of one dependency's
// updates from several managers.
func TestGroupUpdatesAcrossManagers(t *testing.T) {
	up := func(mgr, name, version, bucket, topicPrefix string) dep.Update {
		return dep.Update{Dep: dep.Dep{Manager: mgr, Name: name}, NewVersion: version, Bucket: bucket, BranchTopicPrefix: topicPrefix}
	}
	sec := up("gomod", "x/y", "v1.0.1", "", "")
	sec.Advisories = []dep.Advisory{{ID: "GHSA-1"}}
	tests := []struct {
		name    string
		updates []dep.Update
		want    []string // "<branch>|<title>|<updates>"
	}{
		{"versions differing by a leading v share a title, in the first member's form", []dep.Update{
			up("homebrew", "anchore/syft", "0.7.0", "", ""),
			up("github-actions", "anchore/syft", "v0.7.0", "", ""),
		}, []string{"deps/anchore-syft|chore(deps): update anchore/syft to 0.7.0|2"}},
		{"different versions", []dep.Update{
			up("github-actions", "anchore/syft", "v0.7.0", "", ""),
			up("homebrew", "anchore/syft", "0.7.1", "", ""),
		}, []string{"deps/anchore-syft|chore(deps): update anchore/syft|2"}},
		{"buckets, names and topic prefixes stay apart", []dep.Update{
			up("github-actions", "anchore/syft", "v0.7.0", "", ""),
			up("homebrew", "anchore/syft", "1.0.0", "major", ""),
			up("homebrew", "anchore/grype", "0.9.0", "", ""),
			up("homebrew", "anchore/syft", "0.7.0", "", "homebrew/"),
		}, []string{
			"deps/anchore-grype|chore(deps): update anchore/grype to 0.9.0|1",
			"deps/anchore-syft|chore(deps): update anchore/syft to v0.7.0|1",
			"deps/anchore-syft-major|chore(deps): update anchore/syft to 1.0.0|1",
			"deps/homebrew/anchore-syft|chore(deps): update anchore/syft to 0.7.0|1",
		}},
		{"security updates join the dep's PR", []dep.Update{
			sec,
			up("cargo", "x/y", "v1.0.1", "", ""),
		}, []string{"deps/x-y|fix(deps): update x/y to v1.0.1 [security]|2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, g := range GroupUpdates(tt.updates, "") {
				got = append(got, fmt.Sprintf("%s|%s|%d", g.Branch, g.Title, len(g.Updates)))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("groups:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}
