package bot

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

const dashTitle = "Dependency Dashboard"

var dashNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// dashFixture is the bot fixture with detected deps (one skipped) for the dashboard.
func dashFixture(t *testing.T) (string, *engine.Result) {
	t.Helper()
	root, res := fixture(t)
	local := dep.Dep{Manager: "github-actions", File: "wf.yml", Line: 4, Name: "./local", Skip: "local action"}
	res.Deps = []dep.Dep{res.Updates[1].Dep, res.Updates[0].Dep, local}
	res.Skipped = []dep.Dep{local}
	return root, res
}

// rateLimited makes newPRLimit 0: one managed PR was created in the last hour.
func rateLimited() ([]platform.PR, Options) {
	return []platform.PR{{Number: 50, Head: "deps/gomod/old", Merged: true, CreatedAt: dashNow.Add(-10 * time.Minute)}}, Options{HourlyLimit: 1}
}

func dashRun(t *testing.T, o Options, res *engine.Result, root string, pc *fakePC, g *fakeGit) ([]Action, []error, Action) {
	t.Helper()
	o.AuthorEmail, o.Managers, o.Now = email, managers, dashNow
	if o.Dashboard == nil {
		o.Dashboard = &DashboardOptions{Title: dashTitle, Labels: []string{"dependencies"}}
	}
	acts, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: g}}, nil, pc)
	var dash Action
	for _, a := range acts {
		if a.Dashboard {
			dash = a
		}
	}
	return acts, errs, dash
}

func statusOf(acts []Action, branch string) string {
	for _, a := range acts {
		if a.Branch == branch && !a.Dashboard {
			return a.Status
		}
	}
	return ""
}

func TestDashboardCreate(t *testing.T) {
	root, res := dashFixture(t)
	pc := &fakePC{}
	_, errs, dash := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if dash.Status != StatusCreated || dash.Number != 500 || dash.URL != "https://forge/issues/500" || dash.Title != dashTitle {
		t.Fatalf("dashboard action %+v", dash)
	}
	if pc.issuePages != dashboardIssuePages || len(pc.createdIssues) != 1 || len(pc.updatedIssues) != 0 {
		t.Fatalf("issues: pages %d created %v updated %v", pc.issuePages, pc.createdIssues, pc.updatedIssues)
	}
	if is := pc.createdIssues[0]; is.Title != dashTitle || !slices.Equal(pc.issueLabels[0], []string{"dependencies"}) {
		t.Errorf("created %q labels %v", is.Title, pc.issueLabels[0])
	}
	body := pc.createdIssues[0].Body
	for _, want := range []string{
		"## Open\n\nThese PRs are open. Tick one to rebuild its branch.\n\n" +
			"- [ ] <!-- deps:rebase=" + branchA + " --> " + titleA + " (#101)\n" +
			"- [ ] <!-- deps:rebase=" + branchB + " --> chore(deps): update b/y to v2 (#102)\n",
		"## Detected dependencies\n\n<details><summary>github-actions</summary>\n\n<details><summary>wf.yml</summary>\n\n" +
			"- `a/x v1`\n- `b/y v1`\n- `./local`\n</details>\n\n</details>\n",
		"\n" + dashboardMarker + "\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "## Errors") || strings.Contains(body, "## Awaiting schedule") {
		t.Errorf("empty sections rendered:\n%s", body)
	}
}

