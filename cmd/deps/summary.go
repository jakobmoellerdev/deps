package main

import (
	"cmp"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/renovate"
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
	prs         map[string]int
	grouped     map[string]bool
	Actions     []bot.Action // run only
	Errors      []error      // engine and build/bot errors
	Elapsed     time.Duration
	ShowSkipped bool           // text lists the held-back releases, not only their count
	Report      string         // --report file; "" = none
	Unused      []int          // indices of the rules that matched no dependency (config.UnusedRules)
	Config      *config.Config // the loaded config, for the positions of Unused rules
	Style       logx.Style     // colors of the text; the markdown has none
	// Repository names the repository of a multi-repository run: the step summary heading
	// names it, and the run writes the report and step outputs of all repositories at its end
	// instead of write.
	Repository string
}

// plain is the style of markdown: no colors.
var plain logx.Style

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
	{"branch ref", "an action on a branch (`@main`); set `pin: digest` to pin it to the branch's commit"},
	{"pinned without version comment", "the comment after the SHA is no version; make it `# vX.Y.Z` (pre-commit: `# frozen: vX.Y.Z`) or drop it"},
	{"pseudo-version", "a commit pseudo-version; require a tagged version to get updates"},
	{"module path changed", "the new version's go.mod declares another module path; require that path to update"},
	{"conflicts with the inputs", "the compile inputs (requirements.in, constraints) do not admit the new version; relax them to update"},
	{"replaced", "a replace directive overrides the module"},
	{"variable reference", "the value is computed at runtime"},
	{"template value", "the value is computed at runtime"},
	{"expression", "the value is computed at runtime"},
	{dep.SkipUnknownRegistry + " ", "set `[registries.<name>] index` in `.cargo/config.toml` or `CARGO_REGISTRIES_<NAME>_INDEX`"},
	{dep.SkipUnknownRegistry, "set `registryUrls` in a rule matching the package (Dockerfile `apt-get install` pins, `apk add` pins of an unknown Alpine release)"},
	{"covered by custom match", "an earlier `custom` entry matches the same version; drop the duplicate entry, or give it that entry's datasource and name to share the version with a checksum of its own"},
	{"covered by ", "a custom manager updates these bytes instead"},
}

func skipHint(reason string) string {
	for _, h := range skipHints {
		if strings.HasPrefix(reason, h.prefix) {
			return h.hint
		}
	}
	return ""
}

// skipReasons aggregates the skipped deps by exact reason, most frequent first (the JSON
// report).
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

