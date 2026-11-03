package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

const head = "1111111111111111111111111111111111111111"

type fakeGit struct {
	branches map[string]string // ref -> sha
	authors  map[string]string // branch -> email
	pushed   []string          // branches
	deleted  []string
}

func (g *fakeGit) RevParse(context.Context, string) (string, error) { return head, nil }
func (g *fakeGit) RemoteURL(context.Context) (string, error)        { return "", nil }
func (g *fakeGit) LsRemote(_ context.Context, pattern string) (map[string]string, error) {
	if pattern == "refs/heads/main" {
		return map[string]string{"refs/heads/main": head}, nil
	}
	return g.branches, nil
}
func (g *fakeGit) Commit(context.Context, string, []engine.FileChange, string, string, string) (string, error) {
	return "2222222222222222222222222222222222222222", nil
}
func (g *fakeGit) Push(_ context.Context, _, branch, _ string) error {
	g.pushed = append(g.pushed, branch)
	return nil
}
func (g *fakeGit) DeleteBranch(_ context.Context, branch string) error {
	g.deleted = append(g.deleted, branch)
	return nil
}
func (g *fakeGit) BranchAuthorEmail(_ context.Context, branch string) (string, error) {
	return g.authors[branch], nil
}

type fakePC struct {
	open, closed []platform.PR
	created      []string // heads
	updated      []int
	closedNow    []int
}

func (c *fakePC) DefaultBranch(context.Context) (string, error) { return "main", nil }
func (c *fakePC) ListPRs(_ context.Context, state string, _ int) ([]platform.PR, error) {
	if state == "open" {
		return c.open, nil
	}
	return c.closed, nil
}
func (c *fakePC) GetPR(_ context.Context, n int) (platform.PR, error) {
	return platform.PR{Number: n}, nil
}
func (c *fakePC) CreatePR(_ context.Context, head, _, _, _ string) (platform.PR, error) {
	c.created = append(c.created, head)
	return platform.PR{Number: 100 + len(c.created), Head: head}, nil
}
func (c *fakePC) UpdatePR(_ context.Context, n int, _, _ string) error {
	c.updated = append(c.updated, n)
	return nil
}
func (c *fakePC) ClosePR(_ context.Context, n int) error {
	c.closedNow = append(c.closedNow, n)
	return nil
}

const (
	branchA = "deps/github-actions/a-x"
	titleA  = "chore(deps): update a/x to v2"
	branchB = "deps/github-actions/b-y"
	email   = "bot@example.invalid"
)

func fixture(t *testing.T) (string, *engine.Result) {
	t.Helper()
	root := t.TempDir()
	src := "steps:\n  - uses: a/x@v1\n  - uses: b/y@v1\n"
	p := filepath.Join(root, "wf.yml")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, start int) dep.Update {
		return dep.Update{
			Dep:  dep.Dep{Manager: "github-actions", File: "wf.yml", Line: 2, Name: name, Version: "v1", RefSpan: dep.Span{Start: start, End: start + 2}},
			Type: dep.Major, NewVersion: "v2",
		}
	}
	return root, &engine.Result{Updates: []dep.Update{mk("a/x", 21), mk("b/y", 38)}}
}

