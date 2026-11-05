package bot

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// DashboardOptions configure the dependency dashboard issue.
type DashboardOptions struct {
	Title  string   // exact issue title the dashboard is found by
	Labels []string // set when the issue is created
}

// Dashboard body markers and limits.
const (
	dashboardMarker = "<!-- deps:dashboard -->"
	// dashboardMaxBytes keeps the body below GitHub's 65536 character limit with room for
	// edits; Gitea and Forgejo have no smaller limit.
	dashboardMaxBytes  = 60000
	dashboardMaxErrors = 20
	dashboardErrorLen  = 300
	// dashboardIssuePages bounds the open-issue scan that finds the dashboard.
	dashboardIssuePages = 10
)

// checkedRe finds ticked dashboard checkboxes: "- [x] <!-- deps:<action>=<branch> -->".
var checkedRe = regexp.MustCompile(`(?m)^\s*[-*] \[[xX]\] <!-- deps:(create|recreate|rebase|pr)=(\S+) -->`)

// dashboard is the dashboard issue found at the start of a run (issue nil = none yet) and the
// requests of its ticked checkboxes.
type dashboard struct {
	issue                        *platform.Issue
	create, recreate, rebase, pr map[string]bool // branches
}

// loadDashboard finds the open issue titled title whose body has the dashboard marker (the
// lowest number if there are several) and parses its ticked checkboxes.
func loadDashboard(ctx context.Context, pc platform.Client, title string) (*dashboard, error) {
	issues, err := pc.ListIssues(ctx, dashboardIssuePages)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	d := &dashboard{create: map[string]bool{}, recreate: map[string]bool{}, rebase: map[string]bool{}, pr: map[string]bool{}}
	for i := range issues {
		is := &issues[i]
		if is.Title != title || !strings.Contains(is.Body, dashboardMarker) {
			continue
		}
		if d.issue == nil || is.Number < d.issue.Number {
			d.issue = is
		}
	}
	if d.issue == nil {
		return d, nil
	}
	for _, m := range checkedRe.FindAllStringSubmatch(d.issue.Body, -1) {
		switch m[1] {
		case "create":
			d.create[m[2]] = true
		case "recreate":
			d.recreate[m[2]] = true
		case "rebase":
			d.rebase[m[2]] = true
		case "pr":
			d.pr[m[2]] = true
		}
	}
	return d, nil
}

// force returns the requests for branch; a nil dashboard requests nothing.
func (d *dashboard) force(branch string) force {
	if d == nil {
		return force{}
	}
	return force{create: d.create[branch], recreate: d.recreate[branch], rebase: d.rebase[branch], pr: d.pr[branch]}
}

// write creates the dashboard issue or updates its body when it changed; a dry run only
// reports what it would do.
func (d *dashboard) write(ctx context.Context, o Options, pc platform.Client, body string) (Action, error) {
	a := Action{Dashboard: true, Title: o.Dashboard.Title}
	if d.issue == nil {
		if o.DryRun {
			a.Status = StatusWouldCreate
			return a, nil
		}
		is, err := pc.CreateIssue(ctx, o.Dashboard.Title, body, o.Dashboard.Labels)
		if err != nil {
			return a, fmt.Errorf("create issue: %w", err)
		}
		a.Status, a.Number, a.URL = StatusCreated, is.Number, is.URL
		return a, nil
	}
	a.Number, a.URL = d.issue.Number, d.issue.URL
	switch {
	case sameBody(d.issue.Body, body):
		a.Status = StatusUpToDate
	case o.DryRun:
		a.Status = StatusWouldUpdate
	default:
		if err := pc.UpdateIssue(ctx, d.issue.Number, body); err != nil {
			return a, fmt.Errorf("update issue #%d: %w", d.issue.Number, err)
		}
		a.Status = StatusUpdated
	}
	return a, nil
}

// sameBody compares bodies ignoring line endings and surrounding whitespace, which web
// editors change.
func sameBody(a, b string) bool {
	norm := func(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }
	return norm(a) == norm(b)
}

