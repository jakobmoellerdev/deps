package bot

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// Tips of the existing automerge branches in branchGit.
const tipA, tipB = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// branchGit has an up-to-date automerge branch for each of branches: authored by the bot, one
// commit on top of head, with the tree the bot builds now.
func branchGit(branches ...string) fakeGit {
	g := fakeGit{branches: map[string]string{}, authors: map[string]string{}, trees: map[string]string{}}
	for _, b := range branches {
		g.branches["refs/heads/"+b] = map[string]string{branchA: tipA, branchB: tipB}[b]
		g.authors[b] = email
		g.trees[b] = newTree
	}
	return g
}

// branchFixture returns the fixture with the a/x update (and with both, also b/y) automerging
// its branch with the squash strategy.
func branchFixture(t *testing.T, both bool) (string, *engine.Result) {
	t.Helper()
	root, res := fixture(t)
	if !both {
		res.Updates = res.Updates[:1]
	}
	for i := range res.Updates {
		u := &res.Updates[i]
		u.Automerge, u.AutomergeStrategy, u.AutomergeType = true, config.StrategySquash, config.AutomergeTypeBranch
	}
	return root, res
}

func TestRunBranchAutomerge(t *testing.T) {
	yes := true
	weekly, err := schedule.Parse([]string{"0 0 * * 0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	outWindow := schedule.Trigger{Event: schedule.EventSchedule, Cron: "0 0 * * *"}
	limited, limitOpts := rateLimited()
	limitOpts.Now = dashNow
	tests := []struct {
		name      string
		opts      Options
		both      bool // b/y automerges its branch too
		weekly    bool // a/x is scheduled weekly
		git       fakeGit
		pc        fakePC
		openPR    bool // an open PR for branchA with the current fingerprint
		want      map[string]string
		wantErr   string
		wantPush  []string // "<commit>:<branch>@<expected>"
		wantDel   []string
		wantPRs   []string // created PR heads
		wantBody  string   // in the created PR's body
		wantCheck []string
		wantAuto  []string // EnableAutoMerge calls
	}{
		{
			name:     "a new branch is pushed without a PR",
			want:     map[string]string{branchA: "created, " + AutomergeBranchWaiting},
			wantPush: []string{pushed + ":" + branchA + "@"},
		},
		{
			name:     "new branches count toward the PR limits",
			opts:     Options{MaxNewPRs: 1},
			both:     true,
			want:     map[string]string{branchA: "created, " + AutomergeBranchWaiting, branchB: StatusSkipLimit},
			wantPush: []string{pushed + ":" + branchA + "@"},
		},
		{
			name:      "pending checks wait",
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksPending},
			want:      map[string]string{branchA: "up-to-date, " + AutomergeBranchWaiting},
			wantCheck: []string{tipA},
		},
		{
			name:      "a branch without any checks waits",
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksNone},
			want:      map[string]string{branchA: "up-to-date, " + AutomergeBranchWaiting},
			wantCheck: []string{tipA},
		},
		{
			name:      "green checks fast-forward the base and delete the branch",
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      map[string]string{branchA: "up-to-date, " + AutomergeMerged},
			wantPush:  []string{tipA + ":main@" + head},
			wantDel:   []string{branchA},
			wantCheck: []string{tipA},
		},
		{
			name:      "only one branch is merged per run",
			both:      true,
			git:       branchGit(branchA, branchB),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      map[string]string{branchA: "up-to-date, " + AutomergeMerged, branchB: "up-to-date, " + AutomergeBranchNextRun},
			wantPush:  []string{tipA + ":main@" + head},
			wantDel:   []string{branchA},
			wantCheck: []string{tipA, tipB},
		},
		{
			name:      "failed checks raise a PR with platform auto-merge",
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksFailure},
			want:      map[string]string{branchA: "created, " + AutomergeRequested},
			wantPRs:   []string{branchA},
			wantBody:  "(`automergeType: branch`), but its checks failed, so deps opened this PR.",
			wantCheck: []string{tipA},
			wantAuto:  []string{"101:squash@" + tipA},
		},
		{
			name:      "failed checks wait for the PR limits",
			opts:      limitOpts,
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksFailure, closed: limited},
			want:      map[string]string{branchA: StatusSkipLimit},
			wantCheck: []string{tipA},
		},
		{
			name: "a branch behind the base is rebuilt",
			git: func() fakeGit {
				g := branchGit(branchA)
				g.parents = map[string][]string{branchA: {"0000000000000000000000000000000000000000"}}
				return g
			}(),
			pc:       fakePC{checks: platform.ChecksSuccess},
			want:     map[string]string{branchA: "updated, " + AutomergeBranchWaiting},
			wantPush: []string{pushed + ":" + branchA + "@" + tipA},
		},
		{
			name: "a branch with other content is rebuilt",
			git: func() fakeGit {
				g := branchGit(branchA)
				g.trees[branchA] = "6666666666666666666666666666666666666666"
				return g
			}(),
			pc:       fakePC{checks: platform.ChecksSuccess},
			want:     map[string]string{branchA: "updated, " + AutomergeBranchWaiting},
			wantPush: []string{pushed + ":" + branchA + "@" + tipA},
		},
		{
			name: "a base that moved during the run is merged next run",
			git: func() fakeGit {
				g := branchGit(branchA)
				g.fail = map[string]error{"Push:main": fmt.Errorf("%w: [rejected] (stale info): exit status 1", gitx.ErrStale)}
				return g
			}(),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      map[string]string{branchA: "up-to-date, " + AutomergeBranchNextRun},
			wantCheck: []string{tipA},
		},
		{
			name: "a base that declines the push raises a PR",
			git: func() fakeGit {
				g := branchGit(branchA)
				g.fail = map[string]error{"Push:main": fmt.Errorf("%w: [remote rejected] (protected branch hook declined): exit status 1", gitx.ErrRejected)}
				return g
			}(),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      map[string]string{branchA: "created, " + AutomergeRequested},
			wantPRs:   []string{branchA},
			wantBody:  "but `main` declined the push, so deps opened this PR.",
			wantCheck: []string{tipA},
			wantAuto:  []string{"101:squash@" + tipA},
		},
		{
			name: "other push errors are reported",
			git: func() fakeGit {
				g := branchGit(branchA)
				g.fail = map[string]error{"Push:main": errBoom}
				return g
			}(),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      map[string]string{},
			wantErr:   branchA + ": boom",
			wantCheck: []string{tipA},
		},
		{
			name:      "check errors are reported",
			git:       branchGit(branchA),
			pc:        fakePC{fail: map[string]error{"Checks": errBoom}},
			want:      map[string]string{},
			wantErr:   branchA + ": boom",
			wantCheck: []string{tipA},
		},
		{
			name: "a merged branch that cannot be deleted is no error",
			git: func() fakeGit {
				g := branchGit(branchA)
				g.fail = map[string]error{"DeleteBranch:" + branchA: errBoom}
				return g
			}(),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      map[string]string{branchA: "up-to-date, " + AutomergeMerged},
			wantPush:  []string{tipA + ":main@" + head},
			wantCheck: []string{tipA},
		},
		{
			name: "a branch someone else committed to is left alone",
			git: func() fakeGit {
				g := branchGit(branchA)
				g.authors[branchA] = "dev@example.invalid"
				return g
			}(),
			pc:   fakePC{checks: platform.ChecksSuccess},
			want: map[string]string{branchA: StatusSkipModified},
		},
		{
			name:     "an open PR is automerged as a PR",
			git:      branchGit(branchA),
			pc:       fakePC{checks: platform.ChecksSuccess},
			openPR:   true,
			want:     map[string]string{branchA: "up-to-date, " + AutomergeRequested},
			wantAuto: []string{"1:squash@" + tipA},
		},
		{
			name:      "dry run with green checks",
			opts:      Options{DryRun: true},
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      map[string]string{branchA: "up-to-date, " + AutomergeWould},
			wantCheck: []string{tipA},
		},
		{
			name:      "dry run with failed checks",
			opts:      Options{DryRun: true},
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksFailure},
			want:      map[string]string{branchA: StatusWouldCreate + ", " + AutomergeWould},
			wantCheck: []string{tipA},
		},
		{
			name: "dry run of a new branch",
			opts: Options{DryRun: true},
			want: map[string]string{branchA: StatusWouldCreate + ", " + AutomergeWould},
		},
		{
			name:      "outside its schedule an existing branch is still merged",
			opts:      Options{Trigger: outWindow, UpdateNotScheduled: true},
			weekly:    true,
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      map[string]string{branchA: "up-to-date, " + AutomergeMerged},
			wantPush:  []string{tipA + ":main@" + head},
			wantDel:   []string{branchA},
			wantCheck: []string{tipA},
		},
		{
			name:   "outside its schedule a new branch waits",
			opts:   Options{Trigger: outWindow, UpdateNotScheduled: true},
			weekly: true,
			want:   map[string]string{branchA: StatusSkipSchedule},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := branchFixture(t, tt.both)
			if tt.weekly {
				res.Updates[0].Schedule = weekly
			}
			if tt.openPR {
				tt.pc.open = []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "<!-- deps:fingerprint=" + fingerprintOf(t, root, res, "a/x") + " -->", HeadSHA: tipA, Mergeable: &yes}}
			}
			o := tt.opts
			o.AuthorEmail, o.Managers = email, managers
			acts, errs := Run(t.Context(), o, root, res, nil, &tt.pc, &tt.git)
			if tt.wantErr == "" && len(errs) > 0 || tt.wantErr != "" && (len(errs) != 1 || errs[0].Error() != tt.wantErr) {
				t.Fatalf("errs = %v, want %q", errs, tt.wantErr)
			}
			got := map[string]string{}
			for _, a := range acts {
				got[a.Branch] = a.Display()
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("actions = %v, want %v", got, tt.want)
			}
			for name, pair := range map[string][2][]string{
				"pushes":          {tt.git.pushes, tt.wantPush},
				"deleted":         {tt.git.deleted, tt.wantDel},
				"created PRs":     {tt.pc.created, tt.wantPRs},
				"checked":         {tt.pc.checked, tt.wantCheck},
				"EnableAutoMerge": {tt.pc.automerge, tt.wantAuto},
			} {
				if !slices.Equal(pair[0], pair[1]) {
					t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
				}
			}
			if tt.wantBody != "" && (len(tt.pc.bodies) != 1 || !strings.Contains(tt.pc.bodies[0], tt.wantBody) || !strings.Contains(tt.pc.bodies[0], "<!-- deps:fingerprint=")) {
				t.Errorf("PR body lacks %q: %q", tt.wantBody, tt.pc.bodies)
			}
		})
	}
}

