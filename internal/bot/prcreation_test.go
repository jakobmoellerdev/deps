package bot

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// waitFixture returns the fixture with only the a/x update, whose PR waits with mode.
func waitFixture(t *testing.T, mode string) (string, *engine.Result) {
	t.Helper()
	root, res := fixture(t)
	res.Updates = res.Updates[:1]
	res.Updates[0].PRCreation = mode
	return root, res
}

// behindGit has branchA one commit on an older base, with other files than head with the
// changes applied (newTree); wf.yml is tipBlob in the tip and newBlob in newTree.
func behindGit(tipBlob, newBlob string) fakeGit {
	g := branchGit(branchA)
	g.parents = map[string][]string{branchA: {"0000000000000000000000000000000000000000"}}
	g.trees[branchA] = "8888888888888888888888888888888888888888"
	g.revObjects = map[string]map[string]string{
		tipA:    {".": g.trees[branchA], "wf.yml": tipBlob},
		newTree: {"wf.yml": newBlob},
	}
	return g
}

func TestRunPRCreation(t *testing.T) {
	weekly, err := schedule.Parse([]string{"0 0 * * 0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	outWindow := schedule.Trigger{Event: schedule.EventSchedule, Cron: "0 0 * * *"}
	limited, limitOpts := rateLimited()
	limitOpts.Now = dashNow
	tests := []struct {
		name      string
		mode      string // prCreation of a/x; "" = status-success
		opts      Options
		weekly    bool // a/x is scheduled weekly
		git       fakeGit
		pc        fakePC
		want      string // Display of branchA's action
		wantPush  []string
		wantPRs   []string
		wantCheck []string
	}{
		{
			name:     "a new branch is pushed without a PR",
			want:     "created, " + PRWaitChecks,
			wantPush: []string{pushed + ":" + branchA + "@"},
		},
		{
			name:      "running checks wait",
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksPending},
			want:      "up-to-date, " + PRWaitChecks,
			wantCheck: []string{tipA},
		},
		{
			name:      "no checks wait",
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksNone},
			want:      "up-to-date, " + PRWaitChecks,
			wantCheck: []string{tipA},
		},
		{
			name:      "green checks open the PR",
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      StatusCreated,
			wantPRs:   []string{branchA},
			wantCheck: []string{tipA},
		},
		{
			name:      "status-success waits on failed checks",
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksFailure},
			want:      "up-to-date, " + PRWaitFailed,
			wantCheck: []string{tipA},
		},
		{
			name:      "not-pending opens the PR on failed checks",
			mode:      config.PRCreationNotPending,
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksFailure},
			want:      StatusCreated,
			wantPRs:   []string{branchA},
			wantCheck: []string{tipA},
		},
		{
			name:      "not-pending waits on running checks",
			mode:      config.PRCreationNotPending,
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksPending},
			want:      "up-to-date, " + PRWaitChecks,
			wantCheck: []string{tipA},
		},
		{
			name:      "not-pending opens the PR on running checks after prNotPendingHours",
			mode:      config.PRCreationNotPending,
			opts:      Options{Now: dashNow, NotPendingHours: 25},
			git:       datedGit(dashNow.Add(-25 * time.Hour)),
			pc:        fakePC{checks: platform.ChecksPending},
			want:      StatusCreated,
			wantPRs:   []string{branchA},
			wantCheck: []string{tipA},
		},
		{
			name:      "not-pending opens the PR without checks after prNotPendingHours",
			mode:      config.PRCreationNotPending,
			opts:      Options{Now: dashNow, NotPendingHours: 25},
			git:       datedGit(dashNow.Add(-26 * time.Hour)),
			pc:        fakePC{checks: platform.ChecksNone},
			want:      StatusCreated,
			wantPRs:   []string{branchA},
			wantCheck: []string{tipA},
		},
		{
			name:      "not-pending waits within prNotPendingHours",
			mode:      config.PRCreationNotPending,
			opts:      Options{Now: dashNow, NotPendingHours: 25},
			git:       datedGit(dashNow.Add(-24 * time.Hour)),
			pc:        fakePC{checks: platform.ChecksPending},
			want:      "up-to-date, " + PRWaitChecks,
			wantCheck: []string{tipA},
		},
		{
			name:      "status-success ignores prNotPendingHours",
			opts:      Options{Now: dashNow, NotPendingHours: 1},
			git:       datedGit(dashNow.Add(-48 * time.Hour)),
			pc:        fakePC{checks: platform.ChecksPending},
			want:      "up-to-date, " + PRWaitChecks,
			wantCheck: []string{tipA},
		},
		{
			name:     "approval pushes a new branch without a PR",
			mode:     config.PRCreationApproval,
			want:     "created, " + PRWaitApproval,
			wantPush: []string{pushed + ":" + branchA + "@"},
		},
		{
			name: "approval waits whatever the checks",
			mode: config.PRCreationApproval,
			git:  branchGit(branchA),
			pc:   fakePC{checks: platform.ChecksSuccess},
			want: "up-to-date, " + PRWaitApproval,
		},
		{
			name:     "immediate opens the PR with the push",
			mode:     config.PRCreationImmediate,
			want:     StatusCreated,
			wantPush: []string{pushed + ":" + branchA + "@"},
			wantPRs:  []string{branchA},
		},
		{
			name:      "a branch behind base with the same changes is kept",
			git:       behindGit("blob1", "blob1"),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      StatusCreated,
			wantPRs:   []string{branchA},
			wantCheck: []string{tipA},
		},
		{
			name:     "a branch with other changes is rebuilt",
			git:      behindGit("blob0", "blob1"),
			pc:       fakePC{checks: platform.ChecksSuccess},
			want:     "updated, " + PRWaitChecks,
			wantPush: []string{pushed + ":" + branchA + "@" + tipA},
		},
		{
			name:     "a tip that is not local is rebuilt",
			git:      behindGit("", "blob1"),
			pc:       fakePC{checks: platform.ChecksSuccess},
			want:     "updated, " + PRWaitChecks,
			wantPush: []string{pushed + ":" + branchA + "@" + tipA},
		},
		{
			name:     "behind-base-branch rebuilds a branch behind base",
			opts:     Options{RebaseWhen: config.RebaseBehindBase},
			git:      behindGit("blob1", "blob1"),
			pc:       fakePC{checks: platform.ChecksSuccess},
			want:     "updated, " + PRWaitChecks,
			wantPush: []string{pushed + ":" + branchA + "@" + tipA},
		},
		{
			name:      "green checks within the PR limit",
			opts:      limitOpts,
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksSuccess, closed: limited},
			want:      StatusSkipLimit,
			wantCheck: []string{tipA},
		},
		{
			name: "a new branch within the PR limit",
			opts: limitOpts,
			pc:   fakePC{closed: limited},
			want: StatusSkipLimit,
		},
		{
			name: "a branch someone else committed to is left alone",
			git: func() fakeGit {
				g := branchGit(branchA)
				g.authors[branchA] = "dev@example.invalid"
				return g
			}(),
			pc:   fakePC{checks: platform.ChecksSuccess},
			want: StatusSkipModified,
		},
		{
			name: "dry run of a new branch",
			opts: Options{DryRun: true},
			want: StatusWouldCreate + ", " + PRWaitChecks,
		},
		{
			name:      "dry run with green checks",
			opts:      Options{DryRun: true},
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      StatusWouldCreate,
			wantCheck: []string{tipA},
		},
		{
			name:      "outside its schedule an existing branch still gets its PR",
			opts:      Options{Trigger: outWindow, UpdateNotScheduled: true},
			weekly:    true,
			git:       branchGit(branchA),
			pc:        fakePC{checks: platform.ChecksSuccess},
			want:      StatusCreated,
			wantPRs:   []string{branchA},
			wantCheck: []string{tipA},
		},
		{
			name:   "outside its schedule a new branch waits",
			opts:   Options{Trigger: outWindow, UpdateNotScheduled: true},
			weekly: true,
			want:   StatusSkipSchedule,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, res := waitFixture(t, cmp.Or(tt.mode, config.PRCreationStatusSuccess))
			if tt.weekly {
				res.Updates[0].Schedule = weekly
			}
			o := tt.opts
			o.AuthorEmail, o.Managers = email, managers
			acts, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: &tt.git}}, nil, &tt.pc)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			i := slices.IndexFunc(acts, func(a Action) bool { return a.Branch == branchA })
			if i < 0 || acts[i].Display() != tt.want {
				t.Errorf("actions = %+v, want %s %q", acts, branchA, tt.want)
			}
			for name, pair := range map[string][2][]string{
				"pushes":      {tt.git.pushes, tt.wantPush},
				"created PRs": {tt.pc.created, tt.wantPRs},
				"checked":     {tt.pc.checked, tt.wantCheck},
			} {
				if !slices.Equal(pair[0], pair[1]) {
					t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
				}
			}
			if len(tt.pc.bodies) > 0 && (strings.Contains(tt.pc.bodies[0], "automergeType") || !strings.Contains(tt.pc.bodies[0], "<!-- deps:fingerprint=")) {
				t.Errorf("PR body %q", tt.pc.bodies[0])
			}
		})
	}
}