// renderDashboard renders the dashboard body: pending PRs (schedule, limits), ignored or
// blocked updates, branches waiting for branch automerge, open PRs, errors and the detected
// dependencies. It is deterministic, so an unchanged run does not edit the issue, and bounded
// to dashboardMaxBytes; every checkbox is unticked again.
func renderDashboard(actions []Action, errs []error, deps []dep.Dep) string {
	var schedule, limited, blocked, branches, open []string
	for _, a := range actions {
		// A PR or branch deps merged in this run is no longer open.
		if a.Dashboard || a.Status == StatusClosed || a.Status == StatusDeleted || a.Automerge == AutomergeMerged {
			continue
		}
		switch {
		case a.Status == StatusSkipClosed:
			blocked = append(blocked, fmt.Sprintf("- [ ] <!-- deps:recreate=%s --> %s (%s closed without merging; tick to recreate it)", a.Branch, a.Title, prLink(a)))
		case a.Status == StatusSkipClosedMajor:
			blocked = append(blocked, fmt.Sprintf("- [ ] <!-- deps:recreate=%s --> %s (%s closed without merging, so this major is ignored; tick to recreate it)", a.Branch, a.Title, prLink(a)))
		case a.Status == StatusSkipModified:
			blocked = append(blocked, fmt.Sprintf("- [ ] <!-- deps:rebase=%s --> %s (branch modified by someone else; tick to discard those commits and rebuild it)", a.Branch, a.Title))
		case a.Status == StatusSkipLimit:
			limited = append(limited, fmt.Sprintf("- [ ] <!-- deps:create=%s --> %s", a.Branch, a.Title))
		case a.Status == StatusSkipApply:
			blocked = append(blocked, fmt.Sprintf("- %s (cannot be applied: %s)", a.Title, a.Reason))
		case a.Status == StatusSkipStopped:
			blocked = append(blocked, fmt.Sprintf("- %s (%s has the %#q label; deps leaves it alone until the label is removed)", a.Title, prLink(a), a.Reason))
		case a.Status == StatusSkipSchedule && a.Number == 0:
			schedule = append(schedule, fmt.Sprintf("- [ ] <!-- deps:create=%s --> %s (%s)", a.Branch, a.Title, waits(a.Waits)))
		case a.Number == 0 && (a.Automerge == AutomergeBranchWaiting || a.Automerge == AutomergeBranchNextRun):
			branches = append(branches, fmt.Sprintf("- [ ] <!-- deps:pr=%s --> %s (%#q)", a.Branch, a.Title, a.Branch))
		case a.Number > 0:
			open = append(open, fmt.Sprintf("- [ ] <!-- deps:rebase=%s --> %s (%s)", a.Branch, a.Title, prLink(a)))
		}
	}

	w := &budget{left: dashboardMaxBytes - len(dashboardMarker) - 2}
	w.text("This issue lists the dependency updates [deps](https://git.jakob-moeller.cloud/jakobmoellerdev/deps) manages. Tick a checkbox to make the next run act on it.\n")
	w.section("Awaiting schedule", "These updates wait for a run triggered by one of their crons. Tick one to create its PR now.", schedule)
	w.section("Rate-limited", "These updates wait for the PR limits. Tick one to create its PR now.", limited)
	w.section("Ignored or blocked", "", blocked)
	w.section("Pending branch automerge", "These branches are merged without a PR once their checks pass. Tick one to create its PR instead.", branches)
	w.section("Open", "These PRs are open. Tick one to rebuild its branch.", open)

	if len(errs) > 0 {
		lines := make([]string, 0, min(len(errs), dashboardMaxErrors)+1)
		for _, e := range errs[:min(len(errs), dashboardMaxErrors)] {
			lines = append(lines, "- `"+oneLine(e.Error(), dashboardErrorLen)+"`")
		}
		if n := len(errs) - dashboardMaxErrors; n > 0 {
			lines = append(lines, fmt.Sprintf("- … and %d more (see the run log)", n))
		}
		w.section("Errors", "The last run reported these errors.", lines)
	}
	w.detected(deps)
	w.b.WriteString("\n" + dashboardMarker + "\n")
	return w.b.String()
}

