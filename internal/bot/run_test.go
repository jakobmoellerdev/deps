package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

var managers = []manager.Manager{actions.New()}

const head = "1111111111111111111111111111111111111111"

var errBoom = errors.New("boom")

// fail returns the injected error for a method, keyed by "<method>:<arg>" (branch, PR head,
// ref pattern or state) or by the method name alone.
func fail(m map[string]error, method, arg string) error {
	if err := m[method+":"+arg]; err != nil {
		return err
	}
	return m[method]
}

type fakeGit struct {
	branches map[string]string // ref -> sha
	authors  map[string]string // branch -> email
	fail     map[string]error
	pushed   []string // branches
	deleted  []string
	patterns []string // LsRemote patterns
}

func (g *fakeGit) RevParse(context.Context, string) (string, error) {
	return head, fail(g.fail, "RevParse", "")
}
func (g *fakeGit) RemoteURL(context.Context) (string, error) { return "", nil }
func (g *fakeGit) LsRemote(_ context.Context, pattern string) (map[string]string, error) {
	g.patterns = append(g.patterns, pattern)
	if err := fail(g.fail, "LsRemote", pattern); err != nil {
		return nil, err
	}
	if pattern == "refs/heads/main" {
		return map[string]string{"refs/heads/main": head}, nil
	}
	return g.branches, nil
}
func (g *fakeGit) Commit(context.Context, string, []engine.FileChange, string, string, string) (string, error) {
	if err := fail(g.fail, "Commit", ""); err != nil {
		return "", err
	}
	return "2222222222222222222222222222222222222222", nil
}
func (g *fakeGit) Push(_ context.Context, _, branch, _ string) error {
	if err := fail(g.fail, "Push", branch); err != nil {
		return err
	}
	g.pushed = append(g.pushed, branch)
	return nil
}
func (g *fakeGit) DeleteBranch(_ context.Context, branch string) error {
	if err := fail(g.fail, "DeleteBranch", branch); err != nil {
		return err
	}
	g.deleted = append(g.deleted, branch)
	return nil
}
func (g *fakeGit) BranchAuthorEmail(_ context.Context, branch string) (string, error) {
	return g.authors[branch], fail(g.fail, "BranchAuthorEmail", branch)
}

type fakePC struct {
	open, closed []platform.PR
	mergeable    *bool // returned by GetPR
	fail         map[string]error
	got          []int    // GetPR numbers
	created      []string // heads
	updated      []int
	closedNow    []int
}