// A green waiting branch of an automerge group gets its PR, then platform auto-merge.
func TestRunPRCreationAutomerge(t *testing.T) {
	root, res := waitFixture(t, config.PRCreationStatusSuccess)
	res.Updates[0].Automerge, res.Updates[0].AutomergeStrategy, res.Updates[0].DraftPR = true, config.StrategySquash, true
	pc := &fakePC{checks: platform.ChecksSuccess}
	g := branchGit(branchA)
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: &g}}, nil, pc)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(acts) != 1 || acts[0].Display() != "created, "+AutomergeRequested || !slices.Equal(pc.automerge, []string{"101:squash@" + tipA}) {
		t.Errorf("actions %+v, automerge %v", acts, pc.automerge)
	}
	// Forges do not merge drafts, so an automerge PR is never one.
	if !slices.Equal(pc.drafts, []bool{false}) {
		t.Errorf("drafts = %v", pc.drafts)
	}
}

func TestRunDraftPR(t *testing.T) {
	root, res := fixture(t)
	res.Updates[0].DraftPR = true
	pc := &fakePC{}
	g := fakeGit{}
	if _, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: &g}}, nil, pc); len(errs) > 0 {
		t.Fatal(errs)
	}
	if !slices.Equal(pc.created, []string{branchA, branchB}) || !slices.Equal(pc.drafts, []bool{true, false}) {
		t.Errorf("created %v, drafts %v", pc.created, pc.drafts)
	}

	// An update keeps a draft a draft and a ready PR ready.
	for _, draft := range []bool{true, false} {
		pc := &fakePC{open: []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "<!-- deps:fingerprint=old -->", Draft: draft}}}
		g := fakeGit{branches: map[string]string{"refs/heads/" + branchA: tipA}, authors: map[string]string{branchA: email}}
		if _, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: &g}}, nil, pc); len(errs) > 0 {
			t.Fatal(errs)
		}
		if !slices.Equal(pc.updated, []int{1}) || !slices.Equal(pc.updateDrafts, []bool{draft}) {
			t.Errorf("draft %v: updated %v, drafts %v", draft, pc.updated, pc.updateDrafts)
		}
	}
}