func TestDashboardUnchangedNoWrite(t *testing.T) {
	root, res := dashFixture(t)
	closed, o := rateLimited()
	first := &fakePC{closed: closed}
	acts, errs, dash := dashRun(t, o, res, root, first, &fakeGit{})
	if len(errs) > 0 || dash.Status != StatusCreated || statusOf(acts, branchA) != StatusSkipLimit {
		t.Fatalf("first run: %v %+v %+v", errs, dash, acts)
	}
	body := first.createdIssues[0].Body
	if !strings.Contains(body, "## Rate-limited\n\nThese updates wait for the PR limits. Tick one to create its PR now.\n\n- [ ] <!-- deps:create="+branchA+" --> "+titleA+"\n") {
		t.Errorf("rate-limited section missing:\n%s", body)
	}

	// The forge returns CRLF bodies after a web edit; a same-titled issue without the marker is
	// not the dashboard.
	issues := []platform.Issue{
		{Number: 3, Title: dashTitle, Body: "my own notes"},
		{Number: 7, Title: dashTitle, Body: strings.ReplaceAll(body, "\n", "\r\n"), URL: "https://forge/issues/7"},
	}
	second := &fakePC{closed: closed, issues: issues}
	_, errs, dash = dashRun(t, o, res, root, second, &fakeGit{})
	if len(errs) > 0 || dash.Status != StatusUpToDate || dash.Number != 7 || len(second.createdIssues)+len(second.updatedIssues) != 0 {
		t.Fatalf("second run: %v %+v created %v updated %v", errs, dash, second.createdIssues, second.updatedIssues)
	}

	// Dry runs report but never write.
	o.DryRun = true
	dry := &fakePC{closed: closed}
	if _, _, dash = dashRun(t, o, res, root, dry, &fakeGit{}); dash.Status != StatusWouldCreate || len(dry.createdIssues) != 0 {
		t.Errorf("dry run without issue: %+v", dash)
	}
	dry = &fakePC{closed: closed, issues: []platform.Issue{{Number: 7, Title: dashTitle, Body: "old " + dashboardMarker}}}
	if _, _, dash = dashRun(t, o, res, root, dry, &fakeGit{}); dash.Status != StatusWouldUpdate || len(dry.updatedIssues) != 0 {
		t.Errorf("dry run with stale issue: %+v", dash)
	}
}

// A repository that opened thousands of issues after the dashboard still finds it: the scan
// lists the oldest issues first, so a newest-first page window cannot push it out.
func TestDashboardBeyondPageWindow(t *testing.T) {
	root, res := dashFixture(t)
	issues := []platform.Issue{{Number: 7, Title: dashTitle, Body: "stale " + dashboardMarker, URL: "https://forge/issues/7"}}
	for n := range dashboardIssuePages * fakeIssuesPerPage {
		issues = append(issues, platform.Issue{Number: 1000 + n, Title: fmt.Sprintf("bug %d", n)})
	}
	pc := &fakePC{issues: issues}
	_, errs, dash := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !pc.issueOldest {
		t.Error("the dashboard scan must ask for the oldest issues first")
	}
	if dash.Status != StatusUpdated || dash.Number != 7 || len(pc.createdIssues) != 0 {
		t.Fatalf("dashboard %+v, created %+v; want #7 updated and no duplicate", dash, pc.createdIssues)
	}
}