// skipRows aggregates the skipped deps for people, most frequent first: as skipReasons, but
// reasons that differ only in the line of the covering custom dep (`covered by custom match
// on line <n>`) share one row without the line, and the deps ignored by pre-policy rules
// share one row naming the rules, busiest first: `ignored by rules #56 (4), #51, #52`. The
// deps of a merged row are ordered by file and line and keep their exact reason in Skip.
func (s *summary) skipRows() []skipReason {
	var rules []skipReason // per `ignored by rule #<i>` reason
	idx := map[string]int{}
	var out []skipReason
	for _, r := range s.skipReasons() {
		if rest, ok := strings.CutPrefix(r.Reason, "ignored by rule #"); ok && !strings.Contains(rest, " ") {
			rules = append(rules, r)
			continue
		}
		key := r.Reason
		if i := strings.LastIndex(key, " on line "); strings.HasPrefix(key, "covered by ") && i > 0 {
			key = key[:i]
		}
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			out = append(out, skipReason{Reason: key})
		}
		out[i].Deps = append(out[i].Deps, r.Deps...)
	}
	if len(rules) > 0 {
		row := skipReason{Reason: rules[0].Reason}
		if len(rules) > 1 {
			names := make([]string, len(rules))
			for i, r := range rules {
				names[i] = strings.TrimPrefix(r.Reason, "ignored by rule ")
				if len(r.Deps) > 1 {
					names[i] += " (" + strconv.Itoa(len(r.Deps)) + ")"
				}
			}
			row.Reason = "ignored by rules " + strings.Join(names, ", ")
		}
		for _, r := range rules {
			row.Deps = append(row.Deps, r.Deps...)
		}
		out = append(out, row)
	}
	for _, r := range out {
		if slices.ContainsFunc(r.Deps, func(d dep.Dep) bool { return d.Skip != r.Reason }) {
			slices.SortStableFunc(r.Deps, func(a, b dep.Dep) int { return cmp.Or(strings.Compare(a.File, b.File), a.Line-b.Line) })
		}
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

// others are the run outcomes without a group: closed obsolete PRs, obsolete PRs kept open by
// the stopUpdatingLabel, and the dashboard.
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

// headline is the one-line overview, or the empty-case sentence; st paints the errors red.
func (s *summary) headline(st logx.Style) string {
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
	errs := plural(len(s.Errors), "error", "errors")
	if len(s.Errors) > 0 {
		errs = st.Paint(logx.Red, errs)
	}
	parts = append(parts, fmt.Sprintf("%d skipped", len(s.Res.Skipped)), errs)
	return strings.Join(parts, ", ") + " (" + s.Elapsed.Round(time.Millisecond).String() + ")"
}

// typeColors are the colors of the update types in the text summary, by risk.
var typeColors = map[dep.UpdateType]string{dep.Major: logx.Red, dep.Minor: logx.Yellow, dep.Patch: logx.Green, dep.Digest: logx.Cyan}

// updateType renders the type with a lockfile-only marker and the security advisories
// ("patch, lockfile, security: GHSA-1"); st colors the type and the advisories.
func updateType(u dep.Update, st logx.Style) string {
	t := st.Paint(typeColors[u.Type], string(u.Type))
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
	return t + ", " + st.Paint(logx.BoldRed, "security: "+strings.Join(ids, ","))
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
		for _, o := range []string{a.Automerge, a.PRWait} {
			if o != "" {
				notes = append(notes, o)
			}
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
		if g.DashboardApproval {
			notes = append(notes, "dashboard approval")
		}
		if g.Draft {
			notes = append(notes, "draft")
		}
		if g.PRCreation != "" && g.PRCreation != config.PRCreationImmediate && !g.AutomergeBranch {
			notes = append(notes, "prCreation "+g.PRCreation)
		}
		if sch := g.Updates[0].Schedule; sch != nil {
			notes = append(notes, "schedule "+sch.String())
		}
	}
	return notes
}

// unusedRule is a rule that matched no dependency, as the summary names it.
type unusedRule struct {
	index int
	where string // "<file>:<line>"; in compatibility mode "<Renovate file> <origin>"; "rules[<i>]" when unknown
	line  bool   // where has the rule's line in the config (for an annotation)
	match string // its match fields, "managers=[npm] names=[react]"
}

// unusedRules splits Unused into the rules the repository's config wrote itself, which the
// summary warns about, and the count of the others: rules of extended configs and Renovate
// presets, and those deps generates for Renovate's defaults (renovate.Implicit). Those are
// written for many repositories, so matching nothing in this one is expected.
func (s *summary) unusedRules() (own []unusedRule, other int) {
	c := s.Config
	for _, i := range s.Unused {
		u := unusedRule{index: i, where: fmt.Sprintf("rules[%d]", i)}
		if c == nil || i >= len(c.Rules) {
			own = append(own, u)
			continue
		}
		r := &c.Rules[i]
		file, line := r.Source()
		comment := r.Comment()
		if file != "" && !slices.Contains(c.Files(), file) || renovate.Implicit(comment) {
			other++
			continue
		}
		switch {
		case renovate.IsConfigName(filepath.Base(file)) && comment != "":
			u.where = file + " " + comment // the line is the translation's
		case file != "":
			u.where, u.line = file+":"+strconv.Itoa(line), true
		}
		match, _, _ := strings.Cut(strings.TrimPrefix(describeRule(*r), "match: "), "; apply:")
		if match != "all" {
			u.match = match
		}
		own = append(own, u)
	}
	return own, other
}

// writeText writes the warnings about unused rules and the summary block for stderr. It goes
// before the counts line, so that line stays last. Skip reasons are counts only: --show-skipped prints the
// per-dep skip lines; errors are already printed as error lines.
func (s *summary) writeText(w io.Writer) {
	own, other := s.unusedRules()
	for _, u := range own {
		fmt.Fprintf(w, "warn    rule %s matched no dependency", u.where)
		if u.match != "" {
			fmt.Fprintf(w, " (%s)", u.match)
		}
		fmt.Fprintln(w)
		if u.line {
			actionsLog.configWarning(s.Config, fmt.Sprintf("rules[%d]", u.index), "matched no dependency")
		}
	}
	if other > 0 {
		slog.Info("rules of presets, extended configs and Renovate defaults matched no dependency", "rules", other)
	}
	st := s.Style
	fmt.Fprintf(w, "\n%s %s\n", st.Paint(logx.Bold, "Summary:"), s.headline(st))
	for _, g := range s.Groups {
		notes := s.textNotes(g)
		fmt.Fprintf(w, "  PR %s  %s", st.Paint(logx.Accent, g.Branch), g.Title)
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
			fmt.Fprintf(w, "      %s  %s  %s -> %s  (%s)\n", u.Dep.Name, st.Paint(logx.Dim, u.Dep.File+":"+strconv.Itoa(u.Dep.Line)), from, to, updateType(u, st))
		}
	}
	switch {
	case len(s.Res.Pending) > 0 && !s.ShowSkipped:
		fmt.Fprintf(w, "  Held back by minimumReleaseAge: %d (--show-skipped lists each)\n", len(s.Res.Pending))
	case len(s.Res.Pending) > 0:
		fmt.Fprintln(w, "  Held back by minimumReleaseAge:")
		for _, p := range s.Res.Pending {
			fmt.Fprintf(w, "      %s  %s:%d  %s  %s\n", p.Dep.Name, p.Dep.File, p.Dep.Line, p.Version, p.Reason)
		}
	}
	switch {
	case len(s.Res.Warnings) > 0 && !s.ShowSkipped:
		fmt.Fprintf(w, "  Deprecated or abandoned: %d (--show-skipped lists each)\n", len(s.Res.Warnings))
	case len(s.Res.Warnings) > 0:
		fmt.Fprintln(w, "  Deprecated or abandoned:")
		for _, d := range s.Res.Warnings {
			fmt.Fprintf(w, "      %s  %s:%d  %s: %s\n", d.Dep.Name, d.Dep.File, d.Dep.Line, d.Kind, d.Reason)
		}
	}
	if rs := s.skipRows(); len(rs) > 0 {
		fmt.Fprintln(w, "  Skipped (--show-skipped lists each dependency):")
		for _, r := range rs {
			fmt.Fprintf(w, "    %4d  %s", len(r.Deps), r.Reason)
			if h := skipHint(r.Reason); h != "" {
				fmt.Fprintf(w, "  (%s)", strings.ReplaceAll(h, "`", ""))
			}
			fmt.Fprintln(w)
		}
	}
}

