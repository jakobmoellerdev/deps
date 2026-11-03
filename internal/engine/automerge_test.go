package engine

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
)

// TestAutomerge mirrors OCM's `matchUpdateTypes: [minor, patch], automerge: true`: updates carry
// the automerge setting of their type's policy, and a group automerges only when all of its
// updates do.
func TestAutomerge(t *testing.T) {
	files := map[string]string{
		".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n" +
			"      - uses: actions/checkout@v4.1.0\n" +
			"      - uses: actions/setup-go@v5.1.0\n" +
			"      - uses: actions/cache@v4.0.0\n" +
			"      - uses: ./local\n",
	}
	root, cfg := setup(t, `automergeStrategy: squash
separateMajorMinor: false
rules:
  - updateTypes: [minor, patch]
    automerge: true
  - names: ["actions/cache", "actions/setup-go"]
    group: tools
`, files)
	o := Options{
		Managers: []manager.Manager{actions.New()},
		Datasources: map[string]Datasource{"git-tags": fakeTags{
			checkout: {"v4.1.0", "v4.2.0"},
			setupGo:  {"v5.1.0", "v6.0.0"},
			cache:    {"v4.0.0", "v4.0.1"},
		}},
	}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	want := map[string]bool{"actions/checkout": true, "actions/setup-go": false, "actions/cache": true}
	for _, u := range res.Updates {
		if u.Automerge != want[u.Dep.Name] || u.AutomergeStrategy != config.StrategySquash {
			t.Errorf("%s %s: automerge %v %q, want %v squash", u.Dep.Name, u.Type, u.Automerge, u.AutomergeStrategy, want[u.Dep.Name])
		}
	}
	if len(res.Updates) != 3 {
		t.Fatalf("updates = %+v", res.Updates)
	}
	got := map[string]bool{}
	for _, g := range GroupUpdates(res.Updates, "") {
		got[g.Branch] = g.Automerge
		if g.Automerge && g.AutomergeStrategy != config.StrategySquash {
			t.Errorf("%s strategy %q", g.Branch, g.AutomergeStrategy)
		}
	}
	// tools mixes a major (setup-go) and a patch (cache): not automerged.
	if want := map[string]bool{"deps/github-actions/actions-checkout": true, "deps/group/tools": false}; len(got) != len(want) || got["deps/github-actions/actions-checkout"] != true || got["deps/group/tools"] != false {
		t.Errorf("groups automerge = %v, want %v", got, want)
	}

	// Deps lists every extracted dep, the skipped local action included.
	if len(res.Deps) != 4 || len(res.Skipped) != 1 {
		t.Fatalf("deps = %+v, skipped = %+v", res.Deps, res.Skipped)
	}
	var names []string
	for _, d := range res.Deps {
		names = append(names, d.Name+" "+d.Version)
	}
	if names[0] != "actions/checkout v4.1.0" || res.Deps[3].Skip == "" {
		t.Errorf("deps = %q", names)
	}
}

func TestGroupAutomergeStrategyFromFirst(t *testing.T) {
	ups := []dep.Update{
		{Dep: dep.Dep{Manager: "gomod", Name: "a"}, NewVersion: "v2", Group: "g", Automerge: true, AutomergeStrategy: "rebase"},
		{Dep: dep.Dep{Manager: "gomod", Name: "b"}, NewVersion: "v2", Group: "g", Automerge: true, AutomergeStrategy: "merge"},
	}
	g := GroupUpdates(ups, "")
	if len(g) != 1 || !g[0].Automerge || g[0].AutomergeStrategy != "rebase" {
		t.Fatalf("groups = %+v", g)
	}
}