func TestDashboardCheckboxCreateBypassesScheduleAndLimit(t *testing.T) {
	root, res := dashFixture(t)
	weekly, err := schedule.Parse([]string{"0 0 * * 0", "0 6 * * 1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Updates[0].Schedule = weekly // a/x is outside its schedule in the daily run
	closed, o := rateLimited()
	o.Trigger = schedule.Trigger{Event: schedule.EventSchedule, Cron: "0 0 * * *"}
	issue := platform.Issue{Number: 7, Title: dashTitle, Body: "x\n- [x] <!-- deps:create=" + branchA + " --> " + titleA + "\n" + dashboardMarker}
	pc := &fakePC{closed: closed, issues: []platform.Issue{issue}}
	g := &fakeGit{}
	acts, errs, dash := dashRun(t, o, res, root, pc, g)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if statusOf(acts, branchA) != StatusCreated || statusOf(acts, branchB) != StatusSkipLimit || !slices.Equal(g.pushed, []string{branchA}) {
		t.Fatalf("actions %+v pushed %v", acts, g.pushed)
	}
	if dash.Status != StatusUpdated || len(pc.updatedIssues) != 1 || pc.updatedIssues[0].Number != 7 {
		t.Fatalf("dashboard %+v updated %v", dash, pc.updatedIssues)
	}
	body := pc.updatedIssues[0].Body
	if strings.Contains(body, "[x]") || !strings.Contains(body, "- [ ] <!-- deps:rebase="+branchA+" -->") || !strings.Contains(body, "- [ ] <!-- deps:create="+branchB+" -->") {
		t.Errorf("regenerated body:\n%s", body)
	}

	// Without the checkbox a/x waits for its schedule and is listed as such.
	pc = &fakePC{closed: closed}
	acts, _, _ = dashRun(t, o, res, root, pc, &fakeGit{})
	if statusOf(acts, branchA) != StatusSkipSchedule || !strings.Contains(pc.createdIssues[0].Body, "## Awaiting schedule\n\nThese updates wait for a run triggered by one of their crons or inside one of their time windows. Tick one to create its PR now.\n\n- [ ] <!-- deps:create="+branchA+" --> "+titleA+" (waits for `0 0 * * 0` or `0 6 * * 1`)\n") {
		t.Errorf("actions %+v body:\n%s", acts, pc.createdIssues[0].Body)
	}
}

func TestDashboardRecreateClosed(t *testing.T) {
	root, res := dashFixture(t)
	closed := []platform.PR{{Number: 4, Head: branchA, Title: titleA, URL: "https://forge/pulls/4"}}
	pc := &fakePC{closed: closed}
	acts, _, _ := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if statusOf(acts, branchA) != StatusSkipClosed {
		t.Fatalf("actions %+v", acts)
	}
	if want := "## Ignored or blocked\n\n- [ ] <!-- deps:recreate=" + branchA + " --> " + titleA + " ([#4](https://forge/pulls/4) closed without merging; tick to recreate it)\n"; !strings.Contains(pc.createdIssues[0].Body, want) {
		t.Errorf("body lacks %q:\n%s", want, pc.createdIssues[0].Body)
	}

	issue := platform.Issue{Number: 7, Title: dashTitle, Body: strings.Replace(pc.createdIssues[0].Body, "- [ ] <!-- deps:recreate=", "- [X] <!-- deps:recreate=", 1)}
	pc = &fakePC{closed: closed, issues: []platform.Issue{issue}}
	acts, errs, _ := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) > 0 || statusOf(acts, branchA) != StatusCreated || !slices.Contains(pc.created, branchA) {
		t.Fatalf("recreate: %v %+v created %v", errs, acts, pc.created)
	}
}

func TestDashboardRebase(t *testing.T) {
	yes := true
	root, res := dashFixture(t)
	fpA := fingerprintOf(t, root, res, "a/x")
	open := []platform.PR{{Number: 1, Head: branchA, Title: titleA, URL: "u1", Mergeable: &yes, Body: "x\n<!-- deps:fingerprint=" + fpA + " -->\n"}}
	branches := map[string]string{"refs/heads/" + branchA: "a", "refs/heads/" + branchB: "b"}
	authors := map[string]string{branchA: email, branchB: "human@example.invalid"}

	// Unticked: a/x is up to date, b/y was modified by someone else.
	pc := &fakePC{open: open}
	g := &fakeGit{branches: branches, authors: authors}
	acts, _, _ := dashRun(t, Options{}, res, root, pc, g)
	if statusOf(acts, branchA) != StatusUpToDate || statusOf(acts, branchB) != StatusSkipModified || len(g.pushed) != 0 {
		t.Fatalf("actions %+v pushed %v", acts, g.pushed)
	}
	body := pc.createdIssues[0].Body
	if !strings.Contains(body, "- [ ] <!-- deps:rebase="+branchB+" --> chore(deps): update b/y to v2 (branch modified by someone else; tick to discard those commits and rebuild it)\n") ||
		!strings.Contains(body, "- [ ] <!-- deps:rebase="+branchA+" --> "+titleA+" ([#1](u1))\n") {
		t.Fatalf("body:\n%s", body)
	}

	// Ticked: both branches are rebuilt.
	ticked := strings.ReplaceAll(body, "- [ ] <!-- deps:rebase=", "- [x] <!-- deps:rebase=")
	pc = &fakePC{open: open, issues: []platform.Issue{{Number: 7, Title: dashTitle, Body: ticked}}}
	g = &fakeGit{branches: branches, authors: authors}
	acts, errs, dash := dashRun(t, Options{}, res, root, pc, g)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if statusOf(acts, branchA) != StatusRebased || statusOf(acts, branchB) != StatusCreated || !slices.Equal(g.pushed, []string{branchA, branchB}) {
		t.Fatalf("actions %+v pushed %v", acts, g.pushed)
	}
	if dash.Status != StatusUpdated || strings.Contains(pc.updatedIssues[0].Body, "[x]") {
		t.Errorf("dashboard %+v body:\n%s", dash, pc.updatedIssues[0].Body)
	}
}

// TestDashboardApproval: an update needing approval gets no PR but a Pending approval
// checkbox; ticking it creates the PR on the next run, and an existing PR or branch counts as
// approved.
func TestDashboardApproval(t *testing.T) {
	root, res := dashFixture(t)
	res.Updates[0].DashboardApproval = true
	pc := &fakePC{}
	g := &fakeGit{}
	acts, errs, _ := dashRun(t, Options{}, res, root, pc, g)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if statusOf(acts, branchA) != StatusSkipApproval || statusOf(acts, branchB) != StatusCreated || !slices.Equal(g.pushed, []string{branchB}) {
		t.Fatalf("actions %+v pushed %v", acts, g.pushed)
	}
	body := pc.createdIssues[0].Body
	want := "## Pending approval\n\nThese updates need approval (`dashboardApproval`, `prCreation: approval`). Tick one to create its PR.\n\n- [ ] <!-- deps:create=" + branchA + " --> " + titleA + "\n"
	if !strings.Contains(body, want) || strings.Index(body, "## Pending approval") > strings.Index(body, "## Open") {
		t.Fatalf("body lacks %q before Open:\n%s", want, body)
	}

	// Ticked: the next run creates the PR, and the regenerated body lists it as open.
	ticked := strings.Replace(body, "- [ ] <!-- deps:create=", "- [x] <!-- deps:create=", 1)
	pc = &fakePC{issues: []platform.Issue{{Number: 7, Title: dashTitle, Body: ticked}}}
	g = &fakeGit{}
	acts, errs, dash := dashRun(t, Options{}, res, root, pc, g)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if statusOf(acts, branchA) != StatusCreated || !slices.Contains(g.pushed, branchA) || !slices.Contains(pc.created, branchA) {
		t.Fatalf("actions %+v pushed %v created %v", acts, g.pushed, pc.created)
	}
	if b := pc.updatedIssues[0].Body; dash.Status != StatusUpdated || strings.Contains(b, "## Pending approval") || !strings.Contains(b, "- [ ] <!-- deps:rebase="+branchA+" -->") {
		t.Errorf("dashboard %+v body:\n%s", dash, b)
	}

	// An open PR or an existing branch was approved before: no checkbox needed.
	yes := true
	fpA := fingerprintOf(t, root, res, "a/x")
	open := []platform.PR{{Number: 1, Head: branchA, Title: titleA, URL: "u1", Mergeable: &yes, Body: "x\n<!-- deps:fingerprint=" + fpA + " -->\n"}}
	if acts, _, _ = dashRun(t, Options{}, res, root, &fakePC{open: open}, &fakeGit{}); statusOf(acts, branchA) != StatusUpToDate {
		t.Errorf("open PR: %+v", acts)
	}
	g = &fakeGit{branches: map[string]string{"refs/heads/" + branchA: "a"}, authors: map[string]string{branchA: email}}
	if acts, _, _ = dashRun(t, Options{}, res, root, &fakePC{}, g); statusOf(acts, branchA) != StatusCreated {
		t.Errorf("existing branch: %+v", acts)
	}
}

func TestDashboardErrorsAndSizeCap(t *testing.T) {
	root, res := dashFixture(t)
	for i := range 25 {
		res.Errors = append(res.Errors, fmt.Errorf("f%d.yml: `boom`\nsecond line %s", i, strings.Repeat("x", 400)))
	}
	long := strings.Repeat("n", 200)
	for i := range 2000 {
		res.Deps = append(res.Deps, dep.Dep{Manager: "gomod", File: fmt.Sprintf("m%03d/go.mod", i/10), Name: fmt.Sprintf("%s/%d", long, i), Version: "v1.0.0"})
	}
	pc := &fakePC{}
	_, errs, dash := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) > 0 || dash.Status != StatusCreated {
		t.Fatalf("%v %+v", errs, dash)
	}
	body := pc.createdIssues[0].Body
	if len(body) > dashboardMaxBytes {
		t.Errorf("body is %d bytes, cap %d", len(body), dashboardMaxBytes)
	}
	if !strings.HasSuffix(body, dashboardMarker+"\n") || strings.Count(body, "<details>") != strings.Count(body, "</details>") {
		t.Errorf("body not closed properly:\n%s", body[len(body)-500:])
	}
	if !strings.Contains(body, "more dependencies not listed (dashboard size limit)") || !strings.Contains(body, "- … and 5 more (see the run log)\n") {
		t.Errorf("truncation notes missing")
	}
	if strings.Count(body, "\n- `f") != dashboardMaxErrors || !strings.Contains(body, "- `f0.yml: 'boom' second line xxx") || strings.Contains(body, strings.Repeat("x", 300)) {
		t.Errorf("errors not flattened and truncated:\n%s", body[:3000])
	}
}