func TestDashboardPendingStatusChecks(t *testing.T) {
	root, res := waitFixture(t, config.PRCreationStatusSuccess)
	for _, tc := range []struct {
		checks platform.CheckState
		note   string
	}{{platform.ChecksPending, ""}, {platform.ChecksFailure, ", checks failed"}} {
		pc := &fakePC{checks: tc.checks}
		g := branchGit(branchA)
		if _, errs, _ := dashRun(t, Options{}, res, root, pc, &g); len(errs) > 0 {
			t.Fatal(errs)
		}
		body := pc.createdIssues[0].Body
		if want := "## Pending status checks\n\nThese branches get their PR once their checks finish or pass (`prCreation`). Tick one to create its PR now.\n\n" +
			"- [ ] <!-- deps:pr=" + branchA + " --> " + titleA + " (`" + branchA + "`" + tc.note + ")\n"; !strings.Contains(body, want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}

	// Ticked: the PR is opened on the unchanged branch without waiting for its checks or
	// pushing a new commit that would restart them.
	pc := &fakePC{checks: platform.ChecksPending}
	g := branchGit(branchA)
	if _, errs, _ := dashRun(t, Options{}, res, root, pc, &g); len(errs) > 0 {
		t.Fatal(errs)
	}
	ticked := strings.ReplaceAll(pc.createdIssues[0].Body, "- [ ] <!-- deps:pr=", "- [x] <!-- deps:pr=")
	pc = &fakePC{checks: platform.ChecksPending, issues: []platform.Issue{{Number: 7, Title: dashTitle, Body: ticked}}}
	g = branchGit(branchA)
	acts, errs, _ := dashRun(t, Options{}, res, root, pc, &g)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	i := slices.IndexFunc(acts, func(a Action) bool { return a.Branch == branchA })
	if i < 0 || acts[i].Display() != StatusCreated || !slices.Equal(pc.created, []string{branchA}) || len(pc.checked) != 0 || len(g.pushes) != 0 {
		t.Errorf("actions %+v, created %v, checked %v, pushes %v", acts, pc.created, pc.checked, g.pushes)
	}
	if body := pc.updatedIssues[0].Body; strings.Contains(body, "Pending status checks") {
		t.Errorf("body after the PR was opened:\n%s", body)
	}
}

// datedGit has branchA up to date, committed at date.
func datedGit(date time.Time) fakeGit {
	g := branchGit(branchA)
	g.dates = map[string]time.Time{branchA: date}
	return g
}

// prCreation approval lists the pushed branch under Pending approval; ticking it opens the PR
// on the unchanged branch.
func TestDashboardPRCreationApproval(t *testing.T) {
	root, res := waitFixture(t, config.PRCreationApproval)
	pc := &fakePC{checks: platform.ChecksSuccess}
	g := branchGit(branchA)
	if _, errs, _ := dashRun(t, Options{}, res, root, pc, &g); len(errs) > 0 {
		t.Fatal(errs)
	}
	body := pc.createdIssues[0].Body
	if want := "## Pending approval\n\nThese updates need approval (`dashboardApproval`, `prCreation: approval`). Tick one to create its PR.\n\n" +
		"- [ ] <!-- deps:create=" + branchA + " --> " + titleA + "\n"; !strings.Contains(body, want) || len(pc.created) != 0 {
		t.Fatalf("created %v, body lacks %q:\n%s", pc.created, want, body)
	}

	ticked := strings.ReplaceAll(body, "- [ ] <!-- deps:create=", "- [x] <!-- deps:create=")
	pc = &fakePC{checks: platform.ChecksPending, issues: []platform.Issue{{Number: 7, Title: dashTitle, Body: ticked}}}
	g = branchGit(branchA)
	acts, errs, _ := dashRun(t, Options{}, res, root, pc, &g)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	i := slices.IndexFunc(acts, func(a Action) bool { return a.Branch == branchA })
	if i < 0 || acts[i].Display() != StatusCreated || !slices.Equal(pc.created, []string{branchA}) || len(pc.checked) != 0 || len(g.pushes) != 0 {
		t.Errorf("actions %+v, created %v, checked %v, pushes %v", acts, pc.created, pc.checked, g.pushes)
	}
	if body := pc.updatedIssues[0].Body; strings.Contains(body, "Pending approval") {
		t.Errorf("body after the PR was opened:\n%s", body)
	}
}

func TestActionDisplayPRWait(t *testing.T) {
	a := Action{Status: StatusCreated, PRWait: PRWaitChecks}
	if s := a.Display(); s != fmt.Sprintf("created, %s", PRWaitChecks) {
		t.Errorf("Display = %q", s)
	}
}
