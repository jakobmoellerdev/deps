package bot

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
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
	branches map[string]string   // ref -> sha
	authors  map[string]string   // branch -> email
	parents  map[string][]string // branch -> tip parents; unset = [head]
	trees    map[string]string   // branch -> tip tree; WriteTree always returns newTree
	fail     map[string]error
	pushed   []string // branches
	pushes   []string // "<commit>:<branch>@<expected>"
	deleted  []string
	deletes  []string             // "<branch>@<expected>"
	patterns []string             // LsRemote patterns
	tips     []string             // BranchTip branches
	dates    map[string]time.Time // BranchTip committer dates by branch; zero = unknown
	objects  map[string]string    // path -> object id at head; unset = "obj:<path>", "" = missing
	// revObjects are the object ids at other revs (branch tips, written trees): rev -> path
	// -> id, unset paths missing.
	revObjects map[string]map[string]string
	oidCalls   int      // ObjectIDs calls
	messages   []string // Commit messages
}

// newTree is the tree fakeGit.WriteTree writes.
const newTree = "7777777777777777777777777777777777777777"

// fakeListMu guards the fake fields Run's concurrent state reads and
// mergeability lookups write (fakeGit.patterns, fakePC.issuePages, fakePC.got);
// the fakes are copied by value in test tables, so the lock cannot live in them.
var fakeListMu sync.Mutex

func (g *fakeGit) RevParse(context.Context, string) (string, error) {
	return head, fail(g.fail, "RevParse", "")
}
func (g *fakeGit) RemoteURL(context.Context) (string, error) { return "", nil }
func (g *fakeGit) LsRemote(_ context.Context, pattern string) (map[string]string, error) {
	fakeListMu.Lock()
	g.patterns = append(g.patterns, pattern)
	fakeListMu.Unlock()
	if err := fail(g.fail, "LsRemote", pattern); err != nil {
		return nil, err
	}
	if pattern == "refs/heads/main" {
		return map[string]string{"refs/heads/main": head}, nil
	}
	return g.branches, nil
}
func (g *fakeGit) ObjectIDs(_ context.Context, rev string, paths []string) ([]string, error) {
	g.oidCalls++
	if err := fail(g.fail, "ObjectIDs", ""); err != nil {
		return nil, err
	}
	if rev != head {
		objs, ok := g.revObjects[rev]
		if !ok {
			return nil, fmt.Errorf("ObjectIDs at %s, want head", rev)
		}
		ids := make([]string, len(paths))
		for i, p := range paths {
			ids[i] = objs[p]
		}
		return ids, nil
	}
	ids := make([]string, len(paths))
	for i, p := range paths {
		id, ok := g.objects[p]
		if !ok {
			id = "obj:" + p
		}
		ids[i] = id
	}
	return ids, nil
}
func (g *fakeGit) WriteTree(context.Context, string, []engine.FileChange) (string, error) {
	return newTree, fail(g.fail, "WriteTree", "")
}
func (g *fakeGit) Commit(_ context.Context, _ string, _ []engine.FileChange, msg, _, _ string) (string, error) {
	g.messages = append(g.messages, msg)
	if err := fail(g.fail, "Commit", ""); err != nil {
		return "", err
	}
	return "2222222222222222222222222222222222222222", nil
}
func (g *fakeGit) Push(_ context.Context, commit, branch, expected string) error {
	if err := fail(g.fail, "Push", branch); err != nil {
		return err
	}
	g.pushed = append(g.pushed, branch)
	g.pushes = append(g.pushes, commit+":"+branch+"@"+expected)
	return nil
}
func (g *fakeGit) DeleteBranch(_ context.Context, branch, expected string) error {
	if err := fail(g.fail, "DeleteBranch", branch); err != nil {
		return err
	}
	g.deleted = append(g.deleted, branch)
	g.deletes = append(g.deletes, branch+"@"+expected)
	return nil
}
func (g *fakeGit) BranchTip(_ context.Context, branch string) (platform.Tip, error) {
	g.tips = append(g.tips, branch)
	parents, ok := g.parents[branch]
	if !ok {
		parents = []string{head}
	}
	return platform.Tip{Author: g.authors[branch], Parents: parents, Tree: g.trees[branch], Date: g.dates[branch]}, fail(g.fail, "BranchTip", branch)
}

