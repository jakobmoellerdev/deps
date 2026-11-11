package engine

import (
	"os/exec"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestGroupUpdatesPolicyGroupsAndSecurity(t *testing.T) {
	gha := func(name string) dep.Dep { return dep.Dep{Manager: "github-actions", Name: name} }
	gomod := func(name string) dep.Dep { return dep.Dep{Manager: "gomod", Name: name} }
	adv := []dep.Advisory{{ID: "GHSA-1"}}
	tests := []struct {
		name    string
		prefix  string
		updates []dep.Update
		want    []Group // Updates compared by count only
		counts  []int
	}{
		{
			name: "group across managers",
			updates: []dep.Update{
				{Dep: gha("actions/checkout"), NewVersion: "v5", Group: "Tooling"},
				{Dep: gomod("example.com/a"), NewVersion: "v1.2.0", Group: "Tooling"},
				{Dep: gomod("example.com/b"), NewVersion: "v1.3.0"},
			},
			want: []Group{
				{Name: "example.com/b", Branch: "deps/example.com-b", Title: "chore(deps): update example.com/b to v1.3.0"},
				{Kind: "group", Name: "Tooling", Branch: "deps/group/tooling", Title: "chore(deps): update Tooling (2 dependencies)"},
			},
			counts: []int{1, 2},
		},
		{
			name: "single-dep group keeps dep title",
			updates: []dep.Update{
				{Dep: gha("actions/checkout"), NewVersion: "v5", Group: "ci"},
				{Dep: gha("actions/checkout"), NewVersion: "v5", Group: "ci"},
			},
			want:   []Group{{Kind: "group", Name: "ci", Branch: "deps/group/ci", Title: "chore(deps): update actions/checkout to v5"}},
			counts: []int{2},
		},
		{
			name: "security never grouped",
			updates: []dep.Update{
				{Dep: gomod("example.com/a"), NewVersion: "v1.2.1", Group: "all", Advisories: adv},
				{Dep: gomod("example.com/b"), NewVersion: "v1.3.0", Group: "all"},
			},
			want: []Group{
				{Name: "example.com/a", Branch: "deps/example.com-a", Title: "fix(deps): update example.com/a to v1.2.1 [security]"},
				{Kind: "group", Name: "all", Branch: "deps/group/all", Title: "chore(deps): update example.com/b to v1.3.0"},
			},
			counts: []int{1, 1},
		},
		{
			name: "security with mixed versions",
			updates: []dep.Update{
				{Dep: gomod("example.com/a"), NewVersion: "v1.2.1", Advisories: adv},
				{Dep: gomod("example.com/a"), NewVersion: "v1.4.0"},
			},
			want:   []Group{{Name: "example.com/a", Branch: "deps/example.com-a", Title: "fix(deps): update example.com/a [security]"}},
			counts: []int{2},
		},
		{
			name: "security group apart from the policy group of the same name",
			updates: []dep.Update{
				{Dep: gomod("example.com/a"), NewVersion: "v1.2.1", SecurityGroup: "go", Advisories: adv},
				{Dep: gomod("example.com/b"), NewVersion: "v1.3.1", SecurityGroup: "go", Advisories: []dep.Advisory{{ID: "GHSA-2"}}},
				{Dep: gomod("example.com/c"), NewVersion: "v1.4.0", Group: "go"},
				{Dep: gomod("example.com/d"), NewVersion: "v1.5.0", Group: "go", SecurityGroup: "go"},
				{Dep: gomod("example.com/e"), NewVersion: "v2.0.1", Bucket: "major", SecurityGroup: "go", Advisories: adv},
				{Dep: gha("actions/x"), NewVersion: "v2.0.1", Bucket: "major", SecurityGroup: "go", Advisories: adv},
			},
			want: []Group{
				{Kind: "group", Name: "go", Branch: "deps/group/go", Title: "chore(deps): update go (2 dependencies)"},
				{Kind: "group", Name: "go", Branch: "deps/group/go-major-security", Title: "fix(deps): update go to v2.0.1 [security]"},
				{Kind: "group", Name: "go", Branch: "deps/group/go-security", Title: "fix(deps): update go (2 dependencies) [security]"},
			},
			counts: []int{2, 2, 2},
		},
		{
			name: "single-dep security group",
			updates: []dep.Update{
				{Dep: gomod("example.com/a"), NewVersion: "v1.2.1", SecurityGroup: "Go Security", Advisories: adv},
			},
			want:   []Group{{Kind: "group", Name: "Go Security", Branch: "deps/group/go-security-security", Title: "fix(deps): update example.com/a to v1.2.1 [security]"}},
			counts: []int{1},
		},
		{
			name: "shared version keeps its group",
			updates: []dep.Update{
				{Dep: dep.Dep{Manager: "maven", Name: "core", VersionRef: "jackson.version"}, NewVersion: "2.17.0", Group: "jackson.version", SecurityGroup: "sec", Advisories: adv},
				{Dep: dep.Dep{Manager: "maven", Name: "databind", VersionRef: "jackson.version"}, NewVersion: "2.17.0", Group: "jackson.version"},
			},
			want:   []Group{{Kind: "group", Name: "jackson.version", Branch: "deps/group/jackson.version", Title: "fix(deps): update jackson.version to 2.17.0 [security]"}},
			counts: []int{2},
		},
		{
			name:   "branch prefix",
			prefix: "renovate/",
			updates: []dep.Update{
				{Dep: gomod("example.com/a"), NewVersion: "v1.2.0"},
				{Dep: gomod("example.com/b"), NewVersion: "v1.3.0", Group: "All Go"},
			},
			want: []Group{
				{Name: "example.com/a", Branch: "renovate/example.com-a", Title: "chore(deps): update example.com/a to v1.2.0"},
				{Kind: "group", Name: "All Go", Branch: "renovate/group/all-go", Title: "chore(deps): update example.com/b to v1.3.0"},
			},
			counts: []int{1, 1},
		},
		{
			name: "commit types and scopes",
			updates: []dep.Update{
				{Dep: gomod("example.com/fix"), NewVersion: "v1.2.0", CommitType: "fix", CommitScope: "deps"},
				{Dep: gomod("example.com/ci"), NewVersion: "v2", CommitType: "ci", CommitScope: ""},
				{Dep: gomod("example.com/build"), NewVersion: "v3", Type: dep.Pin, NewDigest: "abcdef0123", CommitType: "build", CommitScope: "tools"},
				{Dep: gomod("example.com/sec"), NewVersion: "v1.0.1", CommitType: "chore", CommitScope: "", Advisories: adv},
			},
			want: []Group{
				{Name: "example.com/build", Branch: "deps/example.com-build", Title: "build(tools): pin example.com/build to abcdef0"},
				{Name: "example.com/ci", Branch: "deps/example.com-ci", Title: "ci: update example.com/ci to v2"},
				{Name: "example.com/fix", Branch: "deps/example.com-fix", Title: "fix(deps): update example.com/fix to v1.2.0"},
				{Name: "example.com/sec", Branch: "deps/example.com-sec", Title: "fix: update example.com/sec to v1.0.1 [security]"},
			},
			counts: []int{1, 1, 1, 1},
		},
		{
			name: "group type is fix if any member is fix, scope from first",
			updates: []dep.Update{
				{Dep: gomod("k8s.io/api"), NewVersion: "v0.35.0", Group: "kubernetes monorepo", CommitType: "chore", CommitScope: "k8s"},
				{Dep: gomod("k8s.io/client-go"), NewVersion: "v0.35.0", Group: "kubernetes monorepo", CommitType: "fix", CommitScope: "deps"},
				{Dep: gha("a/x"), NewVersion: "v2", Group: "ci", CommitType: "ci", CommitScope: "gha"},
				{Dep: gha("a/y"), NewVersion: "v3", Group: "ci", CommitType: "chore", CommitScope: "deps"},
			},
			want: []Group{
				{Kind: "group", Name: "ci", Branch: "deps/group/ci", Title: "ci(gha): update ci (2 dependencies)"},
				{Kind: "group", Name: "kubernetes monorepo", Branch: "deps/group/kubernetes-monorepo", Title: "fix(k8s): update kubernetes monorepo to v0.35.0"},
			},
			counts: []int{2, 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GroupUpdates(tt.updates, tt.prefix)
			if len(got) != len(tt.want) {
				t.Fatalf("groups = %+v", got)
			}
			for i, w := range tt.want {
				g := got[i]
				if g.Kind != w.Kind || g.Name != w.Name || g.Branch != w.Branch || g.Title != w.Title || len(g.Updates) != tt.counts[i] {
					t.Errorf("group %d = %+v, want %+v with %d updates", i, g, w, tt.counts[i])
				}
			}
		})
	}
}

// Each group keeps a branch of its own that git accepts: names rendering alike ("@types/node"
// and "types/node", a bucket suffix against a name ending in it) would otherwise push two PRs
// into one branch, and a name git rejects as a ref would lose its PR altogether.
func TestGroupUpdatesBranchNames(t *testing.T) {
	gomod := func(name string) dep.Dep { return dep.Dep{Manager: "gomod", Name: name} }
	updates := []dep.Update{
		{Dep: gomod("example.com/a"), NewVersion: "v1.1.0"},
		{Dep: gomod("@types/node"), NewVersion: "v22.0.0"},
		{Dep: gomod("types/node"), NewVersion: "v22.0.0"},
		{Dep: gomod("react"), NewVersion: "v19.0.0", Bucket: "major"},
		{Dep: gomod("react-major"), NewVersion: "v2.0.0"},
		{Dep: gomod("foo.lock"), NewVersion: "v1.1.0"},
		{Dep: gomod("a..b"), NewVersion: "v1.1.0"},
		{Dep: gomod(".hidden."), NewVersion: "v1.1.0"},
		{Dep: gomod("日本語"), NewVersion: "v1.1.0"},
		{Dep: gomod("中文"), NewVersion: "v1.1.0"},
	}
	// "" is any branch of the group's own: the second claimant of a branch carries a hash.
	want := map[string]string{
		"example.com/a": "deps/example.com-a",
		"@types/node":   "deps/types-node",
		"types/node":    "",
		"react":         "deps/react-major",
		"react-major":   "",
		"foo.lock":      "deps/foo-lock",
		"a..b":          "deps/a-b",
		".hidden.":      "deps/hidden",
		"日本語":           "",
		"中文":            "",
	}
	groups := GroupUpdates(updates, "")
	if len(groups) != len(updates) {
		t.Fatalf("groups = %+v", groups)
	}
	byBranch := map[string]string{}
	for _, g := range groups {
		w, ok := want[g.Name]
		switch {
		case !ok:
			t.Fatalf("unexpected group %q", g.Name)
		case w != "" && g.Branch != w:
			t.Errorf("group %q is on %q, want %q", g.Name, g.Branch, w)
		}
		if other, ok := byBranch[g.Branch]; ok {
			t.Errorf("groups %q and %q share the branch %q", other, g.Name, g.Branch)
		}
		byBranch[g.Branch] = g.Name
		out, err := exec.CommandContext(t.Context(), "git", "check-ref-format", "refs/heads/"+g.Branch).CombinedOutput()
		if err != nil {
			t.Errorf("git rejects the branch %q of %q: %v %s", g.Branch, g.Name, err, out)
		}
	}
}
