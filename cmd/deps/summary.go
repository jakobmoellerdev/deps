package main

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

// summary is the end-of-run overview of plan, apply and run: the updates grouped by PR (the
// bot's grouping), the PR outcome per group for run, held-back releases, skip reasons and
// errors. It is rendered as text to stderr and as markdown to the CI step summary.
type summary struct {
	Command string // "plan", "apply", "run" or "run --dry-run"
	Run     bool   // Actions are PR outcomes; plan/apply show what run would open
	Res     *engine.Result
	Groups  []engine.Group
	// prs maps a branch to its first non-dashboard action in Actions, grouped holds the
	// branches of Groups; index builds both on first use.
	prs     map[string]int
	grouped map[string]bool
	Actions []bot.Action // run only
	Errors  []error      // engine and build/bot errors
	Elapsed time.Duration
	Verbose bool   // text lists the held-back releases, not only their count
	Report  string // --report file; "" = none
}

// skipReason is one aggregated skip reason.
type skipReason struct {
	Reason string
	Deps   []dep.Dep
}

// skipHints explain common skip reasons and how to get updates anyway; keys are reason prefixes.
var skipHints = []struct{ prefix, hint string }{
	{"ignored by rule", "a rule sets `ignore: true`; narrow or remove it"},
	{"local action", "actions in this repository (`./…`) have no upstream version"},
	{"non-version ref", "a branch or other ref; pin a version tag to get updates"},
	{"non-version tag", "a tag without a version; set `pin: digest` to pin it"},
	{"pinned without version comment", "add a `# vX.Y.Z` comment after the SHA"},
	{"pseudo-version", "a commit pseudo-version; require a tagged version to get updates"},
	{"module path changed", "the new version's go.mod declares another module path; require that path to update"},
	{"conflicts with the inputs", "the compile inputs (requirements.in, constraints) do not admit the new version; relax them to update"},
	{"replaced", "a replace directive overrides the module"},
	{"variable reference", "the value is computed at runtime"},
	{"template value", "the value is computed at runtime"},
	{"expression", "the value is computed at runtime"},
	{"unknown registry", "set `[registries.<name>] index` in `.cargo/config.toml` or `CARGO_REGISTRIES_<NAME>_INDEX`"},
}

func skipHint(reason string) string {
	for _, h := range skipHints {
		if strings.HasPrefix(reason, h.prefix) {
			return h.hint
		}
	}
	return ""
}

// skipReasons aggregates the skipped deps by reason, most frequent first.
func (s *summary) skipReasons() []skipReason {
	idx := map[string]int{}
	var out []skipReason
	for _, d := range s.Res.Skipped {
		i, ok := idx[d.Skip]
		if !ok {
			i = len(out)
			idx[d.Skip] = i
			out = append(out, skipReason{Reason: d.Skip})
		}
		out[i].Deps = append(out[i].Deps, d)
	}
	slices.SortStableFunc(out, func(a, b skipReason) int {
		return cmp.Or(len(b.Deps)-len(a.Deps), strings.Compare(a.Reason, b.Reason))
	})
	return out
}

// deps is the number of extracted deps; Res.Deps is unset by tests that build a Result by hand.
func (s *summary) deps() int {
	return max(len(s.Res.Deps), len(s.Res.Skipped)+s.distinctUpdated())
}

func (s *summary) distinctUpdated() int {
	type key struct {
		file string
		line int
		name string
	}
	seen := map[key]bool{}
	for _, u := range s.Res.Updates {
		seen[key{u.Dep.File, u.Dep.Line, u.Dep.Name}] = true
	}
	return len(seen)
}

// index builds prs and grouped; Groups and Actions do not change once rendering starts.
func (s *summary) index() {
	if s.prs != nil {
		return
	}
	s.prs = make(map[string]int, len(s.Actions))
	for i, a := range s.Actions {
		if _, ok := s.prs[a.Branch]; !ok && !a.Dashboard {
			s.prs[a.Branch] = i
		}
	}
	s.grouped = make(map[string]bool, len(s.Groups))
	for _, g := range s.Groups {
		s.grouped[g.Branch] = true
	}
}

// action returns the bot outcome of group g.
func (s *summary) action(g engine.Group) (bot.Action, bool) {
	s.index()
	i, ok := s.prs[g.Branch]
	if !ok {
		return bot.Action{}, false
	}
	return s.Actions[i], true
}