type fakePC struct {
	open, closed []platform.PR
	mergeable    *bool // returned by GetPR
	headSHA      string
	fail         map[string]error            // EnableAutoMerge errors are keyed by the PR number
	got          []int                       // GetPR numbers
	created      []string                    // heads
	createdBases []string                    // CreatePR bases
	metadata     map[int]platform.PRMetadata // SetPRMetadata calls by PR number
	bodies       []string                    // CreatePR bodies
	titles       []string                    // CreatePR titles
	drafts       []bool                      // CreatePR draft flags
	updated      []int
	updateDrafts []bool // UpdatePR draft flags
	recorded     []int  // UpdatePR calls that only set the input fingerprint of the listed body
	closedNow    []int
	prBodies     map[int]string // last UpdatePR body by PR number
	unlabeled    []string       // RemovePRLabel "<number>:<label>"

	checks    platform.CheckState
	automerge []string // EnableAutoMerge "<number>:<strategy>@<head sha>"
	checked   []string // Checks shas
	merged    []string // MergePR "<number>:<strategy>@<head sha>"

	issues        []platform.Issue
	issuePages    int
	issueOldest   bool
	createdIssues []platform.Issue // title, body
	issueLabels   [][]string
	updatedIssues []platform.Issue // number, body
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
	fakeListMu.Lock()
	c.got = append(c.got, n)
	fakeListMu.Unlock()
	return platform.PR{Number: n, Mergeable: c.mergeable, HeadSHA: c.headSHA}, nil
}
func (c *fakePC) CreatePR(_ context.Context, head, base, title, body string, draft bool) (platform.PR, error) {
	if err := fail(c.fail, "CreatePR", head); err != nil {
		return platform.PR{}, err
	}
	c.created = append(c.created, head)
	c.createdBases = append(c.createdBases, base)
	c.bodies = append(c.bodies, body)
	c.titles = append(c.titles, title)
	c.drafts = append(c.drafts, draft)
	return platform.PR{Number: 100 + len(c.created), Head: head, Draft: draft}, nil
}
func (c *fakePC) UpdatePR(_ context.Context, n int, _, body string, draft bool) error {
	if err := fail(c.fail, "UpdatePR", ""); err != nil {
		return err
	}
	c.updateDrafts = append(c.updateDrafts, draft)
	if c.prBodies == nil {
		c.prBodies = map[int]string{}
	}
	c.prBodies[n] = body
	for _, p := range c.open {
		if p.Number == n && body != p.Body && withInput(p.Body, bodyInput(body)) == body {
			c.recorded = append(c.recorded, n)
			return nil
		}
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
func (c *fakePC) SetPRMetadata(_ context.Context, n int, m platform.PRMetadata) error {
	if c.metadata == nil {
		c.metadata = map[int]platform.PRMetadata{}
	}
	c.metadata[n] = m
	return fail(c.fail, "SetPRMetadata", strconv.Itoa(n))
}
func (c *fakePC) RemovePRLabel(_ context.Context, n int, label string) error {
	if err := fail(c.fail, "RemovePRLabel", ""); err != nil {
		return err
	}
	c.unlabeled = append(c.unlabeled, fmt.Sprintf("%d:%s", n, label))
	return nil
}
func (c *fakePC) EnableAutoMerge(_ context.Context, pr platform.PR, strategy string) error {
	c.automerge = append(c.automerge, fmt.Sprintf("%d:%s@%s", pr.Number, strategy, pr.HeadSHA))
	return fail(c.fail, "EnableAutoMerge", strconv.Itoa(pr.Number))
}
func (c *fakePC) Checks(_ context.Context, sha string) (platform.CheckState, error) {
	c.checked = append(c.checked, sha)
	return c.checks, fail(c.fail, "Checks", "")
}
func (c *fakePC) MergePR(_ context.Context, pr platform.PR, strategy string) error {
	if err := fail(c.fail, "MergePR", ""); err != nil {
		return err
	}
	c.merged = append(c.merged, fmt.Sprintf("%d:%s@%s", pr.Number, strategy, pr.HeadSHA))
	return nil
}

// fakeIssuesPerPage is the page size the fake pages c.issues in, like the forges.
const fakeIssuesPerPage = 100

func (c *fakePC) ListIssues(_ context.Context, maxPages int, f platform.IssueFilter) ([]platform.Issue, error) {
	fakeListMu.Lock()
	c.issuePages, c.issueOldest = maxPages, f.Oldest
	fakeListMu.Unlock()
	if err := fail(c.fail, "ListIssues", ""); err != nil {
		return nil, err
	}
	// The forges list the newest issue first unless the filter asks for the oldest.
	is := slices.Clone(c.issues)
	slices.SortFunc(is, func(a, b platform.Issue) int {
		if f.Oldest {
			return cmp.Compare(a.Number, b.Number)
		}
		return cmp.Compare(b.Number, a.Number)
	})
	var out []platform.Issue
	for page := 0; page < maxPages && page*fakeIssuesPerPage < len(is); page++ {
		p := is[page*fakeIssuesPerPage : min((page+1)*fakeIssuesPerPage, len(is))]
		out = append(out, p...)
		if f.Stop != nil && slices.ContainsFunc(p, f.Stop) {
			break
		}
	}
	return out, nil
}
func (c *fakePC) CreateIssue(_ context.Context, title, body string, labels []string) (platform.Issue, error) {
	if err := fail(c.fail, "CreateIssue", ""); err != nil {
		return platform.Issue{}, err
	}
	c.createdIssues = append(c.createdIssues, platform.Issue{Title: title, Body: body})
	c.issueLabels = append(c.issueLabels, labels)
	return platform.Issue{Number: 500, Title: title, Body: body, URL: "https://forge/issues/500"}, nil
}
func (c *fakePC) UpdateIssue(_ context.Context, n int, body string) error {
	if err := fail(c.fail, "UpdateIssue", ""); err != nil {
		return err
	}
	c.updatedIssues = append(c.updatedIssues, platform.Issue{Number: n, Body: body})
	return nil
}

const (
	branchA = "deps/a-x"
	titleA  = "chore(deps): update a/x to v2"
	branchB = "deps/b-y"
	email   = "bot@example.invalid"
)

func fixture(t testing.TB) (string, *engine.Result) {
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

// A file the update deletes must not hash like one it empties, and a mode or submodule
// change is a change of the branch too.
func TestFingerprintMarkers(t *testing.T) {
	seen := map[string]string{}
	for name, changes := range map[string][]engine.FileChange{
		"content":    {{Path: "f", Mode: 0o644, New: []byte("x")}},
		"empty":      {{Path: "f", Mode: 0o644, New: []byte{}}},
		"deleted":    {{Path: "f", Mode: 0o644}},
		"executable": {{Path: "f", Mode: 0o755, New: []byte("x")}},
		"gitlink":    {{Path: "f", Mode: 0o644, New: []byte("x"), Gitlink: true}},
	} {
		fp := fingerprint(changes)
		if other, ok := seen[fp]; ok {
			t.Errorf("%s and %s share the fingerprint %s", name, other, fp)
		}
		seen[fp] = name
	}
}

func TestRun(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name        string
		opts        Options
		base        string // Base.Name; "" = the default branch
		git         fakeGit
		pc          fakePC
		engineErr   bool
		limitedErr  bool         // a rate-limited lookup (engine.RateLimited) in the engine errors
		extra       []dep.Update // appended to the fixture's updates
		wantStatus  map[string]string
		wantErrs    []string // substrings, one per returned error, in order
		wantPushed  int
		wantCreated int
		wantUpdated int
		wantClosed  int
		wantDeleted int
		wantGetPR   int
		wantTips    int // BranchTip fetches
		// UpdatePR calls that only record the input fingerprint in an up-to-date PR's body
		// (FP_A bodies have none).
		wantRecorded int
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
			wantRecorded: 1,
		},
		{
			name:       "fingerprint match but conflicting is rebuilt",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &no}}},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantUpdated: 1, wantTips: 1,
		},
		{
			name:       "fingerprint change updates",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Body: "<!-- deps:fingerprint=0000000000000000 -->"}}},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantUpdated: 1, wantTips: 1,
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
			wantPushed: 1, wantCreated: 1, wantTips: 1,
		},
		{
			name:       "ignored author on branch is updated",
			opts:       Options{IgnoredAuthors: []string{"other@example.com", "Human@Example.COM"}},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: "human@example.com"}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 2, wantTips: 1,
		},
		{
			name:       "non-ignored author still skipped",
			opts:       Options{IgnoredAuthors: []string{"other@example.com"}},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: "human@example.com"}},
			wantStatus: map[string]string{branchA: StatusSkipModified, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1, wantTips: 1,
		},
		{
			name:       "own email matched case-insensitively",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: strings.ToUpper(email)}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 2, wantTips: 1,
		},
		{
			name:       "concurrent limit counts open managed PRs",
			opts:       Options{ConcurrentLimit: 2},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}, {Number: 10, Head: "feature/x"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusSkipLimit, "deps/gomod/gone": StatusClosed},
			wantPushed: 1, wantCreated: 1, wantClosed: 1,
		},
		{
			name:       "concurrent limit reached still updates open PRs",
			opts:       Options{ConcurrentLimit: 1},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Body: "<!-- deps:fingerprint=0000000000000000 -->"}}},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusSkipLimit},
			wantPushed: 1, wantUpdated: 1, wantTips: 1,
		},
		{
			name: "hourly limit counts PRs created in the last hour",
			opts: Options{HourlyLimit: 2},
			pc: fakePC{closed: []platform.PR{
				{Number: 7, Head: "deps/gomod/old", Merged: true, CreatedAt: time.Now().Add(-10 * time.Minute)},
				{Number: 8, Head: "deps/gomod/older", Merged: true, CreatedAt: time.Now().Add(-2 * time.Hour)},
			}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusSkipLimit},
			wantPushed: 1, wantCreated: 1,
		},
		{
			name:       "smallest limit wins",
			opts:       Options{MaxNewPRs: 5, ConcurrentLimit: 10, HourlyLimit: 1},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusSkipLimit},
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
			git:        fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x"}, authors: map[string]string{"deps/gomod/gone": email}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}, {Number: 10, Head: "feature/x"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantCreated: 2, wantClosed: 1, wantDeleted: 1, wantTips: 1,
		},
		{
			// A human's commits on an obsolete branch survive the PR being closed.
			name:       "obsolete PR closed, foreign branch kept",
			git:        fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x"}, authors: map[string]string{"deps/gomod/gone": "dev@example.invalid"}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantPushed: 2, wantCreated: 2, wantClosed: 1, wantTips: 1,
		},
		{
			name:       "obsolete PR kept on engine error",
			engineErr:  true,
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 2,
		},
		{
			// A rate-limited lookup is only a warning for the commands, but its dep may still
			// own the PR: it is kept like on any lookup error.
			name:       "obsolete PR and branch kept on rate-limited lookup",
			limitedErr: true,
			git:        fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x"}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 2,
		},
		{
			name:       "unknown mergeability is looked up and treated as mergeable",
			base:       "main",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A"}}, fail: map[string]error{"DefaultBranch": errBoom}},
			wantStatus: map[string]string{branchA: StatusUpToDate, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1, wantGetPR: 1,
			wantRecorded: 1,
		},
		{
			name:       "looked up conflicting PR is rebuilt",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A"}}, mergeable: &no},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantUpdated: 1, wantGetPR: 1, wantTips: 1,
		},
		{
			name:       "GetPR failure isolated to its group",
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A"}}, fail: map[string]error{"GetPR": errBoom}},
			wantStatus: map[string]string{branchB: StatusCreated},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 1, wantCreated: 1,
		},
		{
			// The failed group's own PR stays open; PRs of no group are still closed.
			name:       "build error isolates its group and closes obsolete PRs",
			extra:      []dep.Update{{Dep: dep.Dep{Manager: "github-actions", File: "missing.yml", Line: 1, Name: "c/z", Version: "v1", RefSpan: dep.Span{Start: 0, End: 2}}, Type: dep.Major, NewVersion: "v2"}},
			pc:         fakePC{open: []platform.PR{{Number: 8, Head: "deps/c-z"}, {Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantErrs:   []string{"deps/c-z: missing.yml: "},
			wantPushed: 2, wantCreated: 2, wantClosed: 1,
		},
		{
			name:       "push failure isolated to its group and closes obsolete PRs",
			git:        fakeGit{fail: map[string]error{"Push:" + branchA: errBoom}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 1, wantCreated: 1, wantClosed: 1,
		},
		{
			name:       "commit failure",
			git:        fakeGit{fail: map[string]error{"Commit": errBoom}},
			wantStatus: map[string]string{},
			wantErrs:   []string{branchA + ": boom", branchB + ": boom"},
		},
		{
			name:       "branch author lookup failure",
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, fail: map[string]error{"BranchTip:" + branchA: errBoom}},
			wantStatus: map[string]string{branchB: StatusCreated},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 1, wantCreated: 1, wantTips: 1,
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
			wantPushed: 2, wantCreated: 1, wantTips: 1,
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
			git:        fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x"}, authors: map[string]string{"deps/gomod/gone": email}, fail: map[string]error{"DeleteBranch": errBoom}},
			pc:         fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}},
			wantStatus: map[string]string{branchA: StatusCreated, branchB: StatusCreated, "deps/gomod/gone": StatusClosed},
			wantErrs:   []string{"delete deps/gomod/gone: boom"},
			wantPushed: 2, wantCreated: 2, wantClosed: 1, wantTips: 1,
		},
		{
			name:       "behind base, same content, mergeable is rebased",
			opts:       Options{RebaseWhen: config.RebaseBehindBase},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}, parents: map[string][]string{branchA: {"old"}}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &yes}}},
			wantStatus: map[string]string{branchA: StatusRebased, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantTips: 1,
			wantRecorded: 1,
		},
		{
			name:       "behind base in conflicted mode is up to date without fetching",
			opts:       Options{RebaseWhen: config.RebaseConflicted},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}, parents: map[string][]string{branchA: {"old"}}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &yes}}},
			wantStatus: map[string]string{branchA: StatusUpToDate, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1,
			wantRecorded: 1,
		},
		{
			name:       "parent is base head is up to date",
			opts:       Options{RebaseWhen: config.RebaseBehindBase},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &yes}}},
			wantStatus: map[string]string{branchA: StatusUpToDate, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1, wantTips: 1,
			wantRecorded: 1,
		},
		{
			name:       "behind base with foreign author is skipped",
			opts:       Options{RebaseWhen: config.RebaseBehindBase},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: "human@example.com"}, parents: map[string][]string{branchA: {"old"}}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &yes}}},
			wantStatus: map[string]string{branchA: StatusSkipModified, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1, wantTips: 1,
		},
		{
			name:       "behind base with open PR but no branch is up to date",
			opts:       Options{RebaseWhen: config.RebaseBehindBase},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &yes}}},
			wantStatus: map[string]string{branchA: StatusUpToDate, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1,
			wantRecorded: 1,
		},
		{
			name:       "behind base tip fetch failure",
			opts:       Options{RebaseWhen: config.RebaseBehindBase},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, fail: map[string]error{"BranchTip:" + branchA: errBoom}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &yes}}},
			wantStatus: map[string]string{branchB: StatusCreated},
			wantErrs:   []string{branchA + ": boom"},
			wantPushed: 1, wantCreated: 1, wantTips: 1,
		},
		{
			name:       "conflicted PR in behind mode is rebuilt with one fetch",
			opts:       Options{RebaseWhen: config.RebaseBehindBase},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}, parents: map[string][]string{branchA: {"old"}}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &no}}},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantUpdated: 1, wantTips: 1,
		},
		{
			name:       "never mode leaves a conflicted PR alone",
			opts:       Options{RebaseWhen: config.RebaseNever},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}, parents: map[string][]string{branchA: {"old"}}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &no}}},
			wantStatus: map[string]string{branchA: StatusUpToDate, branchB: StatusCreated},
			wantPushed: 1, wantCreated: 1,
			wantRecorded: 1,
		},
		{
			name:       "never mode still updates changed content",
			opts:       Options{RebaseWhen: config.RebaseNever},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Body: "<!-- deps:fingerprint=0000000000000000 -->"}}},
			wantStatus: map[string]string{branchA: StatusUpdated, branchB: StatusCreated},
			wantPushed: 2, wantCreated: 1, wantUpdated: 1, wantTips: 1,
		},
		{
			name:       "dry run would rebase",
			opts:       Options{RebaseWhen: config.RebaseBehindBase, DryRun: true},
			git:        fakeGit{branches: map[string]string{"refs/heads/" + branchA: "x"}, authors: map[string]string{branchA: email}, parents: map[string][]string{branchA: {"old"}}},
			pc:         fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "FP_A", Mergeable: &yes}}},
			wantStatus: map[string]string{branchA: StatusWouldRebase, branchB: StatusWouldCreate},
			wantTips:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			if tt.engineErr {
				res.Errors = []error{errors.New("upstream down")}
			}
			if tt.limitedErr {
				res.Errors = []error{&engine.DepError{File: "go.mod", Line: 3, Name: "example.com/gone",
					Err: fmt.Errorf("HTTP 403: %w by api.github.com until 2026-11-10T14:33:49Z", httpx.ErrRateLimited)}}
				if !engine.RateLimited(res.Errors[0]) {
					t.Fatal("not a rate-limited lookup")
				}
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
			acts, errs := Run(t.Context(), o, []Base{{Name: tt.base, Root: root, Res: res, Git: &tt.git}}, nil, &tt.pc)
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
			if len(tt.pc.recorded) != tt.wantRecorded {
				t.Errorf("input fingerprints recorded = %v, want %d", tt.pc.recorded, tt.wantRecorded)
			}
			if len(tt.git.tips) != tt.wantTips {
				t.Errorf("BranchTip calls = %v, want %d", tt.git.tips, tt.wantTips)
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

// A run pushes only on the tip of the base; a dry run reports against an older checkout,
// because the base moves while a PR's CI job runs (#296). A missing base fails both.
func TestRunStaleCheckout(t *testing.T) {
	root, res := fixture(t)
	const tip = "3333333333333333333333333333333333333333"
	tests := []struct {
		name    string
		dryRun  bool
		tip     string
		wantErr string
	}{
		{"run", false, tip, "checkout HEAD 1111111 is not the tip of origin/main (3333333); check out the latest main"},
		{"dry run", true, tip, ""},
		{"dry run without base", true, "", "checkout HEAD 1111111 is not the tip of origin/main (); check out the latest main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &staleGit{tip: tt.tip}
			acts, errs := Run(t.Context(), Options{MaxNewPRs: 10, Managers: managers, DryRun: tt.dryRun}, []Base{{Name: "main", Root: root, Res: res, Git: g}}, nil, &fakePC{})
			if g.pushed != nil {
				t.Fatalf("pushed = %v", g.pushed)
			}
			if tt.wantErr != "" {
				if len(errs) != 1 || errs[0].Error() != tt.wantErr {
					t.Fatalf("errs = %v, want %q", errs, tt.wantErr)
				}
				return
			}
			if len(errs) != 0 || len(acts) == 0 || acts[0].Status != StatusWouldCreate {
				t.Fatalf("acts = %+v, errs = %v", acts, errs)
			}
		})
	}
}

func TestRunBranchPrefixAndUnlimitedPRs(t *testing.T) {
	root, res := fixture(t)
	g := &fakeGit{branches: map[string]string{"refs/heads/bot/gomod/gone": "x"}, authors: map[string]string{"bot/gomod/gone": email}}
	pc := &fakePC{open: []platform.PR{{Number: 9, Head: "bot/gomod/gone"}, {Number: 10, Head: "deps/gomod/foreign"}}}
	o := Options{BranchPrefix: "bot/", AuthorEmail: email, Managers: managers} // MaxNewPRs 0 = unlimited
	acts, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: g}}, nil, pc)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if want := []string{"refs/heads/main", "refs/heads/bot/*"}; !slices.Equal(g.patterns, want) {
		t.Errorf("LsRemote patterns = %v, want %v", g.patterns, want)
	}
	if want := []string{"bot/a-x", "bot/b-y"}; !slices.Equal(pc.created, want) {
		t.Errorf("created = %v, want %v", pc.created, want)
	}
	if !slices.Equal(pc.closedNow, []int{9}) || !slices.Equal(g.deletes, []string{"bot/gomod/gone@x"}) {
		t.Errorf("closed = %v deleted = %v; only PRs under the prefix are managed, with a lease on the known tip", pc.closedNow, g.deletes)
	}
	if len(acts) != 3 {
		t.Errorf("actions = %+v", acts)
	}
}

