package bot

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// baseGit is a fakeGit checked out at the tip of base.
type baseGit struct {
	*fakeGit
	base string
}

func (g baseGit) LsRemote(ctx context.Context, pattern string) (map[string]string, error) {
	if pattern == "refs/heads/"+g.base {
		return map[string]string{pattern: head}, nil
	}
	return g.fakeGit.LsRemote(ctx, pattern)
}

const (
	mainA    = "deps/main/github-actions/a-x"
	mainB    = "deps/main/github-actions/b-y"
	releaseA = "deps/release/github-actions/a-x"
	releaseB = "deps/release/github-actions/b-y"
	stale    = "deps/release/github-actions/stale"
)

// multiBase runs the fixture on main and release with a shared limit of two new PRs. Open:
// #1 the main PR of a/x from before multi-base (plain branch name), #2 the release PR of b/y,
// #3 a release PR whose target the forge did not report, #4 a PR to a branch the run does not
// handle. stale is a release branch without a PR.
func multiBase(t *testing.T, releaseErr error) ([]Action, []error, *fakePC, *fakeGit) {
	t.Helper()
	rootM, resM := fixture(t)
	rootR, resR := fixture(t)
	resM.Deps = []dep.Dep{resM.Updates[0].Dep}
	resR.Deps = []dep.Dep{resR.Updates[1].Dep}
	if releaseErr != nil {
		resR.Errors = []error{releaseErr}
	}
	gm := &fakeGit{
		branches: map[string]string{"refs/heads/" + branchA: "x", "refs/heads/" + stale: "y"},
		authors:  map[string]string{stale: email},
	}
	pc := &fakePC{open: []platform.PR{
		{Number: 1, Head: branchA, Base: "main"},
		{Number: 2, Head: releaseB, Base: "release"},
		{Number: 3, Head: "deps/release/github-actions/gone"},
		{Number: 4, Head: branchB, Base: "other"},
	}}
	o := Options{AuthorEmail: email, Managers: managers, MaxNewPRs: 2, Now: dashNow, Dashboard: &DashboardOptions{Title: dashTitle}}
	acts, errs := Run(t.Context(), o, []Base{
		{Name: "main", Root: rootM, Res: resM, Git: gm},
		{Name: "release", Root: rootR, Res: resR, Git: baseGit{&fakeGit{}, "release"}},
	}, nil, pc)
	return acts, errs, pc, gm
}

func TestRunMultiBase(t *testing.T) {
	acts, errs, pc, gm := multiBase(t, nil)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if want := []string{mainA, mainB}; !slices.Equal(pc.created, want) || !slices.Equal(pc.createdBases, []string{"main", "main"}) {
		t.Errorf("created = %q on %q, want %q on main", pc.created, pc.createdBases, want)
	}
	for branch, want := range map[string]string{mainA: StatusCreated, mainB: StatusCreated, releaseA: StatusSkipLimit, releaseB: StatusUpdated} {
		if got := statusOf(acts, branch); got != want {
			t.Errorf("%s: status = %q, want %q", branch, got, want)
		}
	}
	if !slices.Equal(pc.updated, []int{2}) {
		t.Errorf("updated = %v, want [2]", pc.updated)
	}
	if !slices.Equal(pc.closedNow, []int{1, 3}) {
		t.Errorf("closed = %v, want [1 3] (the PR to other stays open)", pc.closedNow)
	}
	if want := []string{branchA, stale}; !slices.Equal(gm.deleted, want) {
		t.Errorf("deleted = %q, want %q", gm.deleted, want)
	}
	if len(pc.createdIssues) != 1 {
		t.Fatalf("dashboard issues = %d, want 1", len(pc.createdIssues))
	}
	body := pc.createdIssues[0].Body
	order := []string{
		"## Base branch `main`", "deps:rebase=" + mainA,
		"## Base branch `release`", "deps:create=" + releaseA, "deps:rebase=" + releaseB,
		"## Detected dependencies", "### `main`", "a/x v1", "### `release`", "b/y v1",
	}
	at := 0
	for _, s := range order {
		i := strings.Index(body[at:], s)
		if i < 0 {
			t.Fatalf("dashboard lacks %q after byte %d:\n%s", s, at, body)
		}
		at += i + len(s)
	}
}

func TestRunMultiBaseResolutionError(t *testing.T) {
	_, errs, pc, gm := multiBase(t, errors.New("upstream down"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !slices.Equal(pc.closedNow, []int{1}) {
		t.Errorf("closed = %v, want [1]: release's obsolete PRs stay open", pc.closedNow)
	}
	if want := []string{branchA}; !slices.Equal(gm.deleted, want) {
		t.Errorf("deleted = %q, want %q: branches without a PR stay", gm.deleted, want)
	}
	if body := pc.createdIssues[0].Body; !strings.Contains(body, "`release: upstream down`") {
		t.Errorf("dashboard should name the base of the error:\n%s", body)
	}
}

func TestRunMultiBaseOneBase(t *testing.T) {
	root, res := fixture(t)
	pc := &fakePC{}
	if _, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers, MultiBase: true}, []Base{{Root: root, Res: res, Git: &fakeGit{}}}, nil, pc); len(errs) > 0 {
		t.Fatal(errs)
	}
	if want := []string{mainA, mainB}; !slices.Equal(pc.created, want) {
		t.Errorf("created = %q, want %q", pc.created, want)
	}
}

func TestRunSingleBaseIgnoresOtherBases(t *testing.T) {
	root, res := fixture(t)
	pc := &fakePC{open: []platform.PR{{Number: 9, Head: "deps/github-actions/gone", Base: "release"}, {Number: 8, Head: "deps/github-actions/old", Base: "main"}}}
	if _, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: &fakeGit{}}}, nil, pc); len(errs) > 0 {
		t.Fatal(errs)
	}
	if want := []string{branchA, branchB}; !slices.Equal(pc.created, want) {
		t.Errorf("created = %q, want %q (single-base names)", pc.created, want)
	}
	if !slices.Equal(pc.closedNow, []int{8}) {
		t.Errorf("closed = %v, want [8]: a PR to another base is not this run's", pc.closedNow)
	}
}

func TestRenderDashboardSkipsEmptyBase(t *testing.T) {
	d := dep.Dep{Manager: "gomod", File: "go.mod", Name: "example.com/m", Version: "v1.0.0"}
	body := renderDashboard([]dashboardSection{
		{base: "main", deps: []dep.Dep{d}},
		{base: "release", actions: []Action{{Branch: releaseA, Title: "t", Status: StatusSkipLimit}}},
	}, nil)
	for s, want := range map[string]bool{
		"## Base branch `main`": false, "## Base branch `release`\n\n### Rate-limited": true,
		"## Detected dependencies\n\n### `main`": true, "### `release`": false,
	} {
		if strings.Contains(body, s) != want {
			t.Errorf("contains %q = %v, want %v:\n%s", s, !want, want, body)
		}
	}
}
