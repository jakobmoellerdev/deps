package main

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func newPreview(o *rootOpts) *cli.Command {
	var base, baseBranch string
	var pr int
	return &cli.Command{
		Name:  "preview",
		Usage: "Compare the PRs a pull request's checkout would get with those of the base branch",
		Metadata: map[string]any{docgen.MetaExample: "  # in a pull_request job with the PR checked out in pr/ and the base branch in base/\n" +
			"  deps preview --dir pr --base-dir base\n" +
			"  # without checkouts: deps clones the pull request and its base itself\n" +
			"  deps preview --repositories my-org/app --pr 42"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			entries := repositoryEntries(cmd, o.repositories)
			switch {
			case len(entries) > 0 && base != "":
				return &usageError{errors.New("--repositories and --base-dir are mutually exclusive: deps clones both sides itself")}
			case len(entries) > 0 && pr == 0:
				return &usageError{errors.New("--repositories needs --pr <number>: the pull request to preview")}
			case len(entries) > 0:
				return previewRemote(ctx, cmd, o, entries, pr)
			case pr != 0:
				return &usageError{errors.New("--pr needs --repositories")}
			case base == "":
				return &usageError{errors.New("--base-dir is required")}
			}
			return preview(ctx, cmd, o, base, cmp.Or(baseBranch, pullRequestBase(os.Getenv)))
		},
		Flags: append(scanFlags(o, scanSession),
			&cli.StringFlag{Name: "base-dir", Destination: &base, Usage: "checkout of the base branch to compare with (required without --repositories)"},
			&cli.StringFlag{Name: "base", Destination: &baseBranch, Usage: "base branch the pull request targets, resolved as run resolves it: its rules[].baseBranches match, and with useBaseBranchConfig on the default branch the config of both checkouts is merged over the default branch's (default: $GITHUB_BASE_REF, then $CI_MERGE_REQUEST_TARGET_BRANCH_NAME)"},
			repositoriesFlag(&o.repositories, "preview a pull request of"),
			&cli.IntFlag{Name: "pr", Destination: &pr, Usage: "number of the pull request (GitLab: merge request) to preview in the repository of --repositories; deps clones its head and its base branch itself"},
		),
	}
}