// Security updates of one securityGroup share a PR listing every advisory; a non-security
// update of the policy group with the same name gets its own PR.
func TestRunSecurityGroup(t *testing.T) {
	root := t.TempDir()
	src := "steps:\n  - uses: a/x@v1\n  - uses: b/y@v1\n  - uses: c/z@v1\n"
	if err := os.WriteFile(filepath.Join(root, "wf.yml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, line, start int, adv ...dep.Advisory) dep.Update {
		u := dep.Update{
			Dep:  dep.Dep{Manager: "github-actions", File: "wf.yml", Line: line, Name: name, Version: "v1", RefSpan: dep.Span{Start: start, End: start + 2}},
			Type: dep.Major, NewVersion: "v2", Advisories: adv,
		}
		if len(adv) == 0 {
			u.Group = "actions"
		} else {
			u.SecurityGroup = "actions"
		}
		return u
	}
	res := &engine.Result{Updates: []dep.Update{
		mk("a/x", 2, 21, dep.Advisory{ID: "GHSA-a", Summary: "rce"}),
		mk("b/y", 3, 38, dep.Advisory{ID: "GHSA-b", Summary: "dos"}),
		mk("c/z", 4, 55),
	}}
	pc := &fakePC{}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: &fakeGit{}}}, nil, pc)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if want := []string{"deps/group/actions", "deps/group/actions-security"}; !slices.Equal(pc.created, want) {
		t.Fatalf("created = %v, want %v", pc.created, want)
	}
	titles := map[string]string{}
	for _, a := range acts {
		titles[a.Branch] = a.Title
	}
	if got := titles["deps/group/actions-security"]; got != "fix(deps): update actions to v2 [security]" {
		t.Errorf("security title = %q", got)
	}
	if got := titles["deps/group/actions"]; got != "chore(deps): update c/z to v2" {
		t.Errorf("group title = %q", got)
	}
	plain, sec := pc.bodies[0], pc.bodies[1]
	for _, want := range []string{"### Security", "- GHSA-a: rce\n", "- GHSA-b: dos\n", "`wf.yml:2`", "`wf.yml:3`"} {
		if !strings.Contains(sec, want) {
			t.Errorf("security body missing %q:\n%s", want, sec)
		}
	}
	if strings.Contains(sec, "`wf.yml:4`") || strings.Contains(plain, "### Security") || !strings.Contains(plain, "`wf.yml:4`") {
		t.Errorf("non-security update mixed in:\nsecurity:\n%s\ngroup:\n%s", sec, plain)
	}
}

