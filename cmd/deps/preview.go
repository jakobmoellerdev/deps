package main

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func newPreview(o *rootOpts) *cli.Command {
	var base string
	return &cli.Command{
		Name:  "preview",
		Usage: "Compare the PRs a pull request's checkout would get with those of the base branch",
		Metadata: map[string]any{docgen.MetaExample: "  # in a pull_request job with the PR checked out in pr/ and the base branch in base/\n" +
			"  deps preview --dir pr --base-dir base"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if base == "" {
				return &usageError{errors.New("--base-dir is required")}
			}
			return preview(ctx, cmd, o, base)
		},
		Flags: append(scanFlags(o, scanSession),
			&cli.StringFlag{Name: "base-dir", Destination: &base, Usage: "checkout of the base branch to compare with (required)"},
		),
	}
}

// previewPR is one PR of either side, keyed by branch.
type previewPR struct {
	Branch, Title string
	Updates       []dep.Update
}

// previewDiff is what the PR changes about the update PRs.
type previewDiff struct {
	Added, Removed []previewPR
	Changed        []previewChange
	Unchanged      int
}

type previewChange struct{ Base, Head previewPR }

func previewPRs(res *engine.Result, prefix string) map[string]previewPR {
	out := map[string]previewPR{}
	for _, g := range engine.GroupUpdates(res.Updates, prefix) {
		out[g.Branch] = previewPR{Branch: g.Branch, Title: g.Title, Updates: g.Updates}
	}
	return out
}

// updateKey identifies an update for the comparison: what it changes, not where it was found
// (a PR that only moves a line still opens the same PRs).
func updateKey(u dep.Update) string {
	from, to := u.FromTo()
	return u.Dep.Manager + " " + u.Dep.File + " " + u.Dep.Name + " " + from + " " + to + " " + string(u.Type)
}

func samePR(a, b previewPR) bool {
	if a.Title != b.Title || len(a.Updates) != len(b.Updates) {
		return false
	}
	ka, kb := make([]string, len(a.Updates)), make([]string, len(b.Updates))
	for i := range a.Updates {
		ka[i], kb[i] = updateKey(a.Updates[i]), updateKey(b.Updates[i])
	}
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}

func diffPreview(base, head map[string]previewPR) previewDiff {
	var d previewDiff
	for _, b := range slices.Sorted(maps.Keys(head)) {
		h := head[b]
		switch old, ok := base[b]; {
		case !ok:
			d.Added = append(d.Added, h)
		case samePR(old, h):
			d.Unchanged++
		default:
			d.Changed = append(d.Changed, previewChange{Base: old, Head: h})
		}
	}
	for _, b := range slices.Sorted(maps.Keys(base)) {
		if _, ok := head[b]; !ok {
			d.Removed = append(d.Removed, base[b])
		}
	}
	return d
}

func (d previewDiff) empty() bool { return len(d.Added)+len(d.Removed)+len(d.Changed) == 0 }

func (d previewDiff) headline() string {
	if d.empty() {
		return fmt.Sprintf("No change to the update PRs (%d the same).", d.Unchanged)
	}
	return fmt.Sprintf("%d new, %d changed, %d no longer opened, %d the same", len(d.Added), len(d.Changed), len(d.Removed), d.Unchanged)
}

func writePRTable(b *strings.Builder, p previewPR) {
	fmt.Fprintf(b, "`%s`\n\n| Dependency | File | From | To | Type |\n|---|---|---|---|---|\n", p.Branch)
	for _, u := range p.Updates {
		if u.Maintenance() {
			fmt.Fprintf(b, "| `%s` | `%s` | | | lock file maintenance |\n", mdCell(u.Dep.Name), mdCell(u.Dep.File))
			continue
		}
		from, to := u.FromTo()
		fmt.Fprintf(b, "| `%s` | `%s:%d` | `%s` | `%s` | %s |\n", mdCell(u.Dep.Name), mdCell(u.Dep.File), u.Dep.Line, mdCell(from), mdCell(to), mdCell(updateType(u, plain)))
	}
	b.WriteString("\n")
}

// markdown renders the preview; errs are the lookup errors of the PR's checkout.
func (d previewDiff) markdown(errs []error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## deps preview\n\n**%s**\n\n", d.headline())
	if len(d.Added) > 0 {
		b.WriteString("### New PRs\n\n")
		for _, p := range d.Added {
			fmt.Fprintf(&b, "#### %s\n\n", p.Title)
			writePRTable(&b, p)
		}
	}
	if len(d.Changed) > 0 {
		b.WriteString("### Changed PRs\n\n")
		for _, c := range d.Changed {
			fmt.Fprintf(&b, "#### %s\n\n", c.Head.Title)
			if c.Base.Title != c.Head.Title {
				fmt.Fprintf(&b, "Was: %s\n\n", c.Base.Title)
			}
			writePRTable(&b, c.Head)
		}
	}
	if len(d.Removed) > 0 {
		b.WriteString("### No longer opened\n\nOpen PRs of these branches would be closed as obsolete.\n\n")
		for _, p := range d.Removed {
			fmt.Fprintf(&b, "- %s (`%s`)\n", p.Title, p.Branch)
		}
		b.WriteString("\n")
	}
	if len(errs) > 0 {
		b.WriteString("### Errors\n\n```\n")
		for _, e := range errs {
			fmt.Fprintln(&b, e)
		}
		b.WriteString("```\n")
	}
	return b.String()
}