// prLink renders "[#n](url)" for a PR action, "the PR" without a number.
func prLink(a Action) string {
	if a.Number == 0 {
		return "the PR"
	}
	if a.URL == "" {
		return fmt.Sprintf("#%d", a.Number)
	}
	return fmt.Sprintf("[#%d](%s)", a.Number, a.URL)
}

// oneLine flattens s to one line of at most n runes, without backticks (it is rendered as a
// code span).
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "'")), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// budget writes the dashboard body within left bytes.
type budget struct {
	b    strings.Builder
	left int
}

// fits reports whether n more bytes fit, keeping reserve bytes for closing text.
func (w *budget) fits(n, reserve int) bool { return n+reserve <= w.left }

func (w *budget) text(s string) {
	w.b.WriteString(s)
	w.left -= len(s)
}

// section writes a heading, an optional intro and the lines that fit; an empty section is left
// out.
func (w *budget) section(heading, intro string, lines []string) {
	if len(lines) == 0 {
		return
	}
	head := "\n## " + heading + "\n\n"
	if intro != "" {
		head += intro + "\n\n"
	}
	const more = "- … %d more not listed (dashboard size limit)\n"
	if !w.fits(len(head), len(more)+8) {
		return
	}
	w.text(head)
	for i, l := range lines {
		if !w.fits(len(l)+1, len(more)+8) {
			w.text(fmt.Sprintf(more, len(lines)-i))
			return
		}
		w.text(l + "\n")
	}
}

// detected writes the detected dependencies as <details> per manager and file, sorted, with
// one "name version" line per dependency, until the budget is used up.
func (w *budget) detected(deps []dep.Dep) {
	if len(deps) == 0 {
		return
	}
	// Sorting indices moves ints instead of whole deps; the index tiebreak keeps the order
	// of a stable sort.
	order := make([]int, len(deps))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(i, j int) int {
		a, b := &deps[i], &deps[j]
		return cmp.Or(strings.Compare(a.Manager, b.Manager), strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), strings.Compare(a.Name, b.Name), cmp.Compare(i, j))
	})
	const (
		closeFile    = "</details>\n"
		closeManager = "</details>\n"
		truncated    = "\n_%d more dependencies not listed (dashboard size limit)._\n"
	)
	reserve := len(closeFile) + len(closeManager) + len(truncated) + 8
	head := "\n## Detected dependencies\n\n"
	if !w.fits(len(head), reserve) {
		return
	}
	w.text(head)
	manager, file := "", ""
	for i, k := range order {
		d := &deps[k]
		var open strings.Builder
		if d.Manager != manager {
			if file != "" {
				open.WriteString(closeFile + "\n")
			}
			if manager != "" {
				open.WriteString(closeManager + "\n")
			}
			fmt.Fprintf(&open, "<details><summary>%s</summary>\n\n", d.Manager)
			manager, file = d.Manager, ""
		}
		if d.File != file {
			if file != "" {
				open.WriteString(closeFile + "\n")
			}
			fmt.Fprintf(&open, "<details><summary>%s</summary>\n\n", d.File)
			file = d.File
		}
		line := "- `" + strings.ReplaceAll(strings.TrimSpace(d.Name+" "+d.Version), "`", "'") + "`\n"
		if !w.fits(open.Len()+len(line), reserve) {
			if i > 0 {
				w.text(closeFile + closeManager)
			}
			w.text(fmt.Sprintf(truncated, len(order)-i))
			return
		}
		w.text(open.String() + line)
	}
	w.text(closeFile + "\n" + closeManager)
}

// waits renders the crons an out-of-schedule group waits for.
func waits(crons []string) string {
	if len(crons) == 0 {
		return "no single cron matches all of its updates"
	}
	return "waits for `" + strings.Join(crons, "` or `") + "`"
}
