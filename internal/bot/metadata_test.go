package bot

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestPRMetadataOnCreate(t *testing.T) {
	root, res := fixture(t)
	res.Updates[0].Labels, res.Updates[0].Reviewers = []string{"dependencies", "actions"}, []string{"alice"}
	res.Updates[1].Assignees = []string{"bob"}

	// a/x is new, b/y already has an open PR (it is only updated).
	pc := &fakePC{open: []platform.PR{openPR(branchB)}, fail: map[string]error{}}
	o := Options{AuthorEmail: email, Managers: managers}
	acts, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: &fakeGit{branches: map[string]string{"refs/heads/" + branchB: "x"}, authors: map[string]string{branchB: email}}}}, nil, pc)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if statusOf(acts, branchA) != StatusCreated || statusOf(acts, branchB) != StatusUpdated {
		t.Fatalf("actions = %+v", acts)
	}
	if len(pc.metadata) != 1 {
		t.Fatalf("metadata = %+v, want one call for the created PR", pc.metadata)
	}
	for _, m := range pc.metadata {
		if !slices.Equal(m.Labels, []string{"dependencies", "actions"}) || !slices.Equal(m.Reviewers, []string{"alice"}) || m.Assignees != nil {
			t.Errorf("metadata = %+v", m)
		}
	}
}

func TestPRMetadataFailureKeepsPR(t *testing.T) {
	root, res := fixture(t)
	for i := range res.Updates {
		res.Updates[i].Labels = []string{"dependencies"}
	}
	pc := &fakePC{fail: map[string]error{"SetPRMetadata:101": errors.New("422 no such label")}}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: &fakeGit{}}}, nil, pc)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "422 no such label") {
		t.Errorf("errs = %v", errs)
	}
	if len(pc.created) != 2 || statusOf(acts, pc.created[0]) != StatusCreated || len(pc.metadata) != 2 {
		t.Errorf("created %v, actions %+v, metadata %+v", pc.created, acts, pc.metadata)
	}
}

// Labels missing from an open PR (a SetPRMetadata that failed when it was created) are
// re-applied on the next run; automerge workflows key on them.
func TestPRLabelsReconciled(t *testing.T) {
	root, res := fixture(t)
	for i := range res.Updates {
		res.Updates[i].Labels, res.Updates[i].Reviewers = []string{"dependencies", "actions"}, []string{"alice"}
	}
	bare, labelled := openPR(branchA), openPR(branchB)
	bare.Number, labelled.Number = 1, 2
	labelled.Labels = []string{"Dependencies", "actions", "extra"} // complete, as forges compare
	pc := &fakePC{open: []platform.PR{bare, labelled}}
	g := &fakeGit{
		branches: map[string]string{"refs/heads/" + branchA: "x", "refs/heads/" + branchB: "y"},
		authors:  map[string]string{branchA: email, branchB: email},
	}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: g}}, nil, pc)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if statusOf(acts, branchA) != StatusUpdated || statusOf(acts, branchB) != StatusUpdated {
		t.Fatalf("actions = %+v", acts)
	}
	m, ok := pc.metadata[1]
	if len(pc.metadata) != 1 || !ok {
		t.Fatalf("metadata = %+v, want one call for #1", pc.metadata)
	}
	if !slices.Equal(m.Labels, []string{"dependencies", "actions"}) || m.Reviewers != nil || m.Assignees != nil {
		t.Errorf("metadata = %+v, want only the missing labels", m)
	}
}

// openPR is an open PR of branch whose content differs (an old fingerprint), so it is updated.
func openPR(branch string) platform.PR {
	yes := true
	return platform.PR{Number: 9, Head: branch, Body: "<!-- deps:fingerprint=0000000000000000 -->", Mergeable: &yes}
}