// truncateMarkdown cuts md at a line boundary to at most limit bytes, with a note.
func truncateMarkdown(md string, limit int) string {
	if len(md) <= limit {
		return md
	}
	const note = "\n\n**Truncated; the job log has everything.**\n"
	cut := max(limit-len(note), 0)
	if i := strings.LastIndexByte(md[:cut], '\n'); i >= 0 {
		cut = i
	}
	return md[:cut] + note
}

func preview(ctx context.Context, cmd *cli.Command, o *rootOpts, baseDir string) error {
	start := time.Now()
	stderr := cmd.Root().ErrWriter
	info, _ := platform.FromEnv(os.Getenv)
	token := envToken()
	client := newHTTPClient()
	head, err := newSession(ctx, o, info, token, client, stderr)
	if err != nil {
		return err
	}
	defer head.close()
	base, err := head.sibling(ctx, baseDir)
	if err != nil {
		return fmt.Errorf("base checkout %s: %w", baseDir, err)
	}
	headRes, err := head.resolve(ctx)
	if err != nil {
		return err
	}
	baseRes, err := base.resolve(ctx)
	if err != nil {
		return fmt.Errorf("base checkout %s: %w", baseDir, err)
	}
	d := diffPreview(previewPRs(baseRes, base.cfg.Prefix()), previewPRs(headRes, head.cfg.Prefix()))
	for _, p := range d.Added {
		fmt.Fprintf(stderr, "new     %s  %s\n", p.Branch, p.Title)
	}
	for _, c := range d.Changed {
		fmt.Fprintf(stderr, "changed %s  %s\n", c.Head.Branch, c.Head.Title)
	}
	for _, p := range d.Removed {
		fmt.Fprintf(stderr, "removed %s  %s\n", p.Branch, p.Title)
	}
	for _, e := range headRes.Errors {
		reportError(stderr, o.term, e)
	}
	md := d.markdown(headRes.Errors)
	if p := summaryPath(os.Getenv); p != "" {
		if err := appendFile(p, truncateMarkdown(md, stepSummaryBudget)); err != nil {
			fmt.Fprintf(stderr, "deps: step summary: %v\n", err)
		}
	}
	if sha := pullRequestHead(os.Getenv); sha != "" && info.Kind == platform.GitHub {
		conclusion := "success"
		if len(headRes.Errors) > 0 {
			conclusion = "neutral"
		}
		url, err := platform.CreateCheckRun(ctx, info, token, client, platform.CheckRun{
			Name: "deps preview", HeadSHA: sha, Conclusion: conclusion,
			Title: d.headline(), Summary: truncateMarkdown(md, platform.CheckRunSummaryLimit),
		})
		switch {
		case errors.Is(err, platform.ErrNoCheckRuns):
			fmt.Fprintf(stderr, "note    no check run (the token needs checks: write; PATs cannot create check runs): %v\n", err)
		case err != nil:
			fmt.Fprintf(stderr, "error   check run: %v\n", err)
			headRes.Errors = append(headRes.Errors, err)
		default:
			fmt.Fprintf(stderr, "check   %s\n", url)
		}
	}
	fmt.Fprintf(stderr, "%s (%s)\n", d.headline(), time.Since(start).Round(time.Millisecond))
	if len(headRes.Errors) > 0 {
		return errReported
	}
	return nil
}

// stepSummaryBudget bounds the preview in the step summary (GitHub accepts 1 MiB per step).
const stepSummaryBudget = 1 << 20

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(s)
	return cmp.Or(err, f.Close())
}

// pullRequestHead is the head commit of the pull request that triggered the job (from the
// pull_request or pull_request_target payload at GITHUB_EVENT_PATH; GITHUB_SHA is the merge
// commit there), "" for other events.
func pullRequestHead(getenv func(string) string) string {
	switch getenv("GITHUB_EVENT_NAME") {
	case "pull_request", "pull_request_target":
	default:
		return ""
	}
	b, err := os.ReadFile(getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return ""
	}
	var ev struct {
		PullRequest struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(b, &ev) != nil {
		return ""
	}
	return ev.PullRequest.Head.SHA
}