// Rate-limited lookups are listed apart from the errors, as a warning.
func TestDashboardRateLimited(t *testing.T) {
	limited := &engine.DepError{File: ".env", Line: 6, Name: "golangci/golangci-lint",
		Err: fmt.Errorf("HTTP 403: %w by api.github.com until 2026-11-10T14:33:49Z", httpx.ErrRateLimited)}
	body := renderDashboard([]dashboardSection{{}}, []error{limited, errBoom})
	for _, want := range []string{
		"## Rate-limited lookups\n\nThe last run skipped these lookups because their host's rate limit was exhausted; the next run looks them up again.\n\n" +
			"- `.env:6: golangci/golangci-lint: HTTP 403: rate limited by api.github.com until 2026-11-10T14:33:49Z`\n",
		"## Errors\n\nThe last run reported these errors.\n\n- `boom`\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if body := renderDashboard([]dashboardSection{{}}, []error{limited}); strings.Contains(body, "## Errors") {
		t.Errorf("errors section without errors:\n%s", body)
	}
}

func TestDashboardDisabledAndLoadError(t *testing.T) {
	root, res := dashFixture(t)
	pc := &fakePC{issues: []platform.Issue{{Number: 7, Title: dashTitle, Body: dashboardMarker}}}
	acts, errs := Run(t.Context(), Options{AuthorEmail: email, Managers: managers}, []Base{{Root: root, Res: res, Git: &fakeGit{}}}, nil, pc)
	if len(errs) > 0 || pc.issuePages != 0 || len(pc.createdIssues)+len(pc.updatedIssues) != 0 || slices.ContainsFunc(acts, func(a Action) bool { return a.Dashboard }) {
		t.Fatalf("disabled dashboard touched: %v pages %d %+v", errs, pc.issuePages, acts)
	}

	pc = &fakePC{fail: map[string]error{"ListIssues": errBoom}}
	acts, errs, dash := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) != 1 || errs[0].Error() != "dependency dashboard: list issues: boom" || !errors.Is(errs[0], errBoom) {
		t.Fatalf("errs = %v", errs)
	}
	if dash.Dashboard || len(pc.createdIssues) != 0 || statusOf(acts, branchA) != StatusCreated {
		t.Errorf("dashboard %+v created %v actions %+v", dash, pc.createdIssues, acts)
	}
}

