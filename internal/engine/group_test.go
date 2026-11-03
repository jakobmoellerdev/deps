package engine

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestGroupUpdatesPolicyGroupsAndSecurity(t *testing.T) {
	gha := func(name string) dep.Dep { return dep.Dep{Manager: "github-actions", Name: name} }
	gomod := func(name string) dep.Dep { return dep.Dep{Manager: "gomod", Name: name} }
	adv := []dep.Advisory{{ID: "GHSA-1"}}
	tests := []struct {
		name    string
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
				{Manager: "gomod", Name: "example.com/b", Branch: "deps/gomod/example.com-b", Title: "chore(deps): update example.com/b to v1.3.0"},
				{Manager: "group", Name: "Tooling", Branch: "deps/group/tooling", Title: "chore(deps): update Tooling (2 dependencies)"},
			},
			counts: []int{1, 2},
		},
		{
			name: "single-dep group keeps dep title",
			updates: []dep.Update{
				{Dep: gha("actions/checkout"), NewVersion: "v5", Group: "ci"},
				{Dep: gha("actions/checkout"), NewVersion: "v5", Group: "ci"},
			},
			want:   []Group{{Manager: "group", Name: "ci", Branch: "deps/group/ci", Title: "chore(deps): update actions/checkout to v5"}},
			counts: []int{2},
		},
		{
			name: "security never grouped",
			updates: []dep.Update{
				{Dep: gomod("example.com/a"), NewVersion: "v1.2.1", Group: "all", Advisories: adv},
				{Dep: gomod("example.com/b"), NewVersion: "v1.3.0", Group: "all"},
			},
			want: []Group{
				{Manager: "gomod", Name: "example.com/a", Branch: "deps/gomod/example.com-a", Title: "fix(deps): update example.com/a to v1.2.1 [security]"},
				{Manager: "group", Name: "all", Branch: "deps/group/all", Title: "chore(deps): update example.com/b to v1.3.0"},
			},
			counts: []int{1, 1},
		},
		{
			name: "security with mixed versions",
			updates: []dep.Update{
				{Dep: gomod("example.com/a"), NewVersion: "v1.2.1", Advisories: adv},
				{Dep: gomod("example.com/a"), NewVersion: "v1.4.0"},
			},
			want:   []Group{{Manager: "gomod", Name: "example.com/a", Branch: "deps/gomod/example.com-a", Title: "fix(deps): update example.com/a [security]"}},
			counts: []int{2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GroupUpdates(tt.updates)
			if len(got) != len(tt.want) {
				t.Fatalf("groups = %+v", got)
			}
			for i, w := range tt.want {
				g := got[i]
				if g.Manager != w.Manager || g.Name != w.Name || g.Branch != w.Branch || g.Title != w.Title || len(g.Updates) != tt.counts[i] {
					t.Errorf("group %d = %+v, want %+v with %d updates", i, g, w, tt.counts[i])
				}
			}
		})
	}
}