// others are the run outcomes without a group: closed obsolete PRs and the dashboard.
func (s *summary) others() []bot.Action {
	s.index()
	var out []bot.Action
	for _, a := range s.Actions {
		if a.Dashboard || !s.grouped[a.Branch] {
			out = append(out, a)
		}
	}
	return out
}

// statusCounts counts the PR statuses of run in first-seen order ("2 created, 1 up-to-date").
func (s *summary) statusCounts() string {
	var order []string
	n := map[string]int{}
	for _, a := range s.Actions {
		if a.Dashboard {
			continue
		}
		if n[a.Status] == 0 {
			order = append(order, a.Status)
		}
		n[a.Status]++
	}
	parts := make([]string, len(order))
	for i, st := range order {
		parts[i] = fmt.Sprintf("%d %s", n[st], st)
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// headline is the one-line overview, or the empty-case sentence.
func (s *summary) headline() string {
	if len(s.Res.Updates) == 0 && len(s.Errors) == 0 && len(s.others()) == 0 {
		return fmt.Sprintf("No updates: %s up to date, %d skipped.", plural(s.deps()-len(s.Res.Skipped), "dependency", "dependencies"), len(s.Res.Skipped))
	}
	parts := []string{
		plural(s.deps(), "dependency", "dependencies"),
		plural(len(s.Res.Updates), "update", "updates") + " in " + plural(len(s.Groups), "PR", "PRs"),
	}
	if s.Run {
		if c := s.statusCounts(); c != "" {
			parts = append(parts, "PRs: "+c)
		}
	}
	if len(s.Res.Pending) > 0 {
		parts = append(parts, fmt.Sprintf("%d held back by minimumReleaseAge", len(s.Res.Pending)))
	}
	parts = append(parts, fmt.Sprintf("%d skipped", len(s.Res.Skipped)), plural(len(s.Errors), "error", "errors"))
	return strings.Join(parts, ", ") + " (" + s.Elapsed.Round(time.Millisecond).String() + ")"
}

// updateType renders the type with a lockfile-only marker and the security advisories
// ("patch, lockfile, security: GHSA-1").
func updateType(u dep.Update) string {
	t := string(u.Type)
	if u.Lockfile {
		t += ", lockfile"
	}
	if len(u.Advisories) == 0 {
		return t
	}
	ids := make([]string, len(u.Advisories))
	for i, a := range u.Advisories {
		ids[i] = a.ID
	}
	return t + ", security: " + strings.Join(ids, ",")
}

// groupNotes are the PR facts of a group besides its updates: status, automerge, schedule.
func (s *summary) groupNotes(g engine.Group, link func(a bot.Action) string) []string {
	var notes []string
	a, ok := s.action(g)
	switch {
	case ok:
		st := a.Status
		if l := link(a); l != "" {
			st += " " + l
		}
		notes = append(notes, st)
		if a.Reason != "" {
			notes = append(notes, a.Reason)
		}
		if a.Automerge != "" {
			notes = append(notes, a.Automerge)
		}
		if len(a.Waits) > 0 {
			notes = append(notes, "waiting for "+strings.Join(a.Waits, " or "))
		}
	case s.Run:
		notes = append(notes, "not processed (see errors)")
	default:
		switch {
		case g.AutomergeBranch:
			notes = append(notes, "branch automerge")
		case g.Automerge:
			notes = append(notes, "automerge")
		}
		if sch := g.Updates[0].Schedule; sch != nil {
			notes = append(notes, "schedule "+sch.String())
		}
	}
	return notes
}

// writeText writes the summary block for stderr. It goes before the counts line, so that line
// stays last. Skip reasons are counts only: -v prints the per-dep skip lines; errors are
// already printed as error lines.
func (s *summary) writeText(w io.Writer) {
	fmt.Fprintf(w, "\nSummary: %s\n", s.headline())
	for _, g := range s.Groups {
		notes := s.groupNotes(g, func(a bot.Action) string { return a.URL })
		fmt.Fprintf(w, "  PR %s  %s", g.Branch, g.Title)
		if len(notes) > 0 {
			fmt.Fprintf(w, "  [%s]", strings.Join(notes, "; "))
		}
		fmt.Fprintln(w)
		for _, u := range g.Updates {
			if u.Maintenance() {
				fmt.Fprintf(w, "      %s  lock file maintenance\n", u.Dep.Name)
				continue
			}
			from, to := u.FromTo()
			fmt.Fprintf(w, "      %s  %s:%d  %s -> %s  (%s)\n", u.Dep.Name, u.Dep.File, u.Dep.Line, from, to, updateType(u))
		}
	}
	for _, a := range s.others() {
		fmt.Fprintf(w, "  %s  %s  %s\n", a.Display(), cmp.Or(a.Branch, a.Title), a.URL)
	}
	switch {
	case len(s.Res.Pending) > 0 && !s.Verbose:
		fmt.Fprintf(w, "  Held back by minimumReleaseAge: %d (-v lists each)\n", len(s.Res.Pending))
	case len(s.Res.Pending) > 0:
		fmt.Fprintln(w, "  Held back by minimumReleaseAge:")
		for _, p := range s.Res.Pending {
			fmt.Fprintf(w, "      %s  %s:%d  %s  %s\n", p.Dep.Name, p.Dep.File, p.Dep.Line, p.Version, p.Reason)
		}
	}
	if rs := s.skipReasons(); len(rs) > 0 {
		fmt.Fprintln(w, "  Skipped (-v lists each dependency):")
		for _, r := range rs {
			fmt.Fprintf(w, "    %4d  %s", len(r.Deps), r.Reason)
			if h := skipHint(r.Reason); h != "" {
				fmt.Fprintf(w, "  (%s)", strings.ReplaceAll(h, "`", ""))
			}
			fmt.Fprintln(w)
		}
	}
}

// mdCell escapes a table cell.
func mdCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// stepSummaryLimit is the size GitHub accepts per step summary file (1 MiB); larger files are
// rejected whole.
const stepSummaryLimit = 1 << 20

// Detail levels of the markdown, from full to compact; each drops more to fit the size limit.
const (
	mdFull          = iota
	mdNoSkippedList // the per-dependency list of skipped deps is left out
	mdNoPendingList // also the held-back releases (only their count stays)
	mdNoPRTables    // also the update tables of the PRs (one line per PR stays)
)

// markdown renders the full step summary.
func (s *summary) markdown() string { return s.markdownAt(mdFull) }

// markdownFit renders the most detailed summary of at most limit bytes. If even the compact
// one is larger, it is cut at a line boundary and ends with a note pointing at the job log.
func (s *summary) markdownFit(limit int) string {
	var md string
	for level := mdFull; level <= mdNoPRTables; level++ {
		if md = s.markdownAt(level); len(md) <= limit {
			return md
		}
	}
	const note = "\n\n**Step summary truncated to its size limit; the job log has everything.**\n"
	cut := max(limit-len(note), 0)
	if i := strings.LastIndexByte(md[:min(cut, len(md))], '\n'); i >= 0 {
		cut = i
	}
	if cut+len(note) > limit {
		return ""
	}
	return md[:cut] + note
}

// markdownAt renders the step summary at a detail level.
func (s *summary) markdownAt(level int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## deps %s\n\n", s.Command)
	fmt.Fprintf(&b, "**%s**\n\n", s.headline())
	if len(s.Groups) > 0 {
		if s.Run {
			b.WriteString("### Pull requests\n\n")
		} else {
			b.WriteString("### Pull requests `deps run` would open\n\n")
		}
	}
	for _, g := range s.Groups {
		fmt.Fprintf(&b, "#### %s\n\n", g.Title)
		notes := s.groupNotes(g, func(a bot.Action) string {
			if a.URL == "" {
				return ""
			}
			return fmt.Sprintf("[#%d](%s)", a.Number, a.URL)
		})
		fmt.Fprintf(&b, "Branch `%s`", g.Branch)
		for _, n := range notes {
			b.WriteString(" · " + n)
		}
		if level >= mdNoPRTables {
			fmt.Fprintf(&b, "\n\n%s (table left out: step summary size limit)\n\n", plural(len(g.Updates), "update", "updates"))
			continue
		}
		b.WriteString("\n\n| Dependency | File | From | To | Type |\n|---|---|---|---|---|\n")
		for _, u := range g.Updates {
			if u.Maintenance() {
				fmt.Fprintf(&b, "| `%s` | `%s` | | | lock file maintenance |\n", u.Dep.Name, u.Dep.File)
				continue
			}
			from, to := u.FromTo()
			fmt.Fprintf(&b, "| `%s` | `%s:%d` | `%s` | `%s` | %s |\n", u.Dep.Name, u.Dep.File, u.Dep.Line, from, to, updateType(u))
		}
		b.WriteString("\n")
	}
	if oa := s.others(); len(oa) > 0 {
		b.WriteString("### Other pull request actions\n\n| PR | Status | Link |\n|---|---|---|\n")
		for _, a := range oa {
			title := cmp.Or(a.Title, a.Branch)
			if a.Dashboard {
				title = "dashboard: " + title
			}
			link := ""
			if a.URL != "" {
				link = fmt.Sprintf("[#%d](%s)", a.Number, a.URL)
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", mdCell(title), a.Display(), link)
		}
		b.WriteString("\n")
	}
	if len(s.Res.Pending) > 0 && level >= mdNoPendingList {
		fmt.Fprintf(&b, "### Held back by minimumReleaseAge\n\n%s held back (list left out: step summary size limit; `-v` lists them in the log).\n\n",
			plural(len(s.Res.Pending), "release", "releases"))
	} else if len(s.Res.Pending) > 0 {
		fmt.Fprintf(&b, "### Held back by minimumReleaseAge\n\n<details><summary>%s held back (too new, or without a release time under timestamp-required)</summary>\n\n| Dependency | File | Version | Reason |\n|---|---|---|---|\n",
			plural(len(s.Res.Pending), "release", "releases"))
		for _, p := range s.Res.Pending {
			fmt.Fprintf(&b, "| `%s` | `%s:%d` | `%s` | %s |\n", p.Dep.Name, p.Dep.File, p.Dep.Line, p.Version, mdCell(p.Reason))
		}
		b.WriteString("\n</details>\n\n")
	}
	if rs := s.skipReasons(); len(rs) > 0 {
		b.WriteString("### Skipped\n\n| Reason | Count | How to update |\n|---|---|---|\n")
		for _, r := range rs {
			fmt.Fprintf(&b, "| %s | %d | %s |\n", mdCell(r.Reason), len(r.Deps), mdCell(skipHint(r.Reason)))
		}
		if level >= mdNoSkippedList {
			b.WriteString("\nThe skipped dependencies are left out (step summary size limit); `-v` lists them in the log.\n\n")
		} else {
			b.WriteString("\n<details><summary>Skipped dependencies</summary>\n\n")
			for _, r := range rs {
				fmt.Fprintf(&b, "**%s**\n\n", r.Reason)
				for _, d := range r.Deps {
					fmt.Fprintf(&b, "- `%s` `%s:%d`\n", d.Name, d.File, d.Line)
				}
				b.WriteString("\n")
			}
			b.WriteString("</details>\n\n")
		}
	}
	if len(s.Errors) > 0 {
		b.WriteString("### Errors\n\n```\n")
		for _, e := range s.Errors {
			fmt.Fprintln(&b, e)
		}
		b.WriteString("```\n\n")
	}
	return b.String()
}

// summaryPath is the CI step summary file: GITHUB_STEP_SUMMARY, else GITEA_STEP_SUMMARY.
func summaryPath(getenv func(string) string) string {
	return cmp.Or(getenv("GITHUB_STEP_SUMMARY"), getenv("GITEA_STEP_SUMMARY"))
}

// write prints the text summary to stderr and appends the markdown to the step summary file,
// if the runner provides one. The file is one step's summary, so what earlier commands of the
// step wrote counts against stepSummaryLimit.
func (s *summary) write(stderr io.Writer) {
	s.writeText(stderr)
	s.writeMachine(stderr, s.Report)
	p := summaryPath(os.Getenv)
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "deps: step summary: %v\n", err)
		return
	}
	md := s.markdown()
	if fi, serr := f.Stat(); serr == nil && fi.Size()+int64(len(md)) > stepSummaryLimit {
		md = s.markdownFit(stepSummaryLimit - int(min(fi.Size(), stepSummaryLimit)))
	}
	_, err = f.WriteString(md)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fmt.Fprintf(stderr, "deps: step summary: %v\n", err)
	}
}