func TestDashboardIssuesDisabled(t *testing.T) {
	root, res := dashFixture(t)
	pc := &fakePC{fail: map[string]error{"ListIssues": fmt.Errorf("%w: GET /repos/o/r/issues: 404", platform.ErrIssuesDisabled)}}
	acts, errs, dash := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) > 0 || dash.Status != StatusSkipIssues || dash.Title != dashTitle || len(pc.createdIssues) != 0 {
		t.Fatalf("errs %v dashboard %+v created %v", errs, dash, pc.createdIssues)
	}
	if statusOf(acts, branchA) != StatusCreated {
		t.Errorf("PRs not processed: %+v", acts)
	}
}

// A PR deps merges directly (GitHub without auto-merge) is gone from the dashboard's open list
// in the same run; found in the GitHub end-to-end test.
func TestDashboardOmitsPRMergedByDeps(t *testing.T) {
	root, res := dashFixture(t)
	res.Updates[0].Automerge = true
	yes := true
	fp := fingerprintOf(t, root, res, "a/x")
	pc := &fakePC{
		open:      []platform.PR{{Number: 1, Head: branchA, Title: titleA, Body: "<!-- deps:fingerprint=" + fp + " -->", HeadSHA: "aaaa", Mergeable: &yes, URL: "https://forge/pulls/1"}},
		mergeable: &yes,
		headSHA:   "aaaa",
		checks:    platform.ChecksSuccess,
		fail:      map[string]error{"EnableAutoMerge": fmt.Errorf("enable auto-merge #1: %w: disabled in the repository settings", platform.ErrAutoMergeUnavailable)},
	}
	acts, errs, _ := dashRun(t, Options{}, res, root, pc, &fakeGit{})
	if len(errs) > 0 || len(pc.merged) != 1 {
		t.Fatalf("errs %v merged %v actions %+v", errs, pc.merged, acts)
	}
	body := pc.createdIssues[0].Body
	if strings.Contains(body, "deps:rebase="+branchA) || !strings.Contains(body, "deps:rebase="+branchB) {
		t.Errorf("open section should list only %s:\n%s", branchB, body)
	}
}