func (c *fakePC) DefaultBranch(context.Context) (string, error) {
	return "main", fail(c.fail, "DefaultBranch", "")
}
func (c *fakePC) ListPRs(_ context.Context, state string, _ int) ([]platform.PR, error) {
	if err := fail(c.fail, "ListPRs", state); err != nil {
		return nil, err
	}
	if state == "open" {
		return c.open, nil
	}
	return c.closed, nil
}
func (c *fakePC) GetPR(_ context.Context, n int) (platform.PR, error) {
	if err := fail(c.fail, "GetPR", ""); err != nil {
		return platform.PR{}, err
	}
	c.got = append(c.got, n)
	return platform.PR{Number: n, Mergeable: c.mergeable}, nil
}
func (c *fakePC) CreatePR(_ context.Context, head, _, _, _ string) (platform.PR, error) {
	if err := fail(c.fail, "CreatePR", head); err != nil {
		return platform.PR{}, err
	}
	c.created = append(c.created, head)
	return platform.PR{Number: 100 + len(c.created), Head: head}, nil
}
func (c *fakePC) UpdatePR(_ context.Context, n int, _, _ string) error {
	if err := fail(c.fail, "UpdatePR", ""); err != nil {
		return err
	}
	c.updated = append(c.updated, n)
	return nil
}
func (c *fakePC) ClosePR(_ context.Context, n int) error {
	if err := fail(c.fail, "ClosePR", ""); err != nil {
		return err
	}
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
	for _, g := range engine.GroupUpdates(res.Updates, "") {
		if g.Name == name {
			changes, errs := engine.Build(t.Context(), root, managers, g.Updates, nil)
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
		extra       []dep.Update // appended to the fixture's updates
		wantStatus  map[string]string
		wantErrs    []string // substrings, one per returned error, in order
		wantPushed  int
		wantCreated int
		wantUpdated int
		wantClosed  int
		wantDeleted int
		wantGetPR   int
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
		{
			name:       "unknown mergeability is looked up and treated as mergeable",
			opts:       Options{Base: "main"},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A"}}, fail: map[string]error{"DefaultBranch": errBoom}},
			wantStatus: map[string]string{branchA: StatusUpToDate, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1, wantGetPR: 1,
		},
		{
			name:       "looked up conflicting PR is rebuilt",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A"}}, mergeable: &no},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantUpdated: 1, wantGetPR: 1,
		},
		{
			name:       "GetPR failure isolated to its group",
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A"}}, fail: map[string]error{"GetPR": errBoom}},
			wantStatus: map[string]string{branchB: StatusCreated},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 1, wantCreated: 1,
		},
		{
			name:       "build error isolates its group and keeps obsolete PRs",
			extra:      []dep.Update{{Dep: dep.Dep{Manager: "github-actions", File: "missing.yml", Line: 1, Name: "c/z", Version: "v1", RefSpan: dep.Span{Start: 0, End: 2}}, Type: dep.Major, NewVersion: "v2"}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantErrs:   []string{"deps/github-actions/c-z: missing.yml: "},
			wantPushed: 2, wantCreated: 2,
		},
		{
			name:       "push failure isolated to its group and keeps obsolete PRs",
			git:        fakeGit{fail: map[string]error{"Push:" + branchA: errBoom}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchB: StatusCreated},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 1, wantCreated: 1,
		},
		{
			name:       "commit failure",
			git:        fakeGit{fail: map[string]error{"Commit": errBoom}},
			wantStatus: map[string]string{},
			wantErrs:   []string{branchA + ": boom", branchB + ": boom"},
		},
		{
			name:       "branch author lookup failure",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, fail: map[string]error{"BranchAuthorEmail:" + branchA: errBoom}},
			wantStatus: map[string]string{branchB: StatusCreated},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 1, wantCreated: 1,
		},
		{
			name:       "CreatePR failure after push",
			pc:         fakePC{fail: map[string]error{"CreatePR:" + branchA: errBoom}},
			wantStatus: map[string]string{branchB: StatusCreated},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 2, wantCreated: 1,
		},
		{
			name:       "UpdatePR failure after push",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA}}, fail: map[string]error{"UpdatePR": errBoom}},
			wantStatus: map[string]string{branchB: StatusCreated},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 2, wantCreated: 1,
		},
		{
			name:       "ClosePR failure keeps the branch",
			git:        fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x"}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}, fail: map[string]error{"ClosePR": errBoom}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantErrs:   []string{"close #9: boom"},
			wantPushed: 2, wantCreated: 2,
		},
		{
			name:       "DeleteBranch failure still reports the closed PR",
			git:        fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x"}, fail: map[string]error{"DeleteBranch": errBoom}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantErrs:   []string{"delete deps/gomod/gone: boom"},
			wantPushed: 2, wantCreated: 2, wantClosed: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			if tt.engineErr {
				res.Errors = []error{errors.New("upstream down")}
			}
			res.Updates = append(res.Updates, tt.extra...)
			fpA := fingerprintOf(t, root, res, "a/x")
			for i := range tt.pc.open {
				if tt.pc.open[i].Body == "FP_A" {
					tt.pc.open[i].Body = "x\n<!-- deps:fingerprint=" + fpA + " -->\n"
				}
			}
			o := tt.opts
			o.AuthorEmail = email
			o.Managers = managers
			if o.MaxNewPRs == 0 {
				o.MaxNewPRs = 10
			}
			acts, errs := Run(t.Context(), o, root, res, nil, &tt.pc, &tt.git)
			if len(errs) != len(tt.wantErrs) {
				t.Fatalf("errs = %v, want %q", errs, tt.wantErrs)
			}
			for i, want := range tt.wantErrs {
				if !strings.Contains(errs[i].Error(), want) {
					t.Errorf("err %d = %q, want it to contain %q", i, errs[i], want)
				}
			}
			if len(tt.pc.got) != tt.wantGetPR {
				t.Errorf("GetPR calls = %v, want %d", tt.pc.got, tt.wantGetPR)
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
	_, errs := Run(t.Context(), Options{MaxNewPRs: 10, Managers: managers}, root, res, nil, &fakePC{}, g)
	if len(errs) != 1 || g.pushed != nil {
		t.Fatalf("errs = %v, pushed = %v", errs, g.pushed)
	}
}

func TestRunBranchPrefixAndUnlimitedPRs(t *testing.T) {
	root, res := fixture(t)
	g := &fakeGit{branches: map[string]string{"refs/heads/bot/gomod/gone": "x"}}
	pc := &fakePC{open: []platform.PR{{Number: 9, Head: "bot/gomod/gone"}, {Number: 10, Head: "deps/gomod/foreign"}}}
	o := Options{BranchPrefix: "bot/", AuthorEmail: email, Managers: managers} // MaxNewPRs 0 = unlimited
	acts, errs := Run(t.Context(), o, root, res, nil, pc, g)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if want := []string{"refs/heads/main", "refs/heads/bot/*"}; !slices.Equal(g.patterns, want) {
		t.Errorf("LsRemote patterns = %v, want %v", g.patterns, want)
	}
	if want := []string{"bot/github-actions/a-x", "bot/github-actions/b-y"}; !slices.Equal(pc.created, want) {
		t.Errorf("created = %v, want %v", pc.created, want)
	}
	if !slices.Equal(pc.closedNow, []int{9}) || !slices.Equal(g.deleted, []string{"bot/gomod/gone"}) {
		t.Errorf("closed = %v deleted = %v; only PRs under the prefix are managed", pc.closedNow, g.deleted)
	}
	if len(acts) != 3 {
		t.Errorf("actions = %+v", acts)
	}
}

type staleGit struct{ fakeGit }

func (g *staleGit) LsRemote(_ context.Context, pattern string) (map[string]string, error) {
	return map[string]string{"refs/heads/main": "3333333333333333333333333333333333333333"}, nil
}

func TestRunStateErrors(t *testing.T) {
	tests := []struct {
		name    string
		gitFail map[string]error
		pcFail  map[string]error
		want    string
	}{
		{name: "default branch", pcFail: map[string]error{"DefaultBranch": errBoom}, want: "default branch: boom"},
		{name: "rev-parse", gitFail: map[string]error{"RevParse": errBoom}, want: "boom"},
		{name: "base tip", gitFail: map[string]error{"LsRemote:refs/heads/main": errBoom}, want: "boom"},
		{name: "deps branches", gitFail: map[string]error{"LsRemote:refs/heads/deps/*": errBoom}, want: "boom"},
		{name: "open PRs", pcFail: map[string]error{"ListPRs:open": errBoom}, want: "list open PRs: boom"},
		{name: "closed PRs", pcFail: map[string]error{"ListPRs:closed": errBoom}, want: "list closed PRs: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			g := &fakeGit{fail: tt.gitFail}
			pc := &fakePC{fail: tt.pcFail}
			acts, errs := Run(t.Context(), Options{MaxNewPRs: 10, AuthorEmail: email, Managers: managers}, root, res, nil, pc, g)
			if len(errs) != 1 || errs[0].Error() != tt.want || !errors.Is(errs[0], errBoom) {
				t.Fatalf("errs = %v, want [%s]", errs, tt.want)
			}
			if acts != nil || g.pushed != nil || pc.created != nil {
				t.Errorf("acted despite state error: actions=%v pushed=%v created=%v", acts, g.pushed, pc.created)
			}
		})
	}
}
