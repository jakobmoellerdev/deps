package bot

import (
	"fmt"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

const pushed = "2222222222222222222222222222222222222222" // fakeGit.Commit

func TestRunAutomerge(t *testing.T) {
	yes, no := true, false
	unavailable := fmt.Errorf("enable auto-merge #1: %w: Pull request Pull request is in clean status", platform.ErrAutoMergeUnavailable)
	freshA := func(p platform.PR) []platform.PR {
		p.Number, p.Head, p.Title, p.Body, p.HeadSHA = 1, branchA, titleA, "FP_A", "aaaa"
		if p.Mergeable == nil {
			p.Mergeable = &yes
		}
		return []platform.PR{p}
	}
	tests := []struct {
		name       string
		dryRun     bool
		group      bool // a/x and b/y share one group; only a/x automerges
		pc         fakePC
		wantA      string // Action.Automerge of branchA (or the group)
		wantErr    string
		wantEnable []string
		wantChecks []string
		wantMerged []string
		wantGetPR  int
	}{
		{
			name:       "created PR requests auto-merge, the other group does not",
			wantA:      AutomergeRequested,
			wantEnable: []string{"101:squash@" + pushed},
		},
		{
			name:       "up-to-date PR requests auto-merge with its head",
			pc:         fakePC{open: freshA(platform.PR{})},
			wantA:      AutomergeRequested,
			wantEnable: []string{"1:squash@aaaa"},
		},
		{
			name:       "updated PR requests auto-merge with the pushed head",
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "stale", HeadSHA: "aaaa"}}},
			wantA:      AutomergeRequested,
			wantEnable: []string{"1:squash@" + pushed},
		},
		{
			name:  "already enabled is not requested again",
			pc:    fakePC{open: freshA(platform.PR{AutoMerge: true})},
			wantA: AutomergeEnabled,
		},
		{
			name:  "mixed group is not automerged",
			group: true,
			wantA: "",
		},
		{
			name:   "dry run makes no calls",
			dryRun: true,
			wantA:  AutomergeWould,
		},
		{
			name:   "dry run of an up-to-date PR",
			dryRun: true,
			pc:     fakePC{open: freshA(platform.PR{})},
			wantA:  AutomergeWould,
		},
		{
			name:       "pending is retried next run without an error",
			pc:         fakePC{fail: map[string]error{"EnableAutoMerge": fmt.Errorf("enable auto-merge #101: %w: 405", platform.ErrAutoMergePending)}},
			wantA:      AutomergePending,
			wantEnable: []string{"101:squash@" + pushed},
		},
		{
			name:       "other errors are reported, the PR action is kept",
			pc:         fakePC{fail: map[string]error{"EnableAutoMerge": errBoom}},
			wantErr:    branchA + ": boom",
			wantEnable: []string{"101:squash@" + pushed},
		},
		{
			name:       "fallback waits while mergeability is unknown",
			pc:         fakePC{open: freshA(platform.PR{}), headSHA: "bbbb", fail: map[string]error{"EnableAutoMerge": unavailable}},
			wantA:      AutomergeWaiting,
			wantEnable: []string{"1:squash@aaaa"},
			wantGetPR:  1,
		},
		{
			name:       "fallback waits while not mergeable",
			pc:         fakePC{open: freshA(platform.PR{}), mergeable: &no, headSHA: "bbbb", fail: map[string]error{"EnableAutoMerge": unavailable}},
			wantA:      AutomergeWaiting,
			wantEnable: []string{"1:squash@aaaa"},
			wantGetPR:  1,
		},
		{
			name:       "fallback waits for checks",
			pc:         fakePC{open: freshA(platform.PR{}), mergeable: &yes, headSHA: "bbbb", fail: map[string]error{"EnableAutoMerge": unavailable}},
			wantA:      AutomergeWaiting,
			wantEnable: []string{"1:squash@aaaa"},
			wantChecks: []string{"bbbb"},
			wantGetPR:  1,
		},
		{
			name:       "fallback merges a mergeable PR with green checks at the checked head",
			pc:         fakePC{open: freshA(platform.PR{}), mergeable: &yes, headSHA: "bbbb", checks: platform.ChecksSuccess, fail: map[string]error{"EnableAutoMerge": unavailable}},
			wantA:      AutomergeMerged,
			wantEnable: []string{"1:squash@aaaa"},
			wantChecks: []string{"bbbb"},
			wantMerged: []string{"1:squash@bbbb"},
			wantGetPR:  1,
		},
		{
			name:       "fallback check errors are reported",
			pc:         fakePC{open: freshA(platform.PR{}), mergeable: &yes, headSHA: "bbbb", fail: map[string]error{"EnableAutoMerge": unavailable, "Checks": errBoom}},
			wantA:      AutomergeWaiting,
			wantErr:    branchA + ": boom",
			wantEnable: []string{"1:squash@aaaa"},
			wantChecks: []string{"bbbb"},
			wantGetPR:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			res.Updates[0].Automerge, res.Updates[0].AutomergeStrategy = true, "squash"
			res.Updates[1].AutomergeStrategy = "squash"
			branch := branchA
			if tt.group {
				branch = "deps/group/g"
				res.Updates[0].Group, res.Updates[1].Group = "g", "g"
			}
			for i := range tt.pc.open {
				if tt.pc.open[i].Body == "FP_A" {
					tt.pc.open[i].Body = "x\n<!-- deps:fingerprint=" + fingerprintOf(t, root, res, "a/x") + " -->\n"
				}
			}
			g := &fakeGit{}
			acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers, DryRun: tt.dryRun}, []Base{{Root: root, Res: res, Git: g}}, nil, &tt.pc)
			if tt.wantErr == "" && len(errs) > 0 || tt.wantErr != "" && (len(errs) != 1 || errs[0].Error() != tt.wantErr) {
				t.Fatalf("errs = %v, want %q", errs, tt.wantErr)
			}
			got := map[string]Action{}
			for _, a := range acts {
				got[a.Branch] = a
			}
			a, ok := got[branch]
			if !ok || a.Automerge != tt.wantA {
				t.Errorf("%s: action %+v, want automerge %q", branch, a, tt.wantA)
			}
			if b := got[branchB]; b.Automerge != "" {
				t.Errorf("%s automerge %q", branchB, b.Automerge)
			}
			if !slices.Equal(tt.pc.automerge, tt.wantEnable) || !slices.Equal(tt.pc.checked, tt.wantChecks) || !slices.Equal(tt.pc.merged, tt.wantMerged) {
				t.Errorf("enable %v checks %v merged %v, want %v %v %v", tt.pc.automerge, tt.pc.checked, tt.pc.merged, tt.wantEnable, tt.wantChecks, tt.wantMerged)
			}
			if len(tt.pc.got) != tt.wantGetPR {
				t.Errorf("GetPR calls %v, want %d", tt.pc.got, tt.wantGetPR)
			}
		})
	}
}

func TestActionDisplay(t *testing.T) {
	if s := (Action{Status: StatusCreated, Automerge: AutomergeRequested}).Display(); s != "created, automerge" {
		t.Errorf("Display = %q", s)
	}
	if s := (Action{Status: StatusUpToDate}).Display(); s != "up-to-date" {
		t.Errorf("Display = %q", s)
	}
}