// TestDetectedKeepsStableOrder checks the detected list against a stable sort of the deps:
// deps that compare equal (same manager, file, line and name) keep their input order.
func TestDetectedKeepsStableOrder(t *testing.T) {
	var deps []dep.Dep
	for i := range 400 {
		deps = append(deps, dep.Dep{Manager: []string{"npm", "gomod"}[i*7%2], File: fmt.Sprintf("f%d", i*5%3), Line: i * 3 % 4, Name: fmt.Sprintf("n%d", i*11%3), Version: fmt.Sprintf("v%d", i)})
	}
	stable := slices.Clone(deps)
	slices.SortStableFunc(stable, func(a, b dep.Dep) int {
		return cmp.Or(strings.Compare(a.Manager, b.Manager), strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), strings.Compare(a.Name, b.Name))
	})
	render := func(deps []dep.Dep) string {
		w := &budget{left: 1 << 20}
		w.detected(deps, "## Detected dependencies")
		return w.b.String()
	}
	if got, want := render(deps), render(stable); got != want {
		t.Errorf("detected order differs from a stable sort:\n%s\nwant:\n%s", got, want)
	}
}

// TestDashboardWarnings lists deprecated and abandoned deps once per name and reason, with
// their files, sorted, and the update PR body repeats the dep's warnings.
func TestDashboardWarnings(t *testing.T) {
	req := dep.Dep{Manager: "npm", File: "a/package.json", Name: "request", Version: "2.88.0"}
	req2 := req
	req2.File = "b/package.json"
	pad := dep.Dep{Manager: "npm", File: "package.json", Name: "left-pad", Version: "1.1.0"}
	warns := []engine.Warning{
		{Dep: req, Kind: engine.WarnDeprecated, Reason: "package deprecated: request has been\ndeprecated, see `#3142`"},
		{Dep: pad, Kind: engine.WarnAbandoned, Reason: "no release since 2024-04-16 (abandonmentThreshold 730 days)"},
		{Dep: req2, Kind: engine.WarnDeprecated, Reason: "package deprecated: request has been\ndeprecated, see `#3142`"},
		{Dep: pad, Kind: engine.WarnDeprecated, Reason: "package deprecated: use String.prototype.padStart()"},
	}
	body := renderDashboard([]dashboardSection{{warns: warns}}, nil)
	for _, want := range []string{
		"## Deprecated dependencies\n\nThe registry marks these dependencies or their installed versions deprecated, yanked or inactive, or their source repository is archived. Consider replacing them.\n\n" +
			"- `left-pad`: package deprecated: use String.prototype.padStart() (`package.json`)\n" +
			"- `request`: package deprecated: request has been deprecated, see '#3142' (`a/package.json`, `b/package.json`)\n",
		"## Abandoned dependencies\n\nThese dependencies had no release within `abandonmentThreshold`.\n\n" +
			"- `left-pad`: no release since 2024-04-16 (abandonmentThreshold 730 days) (`package.json`)\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if body := renderDashboard([]dashboardSection{{}}, nil); strings.Contains(body, "Deprecated dependencies") || strings.Contains(body, "Abandoned dependencies") {
		t.Errorf("empty warning sections rendered:\n%s", body)
	}

	ws := []string{"deprecated: package deprecated: use String.prototype.padStart()", "abandoned: no release since 2024-04-16"}
	grp := engine.Group{Updates: []dep.Update{
		{Dep: pad, Type: dep.Patch, NewVersion: "1.1.1", Warnings: ws},
		{Dep: dep.Dep{Manager: "npm", File: "b/package.json", Name: "left-pad", Version: "1.1.0"}, Type: dep.Patch, NewVersion: "1.1.1", Warnings: ws},
	}}
	pr := prBody(t.Context(), Options{}, grp, "fp", "")
	want := "\n### Warnings\n\nConsider replacing these dependencies rather than updating them.\n\n" +
		"- `left-pad`: deprecated: package deprecated: use String.prototype.padStart()\n" +
		"- `left-pad`: abandoned: no release since 2024-04-16\n\n---"
	if !strings.Contains(pr, want) {
		t.Errorf("PR body lacks %q:\n%s", want, pr)
	}
	if pr := prBody(t.Context(), Options{}, engine.Group{Updates: []dep.Update{{Dep: pad, Type: dep.Patch, NewVersion: "1.1.1"}}}, "fp", ""); strings.Contains(pr, "### Warnings") {
		t.Errorf("PR body without warnings has the section:\n%s", pr)
	}
}
