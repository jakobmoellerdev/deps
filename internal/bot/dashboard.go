package bot

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
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
	// dashboardIssuePages bounds the open-issue scan that finds the dashboard. The forge
	// narrows the listing to the issues titled like it (platform.IssueFilter.Title) and the
	// scan runs oldest first and stops at the dashboard, so these pages are far more than
	// the lookup needs; a scan that still runs out of them does not create a second
	// dashboard but fails (loadDashboard).
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
// lowest number if there are several) and parses its ticked checkboxes. The forge narrows the
// listing to the issues titled like the dashboard, and the rest is listed oldest first and
// ends at the first match, so a repository with thousands of other open issues still finds
// it. A listing that ran out of pages without finding it is an error: the dashboard is
// created once and stays open, and creating a second one on every run is worse than failing.
func loadDashboard(ctx context.Context, pc platform.Client, title string) (*dashboard, error) {
	isDashboard := func(is platform.Issue) bool {
		return is.Title == title && strings.Contains(is.Body, dashboardMarker)
	}
	list, err := pc.ListIssues(ctx, dashboardIssuePages, platform.IssueFilter{Title: title, Oldest: true, Stop: isDashboard})
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	d := &dashboard{create: map[string]bool{}, recreate: map[string]bool{}, rebase: map[string]bool{}, pr: map[string]bool{}}
	for i := range list.Issues {
		is := &list.Issues[i]
		if !isDashboard(*is) {
			continue
		}
		if d.issue == nil || is.Number < d.issue.Number {
			d.issue = is
		}
	}
	if d.issue == nil {
		if list.Truncated {
			return nil, fmt.Errorf("the dashboard issue %q was not found among the %d oldest open issues the forge listed, and there are more: not creating a second dashboard", title, len(list.Issues))
		}
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

// dashboardSection is what the dashboard lists for one base branch: the actions of its groups,
// its deprecated and abandoned dependencies and its detected dependencies. base is "" for the only base of a run whose branches do not
// carry it; its lists then go without a base heading.
type dashboardSection struct {
	base    string
	actions []Action
	deps    []dep.Dep
	warns   []engine.Warning
}

// dashboardLists are the checkbox lists and dependency warnings of one base.
type dashboardLists struct {
	approval, schedule, limited, blocked, branches, checks, open []string
	deprecated, abandoned                                        []string
}

func (l dashboardLists) empty() bool {
	return len(l.approval)+len(l.schedule)+len(l.limited)+len(l.blocked)+len(l.branches)+len(l.checks)+len(l.open)+len(l.deprecated)+len(l.abandoned) == 0
}

// dashboardItems sorts actions into the dashboard lists.
func dashboardItems(actions []Action) dashboardLists {
	var l dashboardLists
	for _, a := range actions {
		// A PR or branch deps merged in this run is no longer open.
		if a.Dashboard || a.Status == StatusClosed || a.Status == StatusDeleted || a.Automerge == AutomergeMerged {
			continue
		}
		switch {
		case a.Status == StatusSkipClosed:
			l.blocked = append(l.blocked, fmt.Sprintf("- [ ] <!-- deps:recreate=%s --> %s (%s closed without merging; tick to recreate it)", a.Branch, a.Title, prLink(a)))
		case a.Status == StatusSkipClosedMajor:
			l.blocked = append(l.blocked, fmt.Sprintf("- [ ] <!-- deps:recreate=%s --> %s (%s closed without merging, so this major is ignored; tick to recreate it)", a.Branch, a.Title, prLink(a)))
		case a.Status == StatusSkipModified:
			l.blocked = append(l.blocked, fmt.Sprintf("- [ ] <!-- deps:rebase=%s --> %s (branch modified by someone else; tick to discard those commits and rebuild it)", a.Branch, a.Title))
		case a.Status == StatusSkipLimit:
			l.limited = append(l.limited, fmt.Sprintf("- [ ] <!-- deps:create=%s --> %s", a.Branch, a.Title))
		case a.Status == StatusSkipApply:
			l.blocked = append(l.blocked, fmt.Sprintf("- %s (cannot be applied: %s)", a.Title, a.Reason))
		case a.Status == StatusSkipStopped:
			l.blocked = append(l.blocked, fmt.Sprintf("- %s (%s has the %#q label; deps leaves it alone until the label is removed)", a.Title, prLink(a), a.Reason))
		case a.Status == StatusSkipApproval, a.Number == 0 && a.PRWait == PRWaitApproval:
			l.approval = append(l.approval, fmt.Sprintf("- [ ] <!-- deps:create=%s --> %s", a.Branch, a.Title))
		case a.Status == StatusSkipSchedule && a.Number == 0:
			l.schedule = append(l.schedule, fmt.Sprintf("- [ ] <!-- deps:create=%s --> %s (%s)", a.Branch, a.Title, waits(a.Waits)))
		case a.Number == 0 && (a.Automerge == AutomergeBranchWaiting || a.Automerge == AutomergeBranchNextRun):
			l.branches = append(l.branches, fmt.Sprintf("- [ ] <!-- deps:pr=%s --> %s (%#q)", a.Branch, a.Title, a.Branch))
		case a.Number == 0 && a.PRWait == PRWaitFailed:
			l.checks = append(l.checks, fmt.Sprintf("- [ ] <!-- deps:pr=%s --> %s (%#q, checks failed)", a.Branch, a.Title, a.Branch))
		case a.Number == 0 && a.PRWait != "":
			l.checks = append(l.checks, fmt.Sprintf("- [ ] <!-- deps:pr=%s --> %s (%#q)", a.Branch, a.Title, a.Branch))
		case a.Number > 0:
			l.open = append(l.open, fmt.Sprintf("- [ ] <!-- deps:rebase=%s --> %s (%s)", a.Branch, a.Title, prLink(a)))
		}
	}
	return l
}

// renderDashboard renders the dashboard body: per base branch (headed by it when sections name
// it) updates awaiting approval, pending PRs (schedule, limits), ignored or blocked updates,
// branches waiting for branch automerge or for their checks (prCreation), open PRs and
// deprecated and abandoned dependencies, then the rate-limited lookups among errs
// (engine.RateLimited, warnings), the other errors and the detected dependencies of each base.
// It is deterministic, so an unchanged run does not edit the issue, and bounded to
// dashboardMaxBytes; every checkbox is unticked again.
func renderDashboard(sections []dashboardSection, errs []error) string {
	w := &budget{left: dashboardMaxBytes - len(dashboardMarker) - 2}
	w.text("This issue lists the dependency updates [deps](https://git.jakob-moeller.cloud/jakobmoellerdev/deps) manages. Tick a checkbox to make the next run act on it.\n")
	for _, sec := range sections {
		l := dashboardItems(sec.actions)
		l.deprecated = warningLines(sec.warns, engine.WarnDeprecated)
		l.abandoned = warningLines(sec.warns, engine.WarnAbandoned)
		level := "##"
		if sec.base != "" {
			if l.empty() || !w.heading("## Base branch `"+sec.base+"`") {
				continue
			}
			level = "###"
		}
		w.section(level, "Pending approval", "These updates need approval (`dashboardApproval`, `prCreation: approval`). Tick one to create its PR.", l.approval)
		w.section(level, "Awaiting schedule", "These updates wait for a run triggered by one of their crons or inside one of their time windows. Tick one to create its PR now.", l.schedule)
		w.section(level, "Rate-limited", "These updates wait for the PR limits. Tick one to create its PR now.", l.limited)
		w.section(level, "Ignored or blocked", "", l.blocked)
		w.section(level, "Pending branch automerge", "These branches are merged without a PR once their checks pass. Tick one to create its PR instead.", l.branches)
		w.section(level, "Pending status checks", "These branches get their PR once their checks finish or pass (`prCreation`). Tick one to create its PR now.", l.checks)
		w.section(level, "Open", "These PRs are open. Tick one to rebuild its branch.", l.open)
		w.section(level, "Deprecated dependencies", "The registry marks these dependencies or their installed versions deprecated, yanked or inactive, or their source repository is archived. Consider replacing them.", l.deprecated)
		w.section(level, "Abandoned dependencies", "These dependencies had no release within `abandonmentThreshold`.", l.abandoned)
	}

	var limited, failed []error
	for _, e := range errs {
		if engine.RateLimited(e) {
			limited = append(limited, e)
		} else {
			failed = append(failed, e)
		}
	}
	w.section("##", "Rate-limited lookups", "The last run skipped these lookups because their host's rate limit was exhausted; the next run looks them up again.", errorLines(limited))
	w.section("##", "Errors", "The last run reported these errors.", errorLines(failed))
	const detected = "## Detected dependencies"
	headed := false
	for _, sec := range sections {
		title := detected
		if sec.base != "" {
			if len(sec.deps) == 0 {
				continue
			}
			if !headed && !w.heading(detected) {
				break
			}
			headed, title = true, "### `"+sec.base+"`"
		}
		w.detected(sec.deps, title)
	}
	w.b.WriteString("\n" + dashboardMarker + "\n")
	return w.b.String()
}

// errorLines renders errs as list items, at most dashboardMaxErrors of them; nil without errs.
func errorLines(errs []error) []string {
	if len(errs) == 0 {
		return nil
	}
	lines := make([]string, 0, min(len(errs), dashboardMaxErrors)+1)
	for _, e := range errs[:min(len(errs), dashboardMaxErrors)] {
		lines = append(lines, "- `"+oneLine(e.Error(), dashboardErrorLen)+"`")
	}
	if n := len(errs) - dashboardMaxErrors; n > 0 {
		lines = append(lines, fmt.Sprintf("- … and %d more (see the run log)", n))
	}
	return lines
}

// warningLines renders the warnings of kind, one line per dep name and reason with the files
// it is found in, sorted by name.
func warningLines(warns []engine.Warning, kind string) []string {
	type key struct{ name, reason string }
	files := map[key][]string{}
	var keys []key
	for _, w := range warns {
		if w.Kind != kind {
			continue
		}
		k := key{w.Dep.Name, oneLine(w.Reason, dashboardErrorLen)}
		if _, ok := files[k]; !ok {
			keys = append(keys, k)
		}
		if fs := files[k]; !slices.Contains(fs, w.Dep.File) {
			files[k] = append(fs, w.Dep.File)
		}
	}
	slices.SortFunc(keys, func(a, b key) int {
		return cmp.Or(strings.Compare(a.name, b.name), strings.Compare(a.reason, b.reason))
	})
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = "- `" + strings.ReplaceAll(k.name, "`", "'") + "`: " + k.reason + " (`" + strings.Join(files[k], "`, `") + "`)"
	}
	return lines
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

// dashboardMore ends a list cut by the size limit.
const dashboardMore = "- … %d more not listed (dashboard size limit)\n"

// put writes s when it fits, keeping room for a cut list ending in more; it reports whether
// it did.
func (w *budget) put(s, more string) bool {
	if !w.fits(len(s), len(more)+8) {
		return false
	}
	w.text(s)
	return true
}

// items writes the lines that fit, each followed by end, and ends a list cut by the size
// limit with more ("… %d more").
func (w *budget) items(lines []string, end, more string) {
	for i, l := range lines {
		if !w.fits(len(l)+len(end), len(more)+8) {
			w.text(fmt.Sprintf(more, len(lines)-i))
			return
		}
		w.text(l)
		w.text(end)
	}
}

// list writes head and the lines that fit below it, one per line; an empty list, and one
// whose head does not fit, write nothing.
func (w *budget) list(head string, lines []string, more string) {
	if len(lines) > 0 && w.put(head, more) {
		w.items(lines, "\n", more)
	}
}

// heading writes the markdown heading h ("## Title") when it fits with room for a cut list;
// it reports whether it did.
func (w *budget) heading(h string) bool {
	return w.put("\n"+h+"\n", dashboardMore)
}

// section writes a heading of level ("##", "###"), an optional intro and the lines that fit;
// an empty section is left out.
func (w *budget) section(level, heading, intro string, lines []string) {
	if len(lines) == 0 {
		return
	}
	head := "\n" + level + " " + heading + "\n\n"
	if intro != "" {
		head += intro + "\n\n"
	}
	w.list(head, lines, dashboardMore)
}

// detected writes the detected dependencies under the heading title as <details> per manager
// and file, sorted, with one "name version" line per dependency, until the budget is used up.
func (w *budget) detected(deps []dep.Dep, title string) {
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
	head := "\n" + title + "\n\n"
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

// waits renders the schedule entries (crons, time windows) an out-of-schedule group waits for.
func waits(crons []string) string {
	if len(crons) == 0 {
		return "no single schedule entry matches all of its updates"
	}
	return "waits for `" + strings.Join(crons, "` or `") + "`"
}