// inapplicable is a github-actions post-processor that finds the a/x update inapplicable
// (*dep.SkipError) and fails b/y when fail is set.
type inapplicable struct{ fail bool }

func (inapplicable) Scope(_ context.Context, _, manifest string) (string, bool) {
	return filepath.Dir(manifest), true
}
func (inapplicable) Siblings() []string { return nil }

func (p inapplicable) Process(_ context.Context, _ string, manifests, _ map[string][]byte) (map[string][]byte, error) {
	switch src := string(manifests["wf.yml"]); {
	case strings.Contains(src, "a/x@v2"):
		return nil, fmt.Errorf("wrapped: %w", &dep.SkipError{Reason: "module path changed to example.com/new (a/x@v2)"})
	case p.fail:
		return nil, errBoom
	}
	return nil, nil
}

// A group whose post-processor reports a *dep.SkipError is skipped, not an error: its deps
// join the skipped ones, the dashboard lists it, and obsolete PRs are still closed. Its open
// PR stays open. A real build error fails only its group: obsolete PRs are still closed.
func TestRunSkipsInapplicableUpdate(t *testing.T) {
	root, res := fixture(t)
	res.Deps = []dep.Dep{res.Updates[0].Dep, res.Updates[1].Dep}
	g := &fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x", "refs/heads/" + branchA: "y"}, authors: map[string]string{branchA: email}}
	pc := &fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}, {Number: 10, Head: branchA}}}
	post := map[string]engine.PostProcessor{"github-actions": inapplicable{}}
	o := Options{AuthorEmail: email, Managers: managers, Dashboard: &DashboardOptions{Title: "Dependency Dashboard"}}
	acts, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: g}}, post, pc)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	const reason = "module path changed to example.com/new (a/x@v2)"
	i := slices.IndexFunc(acts, func(a Action) bool { return a.Branch == branchA })
	if i < 0 || acts[i].Status != StatusSkipApply || acts[i].Reason != reason || acts[i].Title != titleA {
		t.Errorf("actions = %+v", acts)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Name != "a/x" || res.Skipped[0].Skip != reason || res.Deps[0].Skip != reason || res.Deps[1].Skip != "" {
		t.Errorf("skipped = %+v, deps = %+v", res.Skipped, res.Deps)
	}
	if !slices.Equal(pc.created, []string{branchB}) || !slices.Equal(pc.closedNow, []int{9}) || slices.Contains(g.pushed, branchA) {
		t.Errorf("created = %v, closed = %v, pushed = %v", pc.created, pc.closedNow, g.pushed)
	}
	if len(pc.createdIssues) != 1 || !strings.Contains(pc.createdIssues[0].Body, "- "+titleA+" (cannot be applied: "+reason+")") {
		t.Errorf("dashboard = %+v", pc.createdIssues)
	}

	root, res = fixture(t)
	g = &fakeGit{branches: map[string]string{"refs/heads/deps/gomod/gone": "x"}}
	pc = &fakePC{open: []platform.PR{{Number: 9, Head: "deps/gomod/gone"}}}
	post = map[string]engine.PostProcessor{"github-actions": inapplicable{fail: true}}
	if _, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: g}}, post, pc); len(errs) != 1 || !strings.Contains(errs[0].Error(), branchB+": wf.yml: boom") || !slices.Equal(pc.closedNow, []int{9}) {
		t.Errorf("errs = %v, closed = %v", errs, pc.closedNow)
	}
}