func TestRunDeletesObsoleteBranches(t *testing.T) {
	const gone, foreign, closedBr = "deps/github-actions/gone", "deps/github-actions/foreign", "deps/github-actions/closed"
	root, res := branchFixture(t, false)
	newGit := func() *fakeGit {
		g := branchGit(branchA)
		for _, b := range []string{gone, foreign, closedBr} {
			g.branches["refs/heads/"+b] = "cccccccccccccccccccccccccccccccccccccccc"
			g.authors[b] = email
		}
		g.authors[foreign] = "dev@example.invalid"
		return &g
	}
	pc := &fakePC{checks: platform.ChecksPending, closed: []platform.PR{{Number: 3, Head: closedBr, Title: "chore(deps): update c"}}}
	g := newGit()
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, root, res, nil, pc, g)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !slices.Equal(g.deleted, []string{gone}) || !slices.ContainsFunc(acts, func(a Action) bool { return a.Branch == gone && a.Status == StatusDeleted }) {
		t.Errorf("deleted %v, actions %+v", g.deleted, acts)
	}

	// With errors nothing is deleted, as no PR is closed.
	g = newGit()
	res.Errors = []error{errBoom}
	if _, _ = Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, root, res, nil, pc, g); len(g.deleted) != 0 {
		t.Errorf("deleted %v despite errors", g.deleted)
	}
}