// previewRemote previews pull request number of the single repository of entries: it reads
// the pull request from the forge, clones its head ref and its base branch into temporary
// directories, and compares them as preview does for two checkouts.
func previewRemote(ctx context.Context, cmd *cli.Command, o *rootOpts, entries []string, number int) error {
	stderr := cmd.Root().ErrWriter
	token, client := envToken(), newHTTPClient()
	src, err := newRemoteSource(ctx, o, entries, token, client)
	if err != nil {
		return err
	}
	targets, err := src.targets(ctx)
	if err != nil {
		return err
	}
	if len(targets) != 1 {
		return &usageError{fmt.Errorf("--pr needs exactly one repository, got %d", len(targets))}
	}
	t := targets[0]
	if t.Info.APIURL == "" {
		return fmt.Errorf("--pr: no forge API for %s: give the repository as owner/name on a known forge", t.URL)
	}
	pr, err := platform.NewClient(t.Info, token, client).GetPR(ctx, number)
	if err != nil {
		return fmt.Errorf("pull request %d of %s: %w", number, t.Name, err)
	}
	branch := pr.Base
	if branch == "" {
		return fmt.Errorf("pull request %d of %s: the forge does not report its base branch", number, t.Name)
	}
	fmt.Fprintf(stderr, "pull request %s#%d  %s -> %s\n", t.Name, number, cmp.Or(pr.Head, pr.HeadSHA), branch)
	work, err := os.MkdirTemp("", "deps-preview-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	header := gitAuthHeader(token)
	headRef := fmt.Sprintf("refs/pull/%d/head", number)
	if t.Info.Kind == platform.GitLab {
		headRef = fmt.Sprintf("refs/merge-requests/%d/head", number)
	}
	dirs := map[string]string{}
	for name, ref := range map[string]string{"head": headRef, "base": branch} {
		dir := filepath.Join(work, name)
		ct := t
		ct.Ref = ref
		if err := cloneRepository(ctx, ct, header, dir); err != nil {
			return fmt.Errorf("clone %s (%s): %w", name, ref, err)
		}
		dirs[name] = filepath.Join(dir, filepath.FromSlash(t.Path))
	}
	ro := *o
	ro.dir = dirs["head"]
	return preview(ctx, cmd, &ro, dirs["base"], branch)
}

// previewPR is one PR of either side, keyed by branch.
type previewPR struct {
	Branch, Title string
	Updates       []dep.Update
}

// previewDiff is what the PR changes about the update PRs.
type previewDiff struct {
	Added, Removed []previewPR
	// Unknown are head PRs without a base counterpart whose dependencies the base checkout
	// failed to extract or look up: they may well be open on the base branch already.
	Unknown   []previewPR
	Changed   []previewChange
	Unchanged int
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

// diffPreview compares the PRs of both sides; baseErrs are the errors of the base checkout.
func diffPreview(base, head map[string]previewPR, baseErrs []error) previewDiff {
	var d previewDiff
	failed := failedOnBase(baseErrs)
	for _, b := range slices.Sorted(maps.Keys(head)) {
		h := head[b]
		switch old, ok := base[b]; {
		case !ok && slices.ContainsFunc(h.Updates, failed):
			d.Unknown = append(d.Unknown, h)
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

// failedOnBase reports whether the base checkout failed to extract the file of an update
// (engine.FileError) or to look up its dependency (engine.DepError).
func failedOnBase(errs []error) func(dep.Update) bool {
	files, deps := map[string]bool{}, map[[2]string]bool{}
	for _, err := range errs {
		if fe, ok := errors.AsType[*engine.FileError](err); ok {
			files[fe.File] = true
		}
		if de, ok := errors.AsType[*engine.DepError](err); ok {
			deps[[2]string{de.File, de.Name}] = true
		}
	}
	return func(u dep.Update) bool { return files[u.Dep.File] || deps[[2]string{u.Dep.File, u.Dep.Name}] }
}

func (d previewDiff) empty() bool {
	return len(d.Added)+len(d.Removed)+len(d.Changed)+len(d.Unknown) == 0
}

func (d previewDiff) headline() string {
	if d.empty() {
		return fmt.Sprintf("No change to the update PRs (%d the same).", d.Unchanged)
	}
	h := fmt.Sprintf("%d new, %d changed, %d no longer opened, %d the same", len(d.Added), len(d.Changed), len(d.Removed), d.Unchanged)
	if len(d.Unknown) > 0 {
		h += fmt.Sprintf(", %d not compared", len(d.Unknown))
	}
	return h
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

// markdown renders the preview; errs are the lookup errors of the PR's checkout, baseErrs
// those of the base checkout (rate-limited lookups among either are listed apart, as
// warnings), base the line naming the base branch and its config files (previewBases), ""
// when unknown.
func (d previewDiff) markdown(errs, baseErrs []error, base string) string {
	errs, limited := splitRateLimited(errs)
	baseErrs, baseLimited := splitRateLimited(baseErrs)
	for _, e := range baseLimited {
		limited = append(limited, fmt.Errorf("base checkout: %w", e))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## deps preview\n\n**%s**\n\n", d.headline())
	if base != "" {
		fmt.Fprintf(&b, "%s\n\n", base)
	}
	if len(d.Added) > 0 {
		b.WriteString("### New PRs\n\n")
		for _, p := range d.Added {
			fmt.Fprintf(&b, "#### %s\n\n", p.Title)
			writePRTable(&b, p)
		}
	}
	if len(d.Unknown) > 0 {
		b.WriteString("### Not compared\n\nThe base checkout failed to extract or look up dependencies of these PRs, so they may already be open.\n\n")
		for _, p := range d.Unknown {
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
	if len(limited) > 0 {
		fmt.Fprintf(&b, "### Rate limited\n\n%s skipped: %s.\n\n```\n", plural(len(limited), "lookup", "lookups"), rateLimitHint)
		for _, e := range limited {
			fmt.Fprintln(&b, e)
		}
		b.WriteString("```\n")
	}
	if len(errs) > 0 {
		b.WriteString("### Errors\n\n```\n")
		for _, e := range errs {
			fmt.Fprintln(&b, e)
		}
		b.WriteString("```\n")
	}
	if len(baseErrs) > 0 {
		b.WriteString("### Errors of the base checkout\n\n```\n")
		for _, e := range baseErrs {
			fmt.Fprintln(&b, e)
		}
		b.WriteString("```\n")
	}
	return b.String()
}

// previewTruncated ends a preview cut to a size limit.
const previewTruncated = "\n\n**Truncated; the job log has everything.**\n"

// preview compares the update PRs of the checkouts of a pull request (--dir) and of its base
// branch (baseDir); branch, when known, is the base branch the pull request targets
// (previewBases).
func preview(ctx context.Context, cmd *cli.Command, o *rootOpts, baseDir, branch string) error {
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
	note := ""
	if branch != "" {
		if head, base, err = previewBases(ctx, head, base, branch); err != nil {
			return err
		}
		fmt.Fprintln(stderr, baseLine(branch, base.cfg))
		note = "Base branch `" + mdCell(branch) + "`"
		if files := base.cfg.Files(); len(files) > 0 {
			note += ", config `" + strings.Join(files, "` + `") + "`"
		}
		note += "."
	}
	headRes, err := head.resolve(ctx)
	if err != nil {
		return err
	}
	baseRes, err := base.resolve(ctx)
	if err != nil {
		return fmt.Errorf("base checkout %s: %w", baseDir, err)
	}
	d := diffPreview(previewPRs(baseRes, base.cfg.Prefix()), previewPRs(headRes, head.cfg.Prefix()), baseRes.Errors)
	for _, p := range d.Added {
		fmt.Fprintf(stderr, "new     %s  %s\n", p.Branch, p.Title)
	}
	for _, p := range d.Unknown {
		fmt.Fprintf(stderr, "unknown %s  %s\n", p.Branch, p.Title)
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
	for _, e := range baseRes.Errors {
		reportError(stderr, o.term, fmt.Errorf("base checkout: %w", e))
	}
	md := d.markdown(headRes.Errors, baseRes.Errors, note)
	appendStepSummary(stderr, func(room int) string { return truncateMarkdown(md, room, previewTruncated) })
	if sha := pullRequestHead(os.Getenv); sha != "" && info.Kind == platform.GitHub {
		conclusion := "success"
		if len(headRes.Errors)+len(baseRes.Errors) > 0 {
			conclusion = "neutral"
		}
		url, err := platform.CreateCheckRun(ctx, info, token, client, platform.CheckRun{
			Name: "deps preview", HeadSHA: sha, Conclusion: conclusion,
			Title: d.headline(), Summary: truncateMarkdown(md, platform.CheckRunSummaryLimit, previewTruncated),
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
	// Rate-limited lookups are warnings (reportError): they do not fail the preview.
	if headErrs, _ := splitRateLimited(headRes.Errors); len(headErrs) > 0 {
		return errReported
	}
	return nil
}

// previewBases returns the sessions of the pull request's checkout head and of its base
// branch's checkout base resolved as run resolves base branch branch: their rules with
// baseBranches match it, and when it is not the default branch (origin's HEAD) and the default
// branch's config file (of the name of each checkout's) sets useBaseBranchConfig, each
// checkout's config file is merged over the default branch's config (mergeBaseConfig). When
// the default branch or its config cannot be read, each checkout keeps its own config, with
// a warning.
func previewBases(ctx context.Context, head, base *session, branch string) (*session, *session, error) {
	over := defaultBranchConfig(ctx, head, branch)
	if over == nil {
		return head.forBase(head.root, branch), base.forBase(base.root, branch), nil
	}
	sides := []*session{head, base}
	for i, s := range sides {
		cfg, err := s.mergeBaseConfig(ctx, over, branch, s.root)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", s.root, err)
		}
		if cfg != over {
			if s, err = s.withConfig(ctx, s.root, cfg, s.info); err != nil {
				return nil, nil, fmt.Errorf("%s: %w", s.root, err)
			}
		}
		sides[i] = s.forBase(s.root, branch)
	}
	return sides[0], sides[1], nil
}

// defaultBranchConfig returns the config of the default branch of head's origin when branch
// is not the default branch and that config sets useBaseBranchConfig to merge or
// merge-if-present: the file of the name of head's config file at the default branch's
// fetched tip, named <default>:<name>. Otherwise, or when it cannot be read, nil. With
// --renovate it is nil: preview reads the base checkout's .deps.yaml, not its Renovate config.
func defaultBranchConfig(ctx context.Context, head *session, branch string) *config.Config {
	name := head.configName()
	if name == "" {
		return nil
	}
	if head.readConfig != nil {
		slog.InfoContext(ctx, "preview --renovate does not apply useBaseBranchConfig")
		return nil
	}
	repo := gitx.Repo{Dir: head.root}
	def, err := repo.DefaultBranch(ctx)
	if err != nil {
		slog.WarnContext(ctx, "default branch unknown, the checkouts keep their config", "err", err)
		return nil
	}
	if def == branch {
		return nil
	}
	tip, err := repo.FetchBranch(ctx, def)
	if err != nil {
		slog.WarnContext(ctx, "default branch not fetched, the checkouts keep their config", "branch", def, "err", err)
		return nil
	}
	src, err := repo.ReadFile(ctx, tip, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		slog.WarnContext(ctx, "default branch config not read, the checkouts keep their config", "branch", def, "err", err)
		return nil
	}
	display := def + ":" + name
	cfg, err := config.Read(ctx, src, display, display, head.fetcher)
	if err != nil {
		slog.WarnContext(ctx, "default branch config invalid, the checkouts keep their config", "branch", def, "err", err)
		return nil
	}
	if cfg.UseBaseBranchConfig != config.BaseConfigMerge && cfg.UseBaseBranchConfig != config.BaseConfigMergeIfPresent {
		return nil
	}
	return cfg
}

// pullRequestBase is the base branch of the pull request that triggered the job:
// $GITHUB_BASE_REF (GitHub, Gitea and Forgejo Actions), else
// $CI_MERGE_REQUEST_TARGET_BRANCH_NAME (GitLab CI); "" for other events.
func pullRequestBase(getenv func(string) string) string {
	return cmp.Or(getenv("GITHUB_BASE_REF"), getenv("CI_MERGE_REQUEST_TARGET_BRANCH_NAME"))
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