// taskGit is a fakeGit whose head is the real commit of a checkout, recording commits.
type taskGit struct {
	fakeGit
	head    string
	commits [][]engine.FileChange
}

func (g *taskGit) RevParse(context.Context, string) (string, error) { return g.head, nil }
func (g *taskGit) LsRemote(ctx context.Context, pattern string) (map[string]string, error) {
	if pattern == "refs/heads/main" {
		return map[string]string{pattern: g.head}, nil
	}
	return g.fakeGit.LsRemote(ctx, pattern)
}
func (g *taskGit) ObjectIDs(ctx context.Context, _ string, paths []string) ([]string, error) {
	return g.fakeGit.ObjectIDs(ctx, head, paths)
}
func (g *taskGit) Commit(ctx context.Context, parent string, changes []engine.FileChange, msg, name, email string) (string, error) {
	g.commits = append(g.commits, changes)
	return g.fakeGit.Commit(ctx, parent, changes, msg, name, email)
}

// gitCheckout makes root a git repository with one commit and returns its id.
func gitCheckout(t *testing.T, root string) string {
	t.Helper()
	writeFile := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(".gitignore", "ignored.txt\n")
	writeFile("old.txt", "old\n")
	var out []byte
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgSign=false", "commit", "-qm", "init"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "tag.gpgSign=false", "tag", "-a", "-m", "v1", "v1"}, {"rev-parse", "HEAD"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		var err error
		if out, err = cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return strings.TrimSpace(string(out))
}

