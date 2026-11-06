package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Setup input ranges are rewritten only for versions outside them (rangeStrategy auto: no
// lockfile, so in-range versions are left alone), and branch refs are pinned to their commit
// under pin: digest (`@main` → `@<sha> # main`); a SHA with a branch comment follows the branch.
func TestActionsSetupRangesAndBranches(t *testing.T) {
	const (
		checkout = "https://github.com/actions/checkout"
		tool     = "https://github.com/o/tool"
		old      = "0000000000000000000000000000000000000000"
		head     = "1111111111111111111111111111111111111111"
	)
	wf := "jobs:\n  a:\n    runs-on: self-hosted\n    steps:\n" +
		"      - uses: actions/checkout@main\n" +
		"      - uses: o/tool@" + old + " # main\n" +
		"      - uses: actions/setup-go@v5\n        with:\n          go-version: 1.27.x\n" +
		"      - uses: actions/setup-node@v4\n        with:\n          node-version: \">=24\"\n"
	opts := Options{Managers: managers, Datasources: map[string]Datasource{
		"git-refs":     fakeBranches{checkout + "#main": head, tool + "#main": head},
		"git-tags":     fakeTags{},
		"go-toolchain": fakeReleases{"1.27.0", "1.27.3", "1.28.0", "1.28.2", "1.28"},
		"node-version": fakeReleases{"24.1.0", "26.0.0", "26"},
	}}
	cfg := "pin: digest\nrules:\n  - names: [actions/setup-go, actions/setup-node]\n    depTypes: [action]\n    ignore: true\n"
	root, c := setup(t, cfg, map[string]string{".github/workflows/ci.yml": wf})
	res, err := Resolve(t.Context(), root, c, env, opts)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	type up struct {
		name, version, digest string
		kind                  dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.NewDigest, u.Type})
	}
	want := []up{
		{"actions/checkout", "main", head, dep.Pin},
		{"o/tool", "main", head, dep.Digest},
		{"go", "1.28.2", "", dep.Minor},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}
	changes, errs := Build(t.Context(), root, managers, res.Updates, nil)
	if len(errs) > 0 || len(changes) != 1 {
		t.Fatalf("changes = %+v, errs = %v", changes, errs)
	}
	wantWF := strings.NewReplacer(
		"actions/checkout@main", "actions/checkout@"+head+" # main",
		"o/tool@"+old, "o/tool@"+head,
		"go-version: 1.27.x", "go-version: 1.28.x",
	).Replace(wf)
	if string(changes[0].New) != wantWF {
		t.Errorf("workflow =\n%s\nwant\n%s", changes[0].New, wantWF)
	}
}

// Without pin: digest a branch ref has nothing to update: it is skipped as `branch ref`.
func TestActionsBranchRefWithoutPin(t *testing.T) {
	opts := Options{Managers: managers, Datasources: map[string]Datasource{"git-refs": fakeBranches{}}}
	root, c := setup(t, "", map[string]string{".github/workflows/ci.yml": "jobs:\n  a:\n    runs-on: x\n    steps:\n      - uses: actions/checkout@main\n"})
	res, err := Resolve(t.Context(), root, c, env, opts)
	if err != nil || len(res.Errors) > 0 || len(res.Updates) > 0 || len(res.Skipped) != 1 || res.Skipped[0].Skip != "branch ref" {
		t.Fatalf("err = %v, errors = %v, updates = %v, skipped = %+v", err, res.Errors, res.Updates, res.Skipped)
	}
}