// textNotes are the groupNotes of the text summary. In run the `pr` and `dashboard` result
// lines already give each action's status, automerge and PR creation outcome and URL, so the
// summary adds only what they lack: why an update cannot be applied (or the stop-updating
// label) and the schedule entries a group waits for. The actions without a group (closed
// obsolete PRs, the dashboard) are not repeated at all.
func (s *summary) textNotes(g engine.Group) []string {
	if !s.Run {
		return s.groupNotes(g, func(bot.Action) string { return "" })
	}
	a, ok := s.action(g)
	if !ok {
		return []string{"not processed (see errors)"}
	}
	var notes []string
	if a.Reason != "" {
		notes = append(notes, a.Reason)
	}
	if len(a.Waits) > 0 {
		notes = append(notes, "waiting for "+strings.Join(a.Waits, " or "))
	}
	return notes
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
	return truncateMarkdown(md, limit, "\n\n**Step summary truncated to its size limit; the job log has everything.**\n")
}

// truncateMarkdown cuts md to at most limit bytes ending with note: at the last line boundary
// that leaves room for the note, else at a rune boundary. It is "" when not even the note fits.
func truncateMarkdown(md string, limit int, note string) string {
	if len(md) <= limit {
		return md
	}
	cut := limit - len(note)
	if cut < 0 {
		return ""
	}
	if i := strings.LastIndexByte(md[:cut], '\n'); i >= 0 {
		cut = i
	}
	for cut > 0 && !utf8.RuneStart(md[cut]) {
		cut--
	}
	return md[:cut] + note
}