// repoState describes what a run must not change in the repository at dir: its objects,
// refs, worktrees, status and index.
func repoState(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	for _, args := range []string{"count-objects -v", "for-each-ref", "worktree list --porcelain", "status --porcelain"} {
		cmd := exec.Command("git", strings.Fields(args)...)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v", args, err)
		}
		b.Write(out)
	}
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	b.Write(index)
	return b.String()
}

// postUpgradeTasks run on a copy of the checkout that is a repository of its own: git sees
// the base with the build's changes, and even commits stay in the copy. The files they add or
// delete join the commit, .gitignored files do not, the checkout's repository stays
// untouched, and a failing command fails its group only.
func TestRunPostUpgradeTasks(t *testing.T) {
	root, res := fixture(t)
	base := gitCheckout(t, root)
	before := repoState(t, root)
	res.Updates[0].Tasks.Commands = []string{
		`git status --porcelain | grep -qx ' M wf.yml' && test "$(git rev-parse HEAD)" = ` + base + ` && git describe --tags > described.txt`,
		"echo generated > gen.txt && echo tmp > ignored.txt && rm old.txt",
		"git -c user.name=t -c user.email=t@t -c commit.gpgSign=false commit -qam inside",
	}
	res.Updates[1].Tasks.Commands = []string{"echo broken >&2; exit 3"}
	g := &taskGit{head: base}
	pc := &fakePC{}
	_, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers, AllowTasks: engine.TaskAllowlist{"*"}}, []Base{{Root: root, Res: res, Git: g}}, nil, pc)
	if len(errs) != 1 || errs[0].Error() != branchB+": postUpgradeTasks: echo broken >&2; exit 3: exit status 3: broken" {
		t.Errorf("errs = %v", errs)
	}
	if !slices.Equal(pc.created, []string{branchA}) || len(g.commits) != 1 {
		t.Fatalf("created = %v, commits = %d", pc.created, len(g.commits))
	}
	if c := g.commits[0]; len(c) != 4 || c[0].Path != "described.txt" || string(c[0].New) != "v1\n" ||
		c[1].Path != "gen.txt" || string(c[1].New) != "generated\n" || c[1].Old != nil ||
		c[2].Path != "old.txt" || string(c[2].Old) != "old\n" || c[2].New != nil ||
		c[3].Path != "wf.yml" || !strings.Contains(string(c[3].New), "a/x@v2") || !strings.Contains(string(c[3].Old), "a/x@v1") {
		t.Errorf("commit = %+v", c)
	}
	if after := repoState(t, root); after != before {
		t.Error("the run changed the checkout's repository")
	}

	g = &taskGit{head: g.head}
	_, errs = Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: g}}, nil, &fakePC{})
	if len(errs) != 2 || !errors.Is(errs[0], engine.ErrTasksNotAllowed) || len(g.commits) != 0 {
		t.Errorf("without AllowTasks: errs = %v, commits = %d", errs, len(g.commits))
	}
}

