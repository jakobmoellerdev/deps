package bot

import (
	"maps"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// The first run after the branch names lost their manager segment opens the PRs of the new
// names and closes the per-manager PRs of earlier versions as obsolete.
func TestRunClosesPerManagerPRs(t *testing.T) {
	const oldA, oldB = "deps/github-actions/a-x", "deps/github-actions/b-y"
	root, res := fixture(t)
	pc := &fakePC{open: []platform.PR{{Number: 1, Head: oldA, Title: titleA}, {Number: 2, Head: oldB}}}
	g := fakeGit{branches: map[string]string{"refs/heads/" + oldA: tipA, "refs/heads/" + oldB: tipB}, authors: map[string]string{oldA: email, oldB: email}}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: &g}}, nil, pc)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if want := []string{branchA, branchB}; !slices.Equal(pc.created, want) {
		t.Errorf("created %v, want %v", pc.created, want)
	}
	if !slices.Equal(pc.closedNow, []int{1, 2}) || !slices.Equal(g.deleted, []string{oldA, oldB}) {
		t.Errorf("closed %v, deleted %v", pc.closedNow, g.deleted)
	}
	got := map[string]string{}
	for _, a := range acts {
		got[a.Branch] = a.Status
	}
	if want := map[string]string{branchA: StatusCreated, branchB: StatusCreated, oldA: StatusClosed, oldB: StatusClosed}; !maps.Equal(got, want) {
		t.Errorf("actions %v, want %v", got, want)
	}
}