// markdownAt renders the step summary at a detail level.
func (s *summary) markdownAt(level int) string {
	var b strings.Builder
	if s.Repository != "" {
		fmt.Fprintf(&b, "## deps %s: %s\n\n", s.Command, s.Repository)
	} else {
		fmt.Fprintf(&b, "## deps %s\n\n", s.Command)
	}
	fmt.Fprintf(&b, "**%s**\n\n", s.headline(plain))
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
				fmt.Fprintf(&b, "| `%s` | `%s` | | | lock file maintenance |\n", mdCell(u.Dep.Name), mdCell(u.Dep.File))
				continue
			}
			from, to := u.FromTo()
			fmt.Fprintf(&b, "| `%s` | `%s:%d` | `%s` | `%s` | %s |\n", mdCell(u.Dep.Name), mdCell(u.Dep.File), u.Dep.Line, mdCell(from), mdCell(to), mdCell(updateType(u, plain)))
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
			fmt.Fprintf(&b, "| %s | %s | %s |\n", mdCell(title), mdCell(a.Display()), link)
		}
		b.WriteString("\n")
	}
	if len(s.Res.Pending) > 0 && level >= mdNoPendingList {
		fmt.Fprintf(&b, "### Held back by minimumReleaseAge\n\n%s held back (list left out: step summary size limit; `--show-skipped` lists them in the log).\n\n",
			plural(len(s.Res.Pending), "release", "releases"))
	} else if len(s.Res.Pending) > 0 {
		fmt.Fprintf(&b, "### Held back by minimumReleaseAge\n\n<details><summary>%s held back (too new, or without a release time under timestamp-required)</summary>\n\n| Dependency | File | Version | Reason |\n|---|---|---|---|\n",
			plural(len(s.Res.Pending), "release", "releases"))
		for _, p := range s.Res.Pending {
			fmt.Fprintf(&b, "| `%s` | `%s:%d` | `%s` | %s |\n", mdCell(p.Dep.Name), mdCell(p.Dep.File), p.Dep.Line, mdCell(p.Version), mdCell(p.Reason))
		}
		b.WriteString("\n</details>\n\n")
	}
	if rs := s.skipRows(); len(rs) > 0 {
		b.WriteString("### Skipped\n\n| Reason | Count | How to update |\n|---|---|---|\n")
		for _, r := range rs {
			fmt.Fprintf(&b, "| %s | %d | %s |\n", mdCell(r.Reason), len(r.Deps), mdCell(skipHint(r.Reason)))
		}
		if level >= mdNoSkippedList {
			b.WriteString("\nThe skipped dependencies are left out (step summary size limit); `--show-skipped` lists them in the log.\n\n")
		} else {
			b.WriteString("\n<details><summary>Skipped dependencies</summary>\n\n")
			for _, r := range rs {
				fmt.Fprintf(&b, "**%s**\n\n", r.Reason)
				for _, d := range r.Deps {
					fmt.Fprintf(&b, "- `%s` `%s:%d`", d.Name, d.File, d.Line)
					if d.Skip != r.Reason {
						fmt.Fprintf(&b, " (%s)", d.Skip)
					}
					b.WriteString("\n")
				}
				b.WriteString("\n")
			}
			b.WriteString("</details>\n\n")
		}
	}
	if own, _ := s.unusedRules(); len(own) > 0 {
		b.WriteString("### Warnings\n\n")
		for _, u := range own {
			fmt.Fprintf(&b, "- rule `%s` matched no dependency", mdCell(u.where))
			if u.match != "" {
				fmt.Fprintf(&b, " (`%s`)", mdCell(u.match))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
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
// if the runner provides one.
func (s *summary) write(stderr io.Writer) {
	s.writeText(stderr)
	appendStepSummary(stderr, s.markdownFit)
}

// appendStepSummary appends fit(room) to the step summary file, if the runner provides one.
// The file is one step's summary, so room is what earlier commands of the step left of
// stepSummaryLimit; fit returns "" when nothing fits.
func appendStepSummary(stderr io.Writer, fit func(room int) string) {
	p := summaryPath(os.Getenv)
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "deps: step summary: %v\n", err)
		return
	}
	room := stepSummaryLimit
	if fi, serr := f.Stat(); serr == nil {
		room -= int(min(fi.Size(), stepSummaryLimit))
	}
	md := fit(room)
	if md == "" {
		fmt.Fprintln(stderr, "deps: step summary: no room")
	} else {
		_, err = f.WriteString(md)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fmt.Fprintf(stderr, "deps: step summary: %v\n", err)
	}
}