// staleGit is a checkout whose HEAD is not tip, the remote main ("" = no main on origin).
type staleGit struct {
	fakeGit
	tip string
}

func (g *staleGit) LsRemote(_ context.Context, pattern string) (map[string]string, error) {
	if pattern != "refs/heads/main" || g.tip == "" {
		return map[string]string{}, nil
	}
	return map[string]string{"refs/heads/main": g.tip}, nil
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
		// The state reads run concurrently; the first failing one in this order wins.
		{name: "deps branches before closed PRs", gitFail: map[string]error{"LsRemote:refs/heads/deps/*": errBoom}, pcFail: map[string]error{"ListPRs:closed": errors.New("late")}, want: "boom"},
		{name: "open before closed PRs", pcFail: map[string]error{"ListPRs:open": errBoom, "ListPRs:closed": errors.New("late")}, want: "list open PRs: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := fixture(t)
			g := &fakeGit{fail: tt.gitFail}
			pc := &fakePC{fail: tt.pcFail}
			acts, errs := Run(t.Context(), Options{MaxNewPRs: 10, AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: g}}, nil, pc)
			if len(errs) != 1 || errs[0].Error() != tt.want || !errors.Is(errs[0], errBoom) {
				t.Fatalf("errs = %v, want [%s]", errs, tt.want)
			}
			if acts != nil || g.pushed != nil || pc.created != nil {
				t.Errorf("acted despite state error: actions=%v pushed=%v created=%v", acts, g.pushed, pc.created)
			}
		})
	}
}

// countProgress counts the PR phase.
type countProgress struct {
	mu                  sync.Mutex
	starts, total, ends int
	done, errs          int
}

func (c *countProgress) Start(p engine.Phase, total int) {
	if p == engine.PhasePRs {
		c.starts++
		c.total = total
	}
}