func TestDashboardBranchAutomerge(t *testing.T) {
	root, res := branchFixture(t, false)
	pc := &fakePC{checks: platform.ChecksPending}
	g := branchGit(branchA)
	if _, errs, _ := dashRun(t, Options{}, res, root, pc, &g); len(errs) > 0 {
		t.Fatal(errs)
	}
	body := pc.createdIssues[0].Body
	if want := "## Pending branch automerge\n\nThese branches are merged without a PR once their checks pass. Tick one to create its PR instead.\n\n" +
		"- [ ] <!-- deps:pr=" + branchA + " --> " + titleA + " (`" + branchA + "`)\n"; !strings.Contains(body, want) {
		t.Fatalf("body lacks %q:\n%s", want, body)
	}

	// Ticked: the PR is opened for the rebuilt branch, and automerged as a PR.
	ticked := strings.ReplaceAll(body, "- [ ] <!-- deps:pr=", "- [x] <!-- deps:pr=")
	pc = &fakePC{checks: platform.ChecksPending, issues: []platform.Issue{{Number: 7, Title: dashTitle, Body: ticked}}}
	g = branchGit(branchA)
	acts, errs, _ := dashRun(t, Options{}, res, root, pc, &g)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	i := slices.IndexFunc(acts, func(a Action) bool { return a.Branch == branchA })
	if i < 0 || acts[i].Display() != "created, "+AutomergeRequested || !slices.Equal(pc.created, []string{branchA}) ||
		!slices.Equal(g.pushes, []string{pushed + ":" + branchA + "@" + tipA}) || len(pc.checked) != 0 {
		t.Errorf("actions %+v, created %v, pushes %v, checked %v", acts, pc.created, g.pushes, pc.checked)
	}
	if body := pc.updatedIssues[0].Body; strings.Contains(body, "Pending branch automerge") || !strings.Contains(body, "- [ ] <!-- deps:rebase="+branchA+" -->") {
		t.Errorf("body after the PR was opened:\n%s", body)
	}
}