// fingerprintOf returns the fingerprint the bot computes for one group.
func fingerprintOf(t *testing.T, root string, res *engine.Result, name string) string {
	t.Helper()
	for _, g := range engine.GroupUpdates(res.Updates) {
		if g.Name == name {
			changes, errs := engine.Build(t.Context(), root, g.Updates, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			return fingerprint(changes)
		}
	}
	t.Fatalf("no group %s", name)
	return ""
}

func TestRun(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name        string
		opts        Options
		git         fakeGit
		pc          fakePC
		engineErr   bool
		wantStatus  map[string]string
		wantPushed  int
		wantCreated int
		wantUpdated int
		wantClosed  int
		wantDeleted int
	}{
		{
			name:        "create both",
			wantStatus:  map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantPushed:  2,
			wantCreated: 2,
		},
		{
			name:       "fingerprint match is up to date",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &yes}}},
			opts:       Options{MaxNewPRs: 1},
			wantStatus: map[string]string{branchA: StatusUpToDate, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1,
		},
		{
			name:       "fingerprint match but conflicting is rebuilt",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &no}}},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantUpdated: 1,
		},
		{
			name:       "fingerprint change updates",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Body: "<!-- deps:fingerprint=0000000000000000 -->"}}},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantUpdated: 1,
		},
		{
			name:       "closed unmerged PR with same title is respected",
			pc:         fakePC{closed: []platform.PR{{Number: 2, Head: branchA, Title: titleA}}},
			wantStatus: map[string]string{branchA: StatusSkipClosed, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1,
		},
		{
			name:       "merged PR with same title does not block",
			pc:         fakePC{closed: []platform.PR{{Number: 2, Head: branchA, Title: titleA, Merged: true}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 2,
		},
		{
			name:       "new PR limit",
			opts:       Options{MaxNewPRs: 1},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusSkipLimit},
			wantPushed: 1, wantCreated: 1,
		},
		{
			name:       "foreign commit on branch",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: "human@example.com"}},
			wantStatus: map[string]string{branchA: StatusSkipModified, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1,
		},
		{
			name:       "dry run writes nothing",
			opts:       Options{DryRun: true},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA}, {Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusWouldUpdate, branchB: StatusWouldCreate},
		},
		{
			name:       "obsolete PR closed",
			git:        fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x"}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}, {Number: 10, Head: "feature/x"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantCreated: 2, wantClosed: 1, wantDeleted: 1,
		},
		{
			name:       "obsolete PR kept on engine error",
			engineErr:  true,
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			if tt.engineErr {
				res.Errors = []error{errors.New("upstream down")}
			}
			fpA := fingerprintOf(t, root, res, "a/x")
			for i := range tt.pc.open {
				if tt.pc.open[i].Body == "FP_A" {
					tt.pc.open[i].Body = "x\n<!-- deps:fingerprint=" + fpA + " -->\n"
				}
			}
			o := tt.opts
			o.AuthorEmail = email
			if o.MaxNewPRs == 0 {
				o.MaxNewPRs = 10
			}
			acts, errs := Run(t.Context(), o, root, res, nil, &tt.pc, &tt.git)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			got := map[string]string{}
			for _, a := range acts {
				got[a.Branch] = a.Status
			}
			if len(got) != len(tt.wantStatus) {
				t.Errorf("actions = %v, want %v", got, tt.wantStatus)
			}
			for b, s := range tt.wantStatus {
				if got[b] != s {
					t.Errorf("%s: status %q, want %q", b, got[b], s)
				}
			}
			if len(tt.git.pushed) != tt.wantPushed || len(tt.pc.created) != tt.wantCreated ||
				len(tt.pc.updated) != tt.wantUpdated || len(tt.pc.closedNow) != tt.wantClosed || len(tt.git.deleted) != tt.wantDeleted {
				t.Errorf("pushed=%v created=%v updated=%v closed=%v deleted=%v", tt.git.pushed, tt.pc.created, tt.pc.updated, tt.pc.closedNow, tt.git.deleted)
			}
		})
	}
}

func TestRunRejectsStaleCheckout(t *testing.T) {
	root, res := fixture(t)
	g := &staleGit{}
	_, errs := Run(t.Context(), Options{MaxNewPRs: 10}, root, res, nil, &fakePC{}, g)
	if len(errs) != 1 || g.pushed != nil {
		t.Fatalf("errs = %v, pushed = %v", errs, g.pushed)
	}
}

type staleGit struct{ fakeGit }

func (g *staleGit) LsRemote(_ context.Context, pattern string) (map[string]string, error) {
	return map[string]string{"refs/heads/main": "3333333333333333333333333333333333333333"}, nil
}