func (c *countProgress) Done(p engine.Phase, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p == engine.PhasePRs {
		c.done++
		if err != nil {
			c.errs++
		}
	}
}

func (c *countProgress) End(p engine.Phase) {
	if p == engine.PhasePRs {
		c.ends++
	}
}

func (*countProgress) RateLimited(string, time.Time) {}

// Every group counts once in the PR phase, a failed one as an error; skipped ones are done.
func TestRunProgress(t *testing.T) {
	root, res := fixture(t)
	p := &countProgress{}
	post := map[string]engine.PostProcessor{"github-actions": inapplicable{fail: true}}
	o := Options{AuthorEmail: email, Managers: managers, Progress: p}
	if _, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: &fakeGit{}}}, post, &fakePC{}); len(errs) != 1 {
		t.Fatalf("errs = %v", errs)
	}
	if p.starts != 1 || p.total != 2 || p.ends != 1 || p.done != 2 || p.errs != 1 {
		t.Errorf("starts=%d total=%d ends=%d done=%d errs=%d", p.starts, p.total, p.ends, p.done, p.errs)
	}
}

type branchKey struct{}

// ctxPost records which PR's context each Process call got, by the action it updates.
type ctxPost struct {
	mu  *sync.Mutex
	got map[string]any
}

func (ctxPost) Scope(_ context.Context, _, manifest string) (string, bool) {
	return filepath.Dir(manifest), true
}
func (ctxPost) Siblings() []string { return nil }

func (p ctxPost) Process(ctx context.Context, _ string, manifests, _ map[string][]byte) (map[string][]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, name := range []string{"a/x", "b/y"} {
		if strings.Contains(string(manifests["wf.yml"]), name+"@v2") {
			p.got[name] = ctx.Value(branchKey{})
		}
	}
	return nil, nil
}

// Each PR's build and handling run with the context of its PRLog, which is done once with
// the PR's status.
func TestRunPRLog(t *testing.T) {
	root, res := fixture(t)
	var mu sync.Mutex
	post := ctxPost{mu: &mu, got: map[string]any{}}
	begun, done := map[string]int{}, map[string]string{}
	o := Options{AuthorEmail: email, Managers: managers, PRLog: func(ctx context.Context, branch string) (context.Context, func(string, time.Duration)) {
		mu.Lock()
		defer mu.Unlock()
		begun[branch]++
		return context.WithValue(ctx, branchKey{}, branch), func(status string, d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			if d <= 0 {
				t.Errorf("%s: duration %v", branch, d)
			}
			done[branch] = status
		}
	}}
	g := &fakeGit{fail: map[string]error{"Push:" + branchB: errBoom}}
	if _, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: g}}, map[string]engine.PostProcessor{"github-actions": post}, &fakePC{}); len(errs) != 1 {
		t.Fatalf("errs = %v", errs)
	}
	if post.got["a/x"] != branchA || post.got["b/y"] != branchB {
		t.Errorf("Process contexts = %v", post.got)
	}
	if begun[branchA] != 1 || begun[branchB] != 1 || done[branchA] != StatusCreated || done[branchB] != "error" {
		t.Errorf("begun = %v, done = %v", begun, done)
	}
}

// barrierGetPC blocks each GetPR until want lookups are in flight (or a timeout), recording
// the most seen at once.
type barrierGetPC struct {
	fakePC
	want     int
	mu       sync.Mutex
	inFlight int
	most     int
	all      chan struct{}
}

func (c *barrierGetPC) GetPR(ctx context.Context, n int) (platform.PR, error) {
	c.mu.Lock()
	c.inFlight++
	c.most = max(c.most, c.inFlight)
	if c.inFlight == c.want {
		close(c.all)
	}
	c.mu.Unlock()
	select {
	case <-c.all:
	case <-time.After(time.Second):
	}
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	return c.fakePC.GetPR(ctx, n)
}

// TestRunLooksUpMergeableConcurrently checks that the mergeability of matched open PRs the
// listing left unknown is looked up concurrently, built or not, not one group after another.
func TestRunLooksUpMergeableConcurrently(t *testing.T) {
	yes := true
	root, res := fixture(t)
	fpA, fpB := fingerprintOf(t, root, res, "a/x"), fingerprintOf(t, root, res, "b/y")
	in := inputOf(t, root, res, "b/y", nil, &fakeGit{})
	if in == "" {
		t.Fatal("no input fingerprint")
	}
	pc := &barrierGetPC{want: 2, all: make(chan struct{}), mergeable: &yes, open: []platform.PR{
		{Number: 1, Head: branchA, Body: "x\n<!-- deps:fingerprint=" + fpA + " -->\n"},
		// b/y's input matches: not built, only looked up.
		{Number: 2, Head: branchB, Body: withInput("x\n<!-- deps:fingerprint="+fpB+" -->\n", in)},
	}}
	g := &fakeGit{
		branches: map[string]string{"refs/heads/" + branchA: "x", "refs/heads/" + branchB: "y"},
		authors:  map[string]string{branchA: email, branchB: email},
	}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: g}}, nil, pc)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, a := range acts {
		if a.Status != StatusUpToDate {
			t.Errorf("%s: status %s, want %s", a.Branch, a.Status, StatusUpToDate)
		}
	}
	if len(pc.got) != 2 || pc.most != 2 {
		t.Errorf("GetPR calls = %v, at most %d at once, want 2 concurrent", pc.got, pc.most)
	}
}
