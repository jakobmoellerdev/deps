// Package bot turns resolved updates into one branch and one pull request per dependency.
package bot

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// Git is the git plumbing the bot needs; implemented by gitx.Repo, and for platformCommit by
// gitx.Repo with Commit, Push and BranchTip going through the GitHub API (platform.GitHubCommits).
type Git interface {
	RevParse(ctx context.Context, rev string) (string, error)
	RemoteURL(ctx context.Context) (string, error)
	LsRemote(ctx context.Context, pattern string) (map[string]string, error)
	// ObjectIDs returns the object ids at paths (slash paths relative to the root, "." the
	// root) in commit rev, in order; "" where rev has none. Input fingerprints use them.
	ObjectIDs(ctx context.Context, rev string, paths []string) ([]string, error)
	// WriteTree returns the id of the tree of parent with changes applied, computed locally;
	// it equals the tree of the commit Commit creates from them.
	WriteTree(ctx context.Context, parent string, changes []engine.FileChange) (string, error)
	Commit(ctx context.Context, parent string, changes []engine.FileChange, msg, name, email string) (string, error)
	// Push wraps gitx.ErrStale when the branch moved away from expected and
	// gitx.ErrRejected when origin declined the update.
	Push(ctx context.Context, commit, branch, expected string) error
	DeleteBranch(ctx context.Context, branch string) error
	BranchTip(ctx context.Context, branch string) (platform.Tip, error)
}

// Options configure Run.
type Options struct {
	AuthorName, AuthorEmail string
	BranchPrefix            string // managed branch prefix, ending in '/'; "" = config.DefaultBranchPrefix
	// MultiBase makes update branches carry their base: <BranchPrefix><base>/<manager>/<name>
	// (BasePrefix). It is implied by more than one Base; config.MultiBase sets it also for a
	// single base found by a regex.
	MultiBase  bool
	DryRun     bool
	RebaseWhen string // config.RebaseWhens; "" = config.RebaseConflicted
	// RebaseLabel on an open PR requests a rebase like the PR's rebase checkbox and is removed
	// once the branch is pushed; StopUpdatingLabel makes Run leave the PR alone. "" = the
	// config defaults (config.DefaultRebaseLabel, config.DefaultStopUpdatingLabel).
	RebaseLabel, StopUpdatingLabel string
	// UpdateNotScheduled keeps updating and rebasing open PRs outside their schedule; only
	// new PRs wait for a run triggered by one of their crons or inside a time window (Renovate's
	// updateNotScheduled). false skips such groups.
	UpdateNotScheduled bool
	MaxNewPRs          int // PRs created per run; 0 = unlimited
	// ConcurrentLimit caps open managed PRs and HourlyLimit managed PRs created in the last
	// hour (open and closed, by creation time); 0 = unlimited. New PRs are created only
	// while every non-zero limit leaves room.
	ConcurrentLimit, HourlyLimit int
	// IgnoredAuthors are emails (case-insensitive) whose branch tips count as the bot's own.
	IgnoredAuthors []string
	Managers       []manager.Manager // edit the updates' files (engine.Build)
	Now            time.Time         // reference time of prHourlyLimit and NotPendingHours; zero = time.Now()
	// NotPendingHours is prNotPendingHours: a branch of prCreation not-pending whose checks
	// are still running or absent this many hours after its last commit gets its PR anyway.
	NotPendingHours int
	// AllowTasks are the postUpgradeTasks commands that may run (engine.BuildWithTasks); a
	// group with another command fails.
	AllowTasks engine.TaskAllowlist
	// Trigger is what started the run and when; groups are in schedule when it matches all
	// their non-security updates' schedules (schedule.Set.Matches).
	Trigger schedule.Trigger
	Notes   Notes // release ages and notes in PR bodies; nil = none
	// Dashboard maintains the dependency dashboard issue; nil leaves an existing one alone.
	Dashboard *DashboardOptions
	// Progress observes the PR phase (engine.PhasePRs, one item per group, done once the
	// group is handled) and rate-limit waits; nil = off.
	Progress engine.Progress
	// PRLog collects the log records of one PR while it is built and handled: it returns the
	// context for the PR's work and done, which writes the records out with the PR's outcome
	// (its status, "error" or "no changes") and the time spent on it (build and handling, not
	// the wait for other PRs). nil = records go out as they come. PRLog and done may be called
	// from several goroutines.
	PRLog func(ctx context.Context, branch string) (prCtx context.Context, done func(status string, d time.Duration))
}

// Notes renders release information for PR bodies (implemented by releasenotes.Notes). Age
// must be safe for concurrent use: prBody looks up the ages of a PR's updates concurrently.
type Notes interface {
	Section(ctx context.Context, ups []dep.Update) string
	Age(ctx context.Context, u dep.Update) string
}

// Action statuses.
const (
	StatusCreated         = "created"
	StatusUpdated         = "updated"
	StatusRebased         = "rebased"
	StatusUpToDate        = "up-to-date"
	StatusWouldCreate     = "would-create"
	StatusWouldUpdate     = "would-update"
	StatusWouldRebase     = "would-rebase"
	StatusClosed          = "closed"
	StatusDeleted         = "deleted" // an obsolete branch without a PR (branch automerge)
	StatusSkipClosed      = "skipped: closed PR for this version"
	StatusSkipClosedMajor = "skipped: closed PR for this major"
	StatusSkipLimit       = "skipped: new PR limit reached"
	StatusSkipModified    = "skipped: branch modified by someone else"
	StatusSkipSchedule    = "skipped: outside schedule"
	StatusSkipApproval    = "skipped: awaiting dashboard approval" // dashboardApproval: no PR until approved on the dashboard
	StatusSkipIssues      = "skipped: issues disabled"             // dashboard only: the repository has no issue tracker
	// StatusSkipApply: a post-processor found the update inapplicable (*dep.SkipError, e.g.
	// the new version of a Go module declares another module path); Action.Reason says why.
	StatusSkipApply = "skipped: update cannot be applied"
	// StatusSkipStopped: the open PR has the StopUpdatingLabel; Action.Reason is the label.
	StatusSkipStopped = "skipped: stop-updating label"
)

// Automerge outcomes (Action.Automerge).
const (
	AutomergeRequested = "automerge"                    // platform auto-merge requested
	AutomergeEnabled   = "automerge enabled"            // already enabled, not requested again
	AutomergePending   = "automerge pending"            // the forge cannot schedule it yet; next run retries
	AutomergeWaiting   = "automerge waiting for checks" // GitHub fallback: not mergeable or checks not green yet
	AutomergeMerged    = "merged"                       // merged directly: GitHub fallback, or a branch fast-forwarded into the base
	AutomergeWould     = "would-automerge"              // dry run
	// Branch automerge (no PR): the checks of the branch have not finished or none are
	// reported yet; the next run checks again.
	AutomergeBranchWaiting = "branch automerge waiting for checks"
	// Branch automerge (no PR): the checks passed, but the base moved during this run
	// (another branch was merged, or someone pushed); the next run rebases or merges it.
	AutomergeBranchNextRun = "branch automerge next run"
)

// PR creation outcomes (Action.PRWait) of a branch pushed without a PR because its
// prCreation (not-pending, status-success, approval) waits for its checks or the dashboard;
// the next run checks again.
const (
	PRWaitChecks   = "PR waiting for checks"       // none reported yet, or still running
	PRWaitFailed   = "PR waiting for green checks" // status-success: a check failed
	PRWaitApproval = "PR waiting for approval"     // approval: not yet approved on the dashboard
)

// Action is what happened to one branch/PR, or to the dependency dashboard issue
// (Dashboard set, Status created, updated, up-to-date, would-create, would-update or
// StatusSkipIssues).
type Action struct {
	Branch, Title, Status, URL string
	Number                     int
	Automerge                  string   // automerge outcome; "" = not an automerge PR
	PRWait                     string   // why a branch has no PR yet (prCreation); "" = none
	Waits                      []string // StatusSkipSchedule: the schedule entries (crons, windows) that would put the group in schedule
	Dashboard                  bool
	Reason                     string // StatusSkipApply: why; StatusSkipStopped: the label
}

// Display is Status with the automerge and PR creation outcomes appended ("created,
// automerge").
func (a Action) Display() string {
	s := a.Status
	if a.Automerge != "" {
		s += ", " + a.Automerge
	}
	if a.PRWait != "" {
		s += ", " + a.PRWait
	}
	return s
}

var fingerprintRe = regexp.MustCompile(`<!-- deps:fingerprint=([0-9a-f]+) -->`)

// rebaseCheckbox ends every PR body above the markers; ticking it makes the next run rebase
// the PR like the dashboard's rebase checkbox. The content fingerprint does not cover the
// body, so ticking it changes nothing else.
const rebaseCheckbox = "- [ ] <!-- deps:rebase --> If you want to rebase/retry this PR, check this box"

// rebaseTickedRe finds a ticked rebaseCheckbox: "- [x] <!-- deps:rebase -->".
var rebaseTickedRe = regexp.MustCompile(`(?m)^(\s*[-*] )\[[xX]\]( <!-- deps:rebase -->)`)

// rebaseTicked reports whether body has its rebase checkbox ticked.
func rebaseTicked(body string) bool { return rebaseTickedRe.MatchString(body) }

// untickRebase returns body with its rebase checkbox unticked.
func untickRebase(body string) string { return rebaseTickedRe.ReplaceAllString(body, "${1}[ ]${2}") }

// Base is one base branch of a run: the checkout of its tip and the updates resolved there.
type Base struct {
	Name string         // base branch; "" = the repository's default branch
	Root string         // directory the updates were resolved in, inside Git's work tree
	Res  *engine.Result // the updates, dependencies and resolution errors of Root
	Git  Git            // the checkout of Name's tip
}

// BasePrefix is the prefix of the update branches of base when they carry it
// (Options.MultiBase).
func BasePrefix(prefix, base string) string { return prefix + base + "/" }

// Run creates, updates and closes dependency PRs for the updates of each base branch; post
// regenerates lockfiles per manager (engine.Build). The PR limits and the dependency dashboard
// span all bases; PR state and cleanup are per base: a base's PRs are those targeting it (with
// MultiBase also under its BasePrefix when the forge does not report the target). Obsolete
// PRs of a base are only closed when its resolution reported no error, and obsolete branches
// without a PR only when no base's did, so a transient failure never closes PRs. A group
// whose every build error is a *dep.SkipError is skipped instead (StatusSkipApply, its deps
// added to Res.Skipped and marked in Res.Deps): retrying cannot help, so it holds back neither
// the cleanup nor other groups; an open PR of it stays open.
func Run(ctx context.Context, o Options, bases []Base, post map[string]engine.PostProcessor, pc platform.Client) ([]Action, []error) {
	ctx, prog := engine.WithProgress(ctx, o.Progress)
	bases = slices.Clone(bases)
	heads := make([]string, len(bases))
	for i := range bases {
		b := &bases[i]
		if b.Name == "" {
			name, err := pc.DefaultBranch(ctx)
			if err != nil {
				return nil, []error{fmt.Errorf("default branch: %w", err)}
			}
			b.Name = name
		}
		head, err := checkoutHead(ctx, b.Git, b.Name, o.DryRun)
		if err != nil {
			return nil, []error{err}
		}
		heads[i] = head
	}

	o.RebaseLabel = cmp.Or(o.RebaseLabel, config.DefaultRebaseLabel)
	o.StopUpdatingLabel = cmp.Or(o.StopUpdatingLabel, config.DefaultStopUpdatingLabel)
	prefix := cmp.Or(o.BranchPrefix, config.DefaultBranchPrefix)
	// The four reads below are independent round trips; run them concurrently
	// and report their errors in the order they used to be issued.
	var (
		branches                map[string]string
		open, closed            []platform.PR
		loaded                  *dashboard
		lsErr, oErr, cErr, dErr error
		reads                   sync.WaitGroup
	)
	g := bases[0].Git // every checkout shares origin
	reads.Go(func() { branches, lsErr = g.LsRemote(ctx, "refs/heads/"+prefix+"*") })
	reads.Go(func() { open, oErr = listDeps(ctx, pc, "open", 20, prefix) })
	reads.Go(func() { closed, cErr = listDeps(ctx, pc, "closed", 4, prefix) })
	if o.Dashboard != nil {
		reads.Go(func() { loaded, dErr = loadDashboard(ctx, pc, o.Dashboard.Title) })
	}
	reads.Wait()
	if err := cmp.Or(lsErr, oErr, cErr); err != nil {
		return nil, []error{err}
	}

	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	var errs []error
	var dash *dashboard
	dashDisabled := false
	if o.Dashboard != nil {
		switch {
		case errors.Is(dErr, platform.ErrIssuesDisabled):
			slog.WarnContext(ctx, "dependency dashboard skipped", "err", dErr)
			dashDisabled = true
		case dErr != nil:
			errs = append(errs, fmt.Errorf("dependency dashboard: %w", dErr))
		default:
			dash = loaded
		}
	}
	multi := o.MultiBase || len(bases) > 1
	budget := &prBudget{limit: newPRLimit(o, open, closed, now)}
	slog.DebugContext(ctx, "new PR limit", "limit", budget.limit, "maxNewPRs", o.MaxNewPRs, "prConcurrentLimit", o.ConcurrentLimit, "prHourlyLimit", o.HourlyLimit)
	runs := make([]*baseRun, len(bases))
	total := 0
	for i, b := range bases {
		bp := prefix
		if multi {
			bp = BasePrefix(prefix, b.Name)
		}
		s := &runState{base: b.Name, head: heads[i], branches: branches, open: ofBase(open, b.Name, bp, multi), closed: ofBase(closed, b.Name, bp, multi), budget: budget, now: now}
		slog.InfoContext(ctx, "bot state", "base", b.Name, "head", short(s.head), "depsBranches", len(branches), "openPRs", len(s.open), "closedPRs", len(s.closed))
		runs[i] = &baseRun{Base: b, s: s, groups: engine.GroupUpdates(declineReplacements(b.Res.Updates, s.open, s.closed, bp, dash), bp)}
		total += len(runs[i].groups)
	}
	prog.Start(engine.PhasePRs, total)
	wanted := map[string]bool{}
	for _, r := range runs {
		errs = append(errs, r.handleGroups(ctx, o, dash, post, pc, prog, wanted)...)
	}
	prog.End(engine.PhasePRs)

	// A branch that failed to build or push is still in wanted, so only resolution errors
	// (a dep that may own an open PR yielded no group) make the wanted set incomplete.
	resolved := true
	for _, r := range runs {
		switch {
		case o.DryRun:
		case len(r.Res.Errors) > 0:
			slog.WarnContext(ctx, "not closing obsolete PRs because of resolution errors", "base", r.Name, "errors", len(r.Res.Errors))
			resolved = false
		default:
			errs = append(errs, r.closeObsolete(ctx, o, wanted, pc)...)
		}
	}
	var actions []Action
	for _, r := range runs {
		actions = append(actions, r.actions...)
	}
	if !o.DryRun && resolved {
		actions, errs = deleteObsoleteBranches(ctx, o, branches, open, closed, wanted, g, actions, errs)
	}
	switch {
	case dashDisabled:
		actions = append(actions, Action{Dashboard: true, Title: o.Dashboard.Title, Status: StatusSkipIssues})
	case dash != nil:
		sections := make([]dashboardSection, len(runs))
		var resErrs []error
		for i, r := range runs {
			sections[i] = dashboardSection{actions: r.actions, deps: r.Res.Deps, warns: r.Res.Warnings}
			if multi {
				sections[i].base = r.Name
			}
			for _, e := range r.Res.Errors {
				if multi {
					e = fmt.Errorf("%s: %w", r.Name, e)
				}
				resErrs = append(resErrs, e)
			}
		}
		a, err := dash.write(ctx, o, pc, renderDashboard(sections, slices.Concat(resErrs, errs)))
		if err != nil {
			errs = append(errs, fmt.Errorf("dependency dashboard: %w", err))
		} else {
			actions = append(actions, a)
		}
	}
	return actions, errs
}

// checkoutHead returns the HEAD of g, which must be the tip of base on origin. A dry run
// accepts an older HEAD with a warning, because it pushes nothing: the base moves while a CI
// job runs (another PR is merged between its checkout and the run), and failing the preview
// for that would only make it flaky. base must exist on origin either way.
func checkoutHead(ctx context.Context, g Git, base string, dryRun bool) (string, error) {
	head, err := g.RevParse(ctx, "HEAD")
	if err != nil {
		return "", err
	}
	remote, err := g.LsRemote(ctx, "refs/heads/"+base)
	if err != nil {
		return "", err
	}
	tip := remote["refs/heads/"+base]
	switch {
	case tip == head:
	case dryRun && tip != "":
		slog.WarnContext(ctx, "dry run: checkout HEAD is not the tip of the base branch, reporting against the checkout", "base", base, "head", short(head), "tip", short(tip))
	default:
		return "", fmt.Errorf("checkout HEAD %s is not the tip of origin/%s (%s); check out the latest %s", short(head), base, short(tip), base)
	}
	return head, nil
}

// ofBase returns the PRs of prs that target base. A PR whose target the forge did not report
// belongs to the only base, or with several to the base whose update branches (prefix) hold
// its head.
func ofBase(prs []platform.PR, base, prefix string, multi bool) []platform.PR {
	var out []platform.PR
	for _, p := range prs {
		if p.Base == base || p.Base == "" && (!multi || strings.HasPrefix(p.Head, prefix)) {
			out = append(out, p)
		}
	}
	return out
}

// baseRun is the work of Run on one base branch: its groups and the actions taken on them.
type baseRun struct {
	Base
	s       *runState
	groups  []engine.Group
	actions []Action
}

// handleGroups builds and handles the groups of r, adding their branches to wanted
// (out-of-schedule branches are still wanted: their PRs stay open).
func (r *baseRun) handleGroups(ctx context.Context, o Options, dash *dashboard, post map[string]engine.PostProcessor, pc platform.Client,
	prog engine.Progress, wanted map[string]bool,
) []error {
	groups, s, root, res := r.groups, r.s, r.Root, r.Res
	built := make([][]engine.FileChange, len(groups))
	buildErrs := make([][]error, len(groups))
	ran := make([]bool, len(groups))
	inSchedule := make([]bool, len(groups))
	forced := make([]force, len(groups))
	for i := range groups {
		forced[i] = dash.force(groups[i].Branch)
		pr := prOf(s.open, groups[i].Branch)
		if pr != nil && pr.HasLabel(o.StopUpdatingLabel) {
			continue // left alone: not built, and handled as StatusSkipStopped
		}
		if pr != nil && (rebaseTicked(pr.Body) || pr.HasLabel(o.RebaseLabel)) {
			slog.DebugContext(ctx, "rebase requested on the PR", "branch", groups[i].Branch, "number", pr.Number, "label", pr.HasLabel(o.RebaseLabel))
			forced[i].rebase = true
		}
		_, branchExists := s.branches["refs/heads/"+groups[i].Branch]
		if awaitsApproval(groups[i], pr, branchExists, forced[i]) {
			continue // not built: handled as StatusSkipApproval
		}
		inSchedule[i] = forced[i].any() || scheduled(groups[i].Updates, o.Trigger)
		existing := pr != nil || withoutPR(groups[i]) && branchExists
		// Nothing is pushed for the others, so they are not built.
		ran[i] = inSchedule[i] || o.UpdateNotScheduled && existing
	}
	inputs := inputFingerprints(ctx, o, root, s.head, groups, ran, post, r.Git)
	skipped := make([]bool, len(groups))
	fetched := make([]*fetchedPR, len(groups))
	logs := newPRLogs(ctx, o.PRLog, groups)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range groups {
		if !ran[i] {
			continue
		}
		pr := prOf(s.open, groups[i].Branch)
		if pr != nil && reusable(o, *pr, inputs[i], forced[i]) {
			slog.DebugContext(logs.ctx(i), "input fingerprint matches the open PR, not building", "branch", groups[i].Branch, "number", pr.Number, "input", inputs[i])
			skipped[i] = true
			if looksUpMergeable(o, pr, forced[i]) {
				// handle treats the PR as matched: look its mergeability up alongside the builds.
				wg.Go(func() {
					sem <- struct{}{}
					defer func() { <-sem }()
					fetched[i] = fetchPR(logs.ctx(i), pc, pr.Number)
				})
			}
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			changes, errs := build(logs.ctx(i), o, root, s.head, groups[i].Updates, post)
			built[i], buildErrs[i] = changes, errs
			logs.spent[i] += time.Since(start)
			if errs == nil && len(changes) > 0 && looksUpMergeable(o, pr, forced[i]) && bodyFingerprint(pr.Body) == fingerprint(changes) {
				fetched[i] = fetchPR(logs.ctx(i), pc, pr.Number)
			}
		})
	}
	wg.Wait()

	var errs []error
	for i, grp := range groups {
		nErrs, nActions, start := len(errs), len(r.actions), time.Now()
		func() {
			ctx := logs.ctx(i)
			wanted[grp.Branch] = true // out-of-schedule branches are still wanted: keep their PRs open
			if pr := prOf(s.open, grp.Branch); pr != nil && pr.HasLabel(o.StopUpdatingLabel) {
				slog.InfoContext(ctx, "PR has the stop-updating label, leaving it alone", "branch", grp.Branch, "number", pr.Number, "label", o.StopUpdatingLabel)
				r.actions = append(r.actions, Action{Branch: grp.Branch, Title: grp.Title, Status: StatusSkipStopped, URL: pr.URL, Number: pr.Number, Reason: o.StopUpdatingLabel})
				return
			}
			if buildErrs[i] != nil {
				if reason, ok := skipReason(buildErrs[i]); ok {
					slog.InfoContext(ctx, "update cannot be applied", "branch", grp.Branch, "reason", reason)
					skipGroup(res, grp, reason)
					r.actions = append(r.actions, Action{Branch: grp.Branch, Title: grp.Title, Status: StatusSkipApply, Reason: reason})
					return
				}
				errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, errors.Join(buildErrs[i]...)))
				return
			}
			if ran[i] && !skipped[i] && len(built[i]) == 0 {
				// Nothing to change (a lockfile that lock file maintenance finds current): no
				// branch, and an open PR of it is obsolete.
				slog.InfoContext(ctx, "no changes", "branch", grp.Branch)
				delete(wanted, grp.Branch)
				return
			}
			b := groupBuild{changes: built[i], input: inputs[i], pr: fetched[i]}
			if skipped[i] {
				b.build = func() ([]engine.FileChange, error) {
					changes, errs := build(ctx, o, root, s.head, grp.Updates, post)
					return changes, errors.Join(errs...)
				}
			}
			a, target, err := handle(ctx, o, s, grp, b, inSchedule[i], forced[i], pc, r.Git)
			if errors.Is(err, errNoChanges) {
				slog.InfoContext(ctx, "no changes", "branch", grp.Branch)
				delete(wanted, grp.Branch)
				return
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, err))
				return
			}
			if a.Status == StatusCreated && a.Number > 0 {
				if m := (platform.PRMetadata{Labels: grp.Labels, Reviewers: grp.Reviewers, Assignees: grp.Assignees}); !m.Empty() {
					if err := pc.SetPRMetadata(ctx, a.Number, m); err != nil {
						errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, err))
					}
				}
			}
			if pr := prOf(s.open, grp.Branch); pr != nil && (a.Status == StatusRebased || a.Status == StatusUpdated) && pr.HasLabel(o.RebaseLabel) {
				// The rebase the label requested is done.
				if err := pc.RemovePRLabel(ctx, pr.Number, o.RebaseLabel); err != nil {
					errs = append(errs, fmt.Errorf("%s: remove label %q from #%d: %w", grp.Branch, o.RebaseLabel, pr.Number, err))
				}
			}
			if grp.Automerge && target != nil {
				if err := automerge(ctx, o, grp, &a, *target, pc); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, err))
				}
			}
			r.actions = append(r.actions, a)
		}()
		var err error
		status := "no changes"
		switch {
		case len(errs) > nErrs:
			err, status = errors.Join(errs[nErrs:]...), "error"
		case len(r.actions) > nActions:
			status = r.actions[len(r.actions)-1].Display()
		}
		logs.done(i, status, time.Since(start))
		prog.Done(engine.PhasePRs, err)
	}
	return errs
}

// closeObsolete closes the open PRs of r no group wants, deleting their branches, apart from
// those with the stop-updating label.
func (r *baseRun) closeObsolete(ctx context.Context, o Options, wanted map[string]bool, pc platform.Client) []error {
	var errs []error
	for _, pr := range r.s.open {
		if wanted[pr.Head] {
			continue
		}
		if pr.HasLabel(o.StopUpdatingLabel) {
			slog.InfoContext(ctx, "obsolete PR has the stop-updating label, leaving it open", "branch", pr.Head, "number", pr.Number, "label", o.StopUpdatingLabel)
			r.actions = append(r.actions, Action{Branch: pr.Head, Title: pr.Title, Status: StatusSkipStopped, URL: pr.URL, Number: pr.Number, Reason: o.StopUpdatingLabel})
			continue
		}
		if err := pc.ClosePR(ctx, pr.Number); err != nil {
			errs = append(errs, fmt.Errorf("close #%d: %w", pr.Number, err))
			continue
		}
		if _, ok := r.s.branches["refs/heads/"+pr.Head]; ok {
			if err := r.Git.DeleteBranch(ctx, pr.Head); err != nil {
				errs = append(errs, fmt.Errorf("delete %s: %w", pr.Head, err))
			}
		}
		r.actions = append(r.actions, Action{Branch: pr.Head, Title: pr.Title, Status: StatusClosed, URL: pr.URL, Number: pr.Number})
	}
	return errs
}

// prLogs are the per-PR log contexts of a run (Options.PRLog), begun on first use.
type prLogs struct {
	parent context.Context
	begin  func(ctx context.Context, branch string) (context.Context, func(string, time.Duration))
	groups []engine.Group
	ctxs   []context.Context
	dones  []func(string, time.Duration)
	spent  []time.Duration // build time per group
}

func newPRLogs(ctx context.Context, begin func(context.Context, string) (context.Context, func(string, time.Duration)), groups []engine.Group) *prLogs {
	return &prLogs{parent: ctx, begin: begin, groups: groups, ctxs: make([]context.Context, len(groups)), dones: make([]func(string, time.Duration), len(groups)), spent: make([]time.Duration, len(groups))}
}

// ctx returns the context of group i's records. Only one goroutine at a time uses a group.
func (l *prLogs) ctx(i int) context.Context {
	if l.begin == nil {
		return l.parent
	}
	if l.ctxs[i] == nil {
		l.ctxs[i], l.dones[i] = l.begin(l.parent, l.groups[i].Branch)
	}
	return l.ctxs[i]
}

// done writes out group i's records with its status; d is the handling time, to which the
// build time is added.
func (l *prLogs) done(i int, status string, d time.Duration) {
	if l.dones[i] != nil {
		l.dones[i](status, l.spent[i]+d)
	}
}

// skipReason returns the reasons of a group's build errors, deduplicated and joined by "; ",
// when every one is a *dep.SkipError.
func skipReason(errs []error) (string, bool) {
	var reasons []string
	for _, e := range errs {
		skip, ok := errors.AsType[*dep.SkipError](e)
		if !ok {
			return "", false
		}
		if !slices.Contains(reasons, skip.Reason) {
			reasons = append(reasons, skip.Reason)
		}
	}
	return strings.Join(reasons, "; "), len(reasons) > 0
}

// skipGroup records the deps of a group that cannot be applied as skipped with reason, as
// Resolve records the deps it skips: in res.Skipped (once per dep) and in res.Deps.
func skipGroup(res *engine.Result, grp engine.Group, reason string) {
	for _, u := range grp.Updates {
		res.SkipDep(u.Dep, reason)
	}
}

// deleteObsoleteBranches deletes the managed branches no group of any base wants that have no
// PR, open or closed, to any base: the branches of branch automerge groups
// whose update is gone, or whose merged branch could not be deleted. Branches someone else
// committed to are kept.
func deleteObsoleteBranches(ctx context.Context, o Options, branches map[string]string, open, closed []platform.PR, wanted map[string]bool, g Git, actions []Action, errs []error) ([]Action, []error) {
	for _, ref := range slices.Sorted(maps.Keys(branches)) {
		b := strings.TrimPrefix(ref, "refs/heads/")
		if wanted[b] || prOf(open, b) != nil || prOf(closed, b) != nil {
			continue
		}
		t, err := g.BranchTip(ctx, b)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b, err))
			continue
		}
		if !ownAuthor(o, t.Author) {
			slog.InfoContext(ctx, "keeping obsolete branch modified by someone else", "branch", b, "author", t.Author)
			continue
		}
		if err := g.DeleteBranch(ctx, b); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", b, err))
			continue
		}
		actions = append(actions, Action{Branch: b, Status: StatusDeleted})
	}
	return actions, errs
}

// force is what the dependency dashboard checkboxes request for one branch; rebase is also
// requested by the PR's rebase checkbox (rebaseCheckbox) and Options.RebaseLabel.
type force struct {
	create   bool // create the PR (or automerge branch) now: bypass schedule, PR limits and dashboardApproval
	recreate bool // create a PR again although one for this version or major was closed
	rebase   bool // rebuild the branch even if unchanged or modified by someone else
	pr       bool // create the PR now instead of merging the branch (branch automerge) or waiting for its checks (prCreation)
}

func (f force) any() bool { return f.create || f.recreate || f.rebase || f.pr }

// awaitsApproval reports whether grp waits for approval on the dependency dashboard: it needs
// it (dashboardApproval), has neither a PR nor a branch yet, and none of its dashboard
// checkboxes is ticked. Once created, a PR or branch is approved for good.
func awaitsApproval(grp engine.Group, pr *platform.PR, branchExists bool, f force) bool {
	return grp.DashboardApproval && pr == nil && !branchExists && !f.any()
}

// automerge requests platform auto-merge for target, the PR of an automerge group after it was
// created, updated, rebased or found up to date, and records the outcome in a.Automerge. When
// the forge will not auto-merge (GitHub: disallowed in the repository, or nothing left to wait
// for) the PR is merged directly, but only once the forge reports it mergeable and all its
// commit statuses and check runs succeeded; otherwise the next run checks again.
func automerge(ctx context.Context, o Options, grp engine.Group, a *Action, target platform.PR, pc platform.Client) error {
	switch {
	case target.AutoMerge:
		a.Automerge = AutomergeEnabled
		return nil
	case o.DryRun:
		a.Automerge = AutomergeWould
		return nil
	}
	err := pc.EnableAutoMerge(ctx, target, grp.AutomergeStrategy)
	switch {
	case err == nil:
		a.Automerge = AutomergeRequested
		return nil
	case errors.Is(err, platform.ErrAutoMergePending):
		slog.DebugContext(ctx, "auto-merge not possible yet", "branch", grp.Branch, "number", target.Number, "err", err)
		a.Automerge = AutomergePending
		return nil
	case !errors.Is(err, platform.ErrAutoMergeUnavailable):
		return err
	}
	slog.DebugContext(ctx, "auto-merge unavailable, merging directly once checks pass", "branch", grp.Branch, "number", target.Number, "err", err)
	full, err := pc.GetPR(ctx, target.Number)
	if err != nil {
		return err
	}
	a.Automerge = AutomergeWaiting
	if full.Mergeable == nil || !*full.Mergeable || full.HeadSHA == "" {
		return nil
	}
	state, err := pc.Checks(ctx, full.HeadSHA)
	if err != nil || state != platform.ChecksSuccess {
		return err
	}
	if err := pc.MergePR(ctx, full, grp.AutomergeStrategy); err != nil {
		return err
	}
	a.Automerge = AutomergeMerged
	return nil
}

// runState is what Run shares between the groups of one base branch it handles.
type runState struct {
	base, head   string            // base branch and its tip, the checkout's HEAD
	branches     map[string]string // managed branches on origin (of every base): ref -> sha
	open, closed []platform.PR     // the PRs of base (ofBase)
	budget       *prBudget         // shared by every base
	merged       bool              // a branch was merged into base, which moved: no further branch merges
	now          time.Time         // reference time of the run (Options.Now)
}

// prBudget counts the new PRs and automerge branches a run creates against its limit.
type prBudget struct {
	limit   int // new PRs and automerge branches this run may create; -1 = unlimited
	created int // new PRs and automerge branches created so far
}

// full reports whether the limit is reached.
func (b *prBudget) full() bool { return b.limit >= 0 && b.created >= b.limit }

// groupBuild is a group's build for handle: the changes, or none yet when build is set (the
// open PR's input fingerprint matched, so Build was skipped). handle then builds only when it
// pushes the branch.
type groupBuild struct {
	changes []engine.FileChange
	input   string // input fingerprint; "" = none
	build   func() ([]engine.FileChange, error)
	pr      *fetchedPR // the open PR as GetPR returned it, fetched ahead; nil = not fetched
}

// fetchedPR is a GetPR result.
type fetchedPR struct {
	pr  platform.PR
	err error
}

func fetchPR(ctx context.Context, pc platform.Client, number int) *fetchedPR {
	pr, err := pc.GetPR(ctx, number)
	return &fetchedPR{pr: pr, err: err}
}

// looksUpMergeable reports whether handle looks up the mergeability of the open PR pr once its
// fingerprint matches: the PR listing left it unknown (GitHub's never reports it) and it
// decides whether the PR is rebuilt.
func looksUpMergeable(o Options, pr *platform.PR, f force) bool {
	return pr != nil && pr.Mergeable == nil && !f.rebase && o.RebaseWhen != config.RebaseNever
}

// errNoChanges is handle's error when a deferred build (groupBuild.build) yields no changes.
var errNoChanges = errors.New("no changes")

// handle creates, updates or skips the PR of one group, or the branch of a group without a PR
// (handleBranch: branch automerge, or a prCreation waiting for checks). target is the PR an
// automerge group requests auto-merge for: the open, created or pushed PR (with the pushed
// head), an empty PR in a dry run, nil when the group was skipped or has no PR.
func handle(ctx context.Context, o Options, s *runState, grp engine.Group, b groupBuild, inSchedule bool, f force,
	pc platform.Client, g Git,
) (a Action, target *platform.PR, err error) {
	a = Action{Branch: grp.Branch, Title: grp.Title}

	pr := prOf(s.open, grp.Branch)
	if pr != nil {
		a.Number, a.URL = pr.Number, pr.URL
	} else if !f.recreate {
		if c, status := closedFor(s.closed, grp); c != nil {
			a.Status, a.Number, a.URL = status, c.Number, c.URL
			return a, nil, nil
		}
	}
	ref := "refs/heads/" + grp.Branch
	expected, exists := s.branches[ref]
	if awaitsApproval(grp, pr, exists, f) {
		a.Status = StatusSkipApproval
		slog.DebugContext(ctx, "awaiting dashboard approval", "branch", grp.Branch)
		return a, nil, nil
	}
	// Branch automerge opens no PR; once one is open (its checks failed, or the dashboard
	// asked for it), that PR is automerged like with automergeType pr. A prCreation waiting
	// for checks opens the PR on a later run, or when the dashboard asks for it; approval
	// only when the dashboard approves it (its create checkbox).
	wantPR := f.pr || f.create && grp.PRCreation == config.PRCreationApproval
	branchMode := pr == nil && withoutPR(grp) && !wantPR
	switch {
	case inSchedule:
	case pr != nil && o.UpdateNotScheduled:
		slog.DebugContext(ctx, "outside schedule, updating the open PR", "branch", grp.Branch, "number", pr.Number)
	case branchMode && exists && o.UpdateNotScheduled:
		slog.DebugContext(ctx, "outside schedule, updating the branch without a PR", "branch", grp.Branch)
	default:
		a.Status, a.Waits = StatusSkipSchedule, waitCrons(grp.Updates)
		slog.DebugContext(ctx, "outside schedule", "branch", grp.Branch, "waits", a.Waits)
		return a, nil, nil
	}

	// The branch tip is fetched at most once, and only when its author, parents or tree are
	// needed.
	var t platform.Tip
	fetched := false
	tip := func() error {
		if fetched {
			return nil
		}
		tt, err := g.BranchTip(ctx, grp.Branch)
		if err != nil {
			return err
		}
		t, fetched = tt, true
		slog.DebugContext(ctx, "branch tip", "branch", grp.Branch, "author", t.Author, "want", o.AuthorEmail, "ignored", o.IgnoredAuthors, "parents", t.Parents)
		return nil
	}

	changes := b.changes
	fp := fingerprint(changes)
	rebase := false
	matched := false
	if pr != nil {
		prFP := bodyFingerprint(pr.Body)
		if b.build != nil {
			fp = prFP // same inputs, same content
		}
		matched = prFP == fp
		slog.DebugContext(ctx, "open PR found", "branch", grp.Branch, "number", pr.Number, "fingerprint", fp, "prFingerprint", prFP,
			"input", b.input, "prInput", bodyInput(pr.Body), "built", b.build == nil)
	}
	// An unchanged open PR is up to date; its body records the current input fingerprint.
	upToDate := func() (Action, *platform.PR, error) {
		recordInput(ctx, o, pc, grp, pr, b.input)
		a.Status = StatusUpToDate
		return a, pr, nil
	}
	switch {
	case matched && f.rebase:
		slog.DebugContext(ctx, "rebase requested", "branch", grp.Branch, "number", pr.Number)
		rebase = true
	case matched:
		if o.RebaseWhen == config.RebaseNever {
			return upToDate()
		}
		mergeable := pr.Mergeable
		if mergeable == nil {
			got := b.pr
			if got == nil {
				got = fetchPR(ctx, pc, pr.Number)
			}
			if got.err != nil {
				return a, nil, got.err
			}
			mergeable = got.pr.Mergeable
		}
		if mergeable != nil && !*mergeable {
			slog.DebugContext(ctx, "PR not mergeable, rebuilding", "branch", grp.Branch, "number", pr.Number)
		} else {
			if o.RebaseWhen != config.RebaseBehindBase || !exists {
				return upToDate()
			}
			if err := tip(); err != nil {
				return a, nil, err
			}
			// deps branches are one commit on top of base; any other parent means base moved.
			if ownAuthor(o, t.Author) && slices.Equal(t.Parents, []string{s.head}) {
				return upToDate()
			}
			slog.DebugContext(ctx, "branch behind base, rebasing", "branch", grp.Branch, "number", pr.Number, "head", short(s.head))
			rebase = true
		}
	}
	// Dashboard requests bypass the PR limits; an existing branch without a PR is not new.
	if pr == nil && !(branchMode && exists) && !f.any() && s.budget.full() {
		a.Status = StatusSkipLimit
		return a, nil, nil
	}
	if exists && !f.rebase {
		if err := tip(); err != nil {
			return a, nil, err
		}
		if !ownAuthor(o, t.Author) {
			a.Status = StatusSkipModified
			return a, nil, nil
		}
	}
	if b.build != nil {
		// The input fingerprint matched, but the branch is pushed (the PR conflicts, or base
		// moved under behind-base-branch): build it now.
		if changes, err = b.build(); err != nil {
			return a, nil, err
		}
		if len(changes) == 0 {
			return a, nil, errNoChanges
		}
		if built := fingerprint(changes); built != fp {
			// The same inputs built other content (a registry moved): an update, not a rebase.
			fp, rebase = built, false
		}
	}
	if branchMode {
		return handleBranch(ctx, o, s, grp, changes, fp, b.input, f, expected, exists, t, pc, g)
	}
	if pr == nil && exists && wantPR && !f.rebase {
		// The dashboard asked for the PR of a branch pushed without one: an unchanged branch
		// gets its PR on the pushed commit, keeping the checks that already ran on it.
		fresh, err := branchFresh(ctx, o, s, grp, changes, expected, t, g)
		if err != nil {
			return a, nil, err
		}
		if fresh {
			return raisePR(ctx, o, s, grp, fp, b.input, f, expected, "", pc)
		}
	}
	if o.DryRun {
		switch {
		case rebase:
			a.Status = StatusWouldRebase
		case pr != nil:
			a.Status = StatusWouldUpdate
		default:
			a.Status = StatusWouldCreate
			return a, &platform.PR{}, nil
		}
		return a, pushedPR(*pr, pr.HeadSHA), nil
	}

	commit, err := g.Commit(ctx, s.head, changes, commitMessage(grp), o.AuthorName, o.AuthorEmail)
	if err != nil {
		return a, nil, err
	}
	slog.DebugContext(ctx, "pushing", "branch", grp.Branch, "commit", short(commit), "expected", short(expected))
	if err := g.Push(ctx, commit, grp.Branch, expected); err != nil {
		return a, nil, err
	}
	if pr != nil {
		target = pushedPR(*pr, commit)
	}
	if rebase {
		// Content and fingerprint are unchanged, so the PR title and body stay as they are
		// apart from the input fingerprint.
		recordInput(ctx, o, pc, grp, pr, b.input)
		a.Status = StatusRebased
		return a, target, nil
	}
	// Notes are fetched only for PRs that are actually written; the fingerprint covers file
	// contents only, so changing upstream notes never churns PRs.
	body := prBody(ctx, o.Notes, grp, fp, b.input)
	if pr != nil {
		if err := pc.UpdatePR(ctx, pr.Number, grp.Title, body, pr.Draft); err != nil {
			return a, nil, err
		}
		a.Status = StatusUpdated
		return a, target, nil
	}
	np, err := pc.CreatePR(ctx, grp.Branch, s.base, grp.Title, body, grp.Draft)
	if err != nil {
		return a, nil, err
	}
	s.budget.created++
	a.Status, a.Number, a.URL = StatusCreated, np.Number, np.URL
	np.HeadSHA = cmp.Or(np.HeadSHA, commit)
	return a, &np, nil
}

// pushedPR returns pr after its branch was pushed to commit: on GitLab the push canceled its
// auto-merge, so it is requested again.
func pushedPR(pr platform.PR, commit string) *platform.PR {
	pr.HeadSHA = commit
	pr.AutoMerge = pr.AutoMerge && !pr.PushCancelsAutoMerge
	return &pr
}

// withoutPR reports whether grp's branch is pushed without a PR: branch automerge, or a
// prCreation that waits for the branch's checks or for approval.
func withoutPR(grp engine.Group) bool {
	return grp.AutomergeBranch || grp.PRCreation != "" && grp.PRCreation != config.PRCreationImmediate
}

// prWait is why a branch of grp, which is pushed without a PR and not a branch automerge
// branch, has no PR yet before its checks were looked at.
func prWait(grp engine.Group) string {
	if grp.PRCreation == config.PRCreationApproval {
		return PRWaitApproval
	}
	return PRWaitChecks
}

// handleBranch handles a group without a PR (withoutPR): a branch automerge group
// (Renovate's automergeType branch), or one whose prCreation waits for the branch's checks.
// The branch, one commit on top of base, is pushed and rebuilt like a PR branch. A branch
// automerge branch is also rebuilt whenever base moved, since only a branch directly on base
// can be fast-forwarded; a waiting branch behind base is kept while its changed files are
// what this run built, unless rebaseWhen is behind-base-branch. An unchanged branch goes to
// mergeBranch or awaitChecks. t is the branch tip, fetched when the branch exists and no
// rebuild was requested.
func handleBranch(ctx context.Context, o Options, s *runState, grp engine.Group, changes []engine.FileChange, fp, input string, f force,
	expected string, exists bool, t platform.Tip, pc platform.Client, g Git,
) (a Action, target *platform.PR, err error) {
	a = Action{Branch: grp.Branch, Title: grp.Title}
	if exists && !f.rebase {
		fresh, err := branchFresh(ctx, o, s, grp, changes, expected, t, g)
		if err != nil {
			return a, nil, err
		}
		switch {
		case fresh && grp.AutomergeBranch:
			return mergeBranch(ctx, o, s, grp, fp, input, f, expected, pc, g)
		case fresh:
			return awaitChecks(ctx, o, s, grp, fp, input, f, expected, t, pc)
		}
		slog.DebugContext(ctx, "branch without a PR changed or behind base, rebuilding", "branch", grp.Branch, "parents", t.Parents, "head", short(s.head))
	}
	switch {
	case !exists:
		a.Status = StatusCreated
	case f.rebase:
		a.Status = StatusRebased
	default:
		a.Status = StatusUpdated
	}
	if !grp.AutomergeBranch {
		a.PRWait = prWait(grp)
	}
	if o.DryRun {
		a.Status = map[string]string{StatusCreated: StatusWouldCreate, StatusRebased: StatusWouldRebase, StatusUpdated: StatusWouldUpdate}[a.Status]
		if grp.AutomergeBranch {
			a.Automerge = AutomergeWould
		}
		return a, nil, nil
	}
	commit, err := g.Commit(ctx, s.head, changes, commitMessage(grp), o.AuthorName, o.AuthorEmail)
	if err != nil {
		return a, nil, err
	}
	slog.DebugContext(ctx, "pushing branch without a PR", "branch", grp.Branch, "commit", short(commit), "expected", short(expected))
	if err := g.Push(ctx, commit, grp.Branch, expected); err != nil {
		return a, nil, err
	}
	if !exists {
		s.budget.created++
	}
	if grp.AutomergeBranch {
		a.Automerge = AutomergeBranchWaiting
	}
	return a, nil, nil
}

// branchFresh reports whether the existing branch (tip t, sha expected) holds what this run
// built: one commit on base with the built tree, or, for a waiting branch unless rebaseWhen
// is behind-base-branch, the same contents of the changed files.
func branchFresh(ctx context.Context, o Options, s *runState, grp engine.Group, changes []engine.FileChange, expected string, t platform.Tip, g Git) (bool, error) {
	tree, err := g.WriteTree(ctx, s.head, changes)
	if err != nil {
		return false, err
	}
	if slices.Equal(t.Parents, []string{s.head}) && t.Tree == tree {
		return true, nil
	}
	if grp.AutomergeBranch || o.RebaseWhen == config.RebaseBehindBase {
		return false, nil
	}
	return sameChanges(ctx, g, expected, tree, changes)
}

// sameChanges reports whether the files changes touch are the same in commit sha (the
// branch tip) as in tree (base head with changes applied). A tip whose objects are not local
// (platformCommit) never matches, so the branch is rebuilt.
func sameChanges(ctx context.Context, g Git, sha, tree string, changes []engine.FileChange) (bool, error) {
	paths := make([]string, 1, len(changes)+1)
	paths[0] = "."
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	have, err := g.ObjectIDs(ctx, sha, paths)
	if err != nil {
		return false, err
	}
	want, err := g.ObjectIDs(ctx, tree, paths[1:])
	if err != nil {
		return false, err
	}
	return have[0] != "" && slices.Equal(have[1:], want), nil
}

// awaitChecks acts on an unchanged branch of a group whose prCreation waits for its checks,
// sha its tip t, by their state: passed (or, with not-pending, failed), the PR is opened; still
// running or none reported (or failed, with status-success), it waits for the next run. Under
// not-pending, a tip committed at least NotPendingHours ago gets its PR anyway. Under approval
// it waits for the dashboard, whatever its checks.
func awaitChecks(ctx context.Context, o Options, s *runState, grp engine.Group, fp, input string, f force, sha string, t platform.Tip, pc platform.Client) (Action, *platform.PR, error) {
	a := Action{Branch: grp.Branch, Title: grp.Title, Status: StatusUpToDate}
	if grp.PRCreation == config.PRCreationApproval {
		a.PRWait = PRWaitApproval
		return a, nil, nil
	}
	state, err := pc.Checks(ctx, sha)
	if err != nil {
		return a, nil, err
	}
	slog.DebugContext(ctx, "checks of the branch without a PR", "branch", grp.Branch, "commit", short(sha), "state", state, "prCreation", grp.PRCreation)
	switch {
	case state == platform.ChecksSuccess, state == platform.ChecksFailure && grp.PRCreation == config.PRCreationNotPending:
		return raisePR(ctx, o, s, grp, fp, input, f, sha, "", pc)
	case state == platform.ChecksFailure:
		a.PRWait = PRWaitFailed
	case grp.PRCreation == config.PRCreationNotPending && !t.Date.IsZero() && s.now.Sub(t.Date) >= time.Duration(o.NotPendingHours)*time.Hour:
		slog.DebugContext(ctx, "checks pending past prNotPendingHours, creating the PR", "branch", grp.Branch, "committed", t.Date, "hours", o.NotPendingHours)
		return raisePR(ctx, o, s, grp, fp, input, f, sha, "", pc)
	default:
		a.PRWait = PRWaitChecks
	}
	return a, nil, nil
}

// mergeBranch acts on an unchanged branch automerge branch, one commit (sha) on top of base,
// by the state of its checks: still running or none reported, it waits; passed, base is
// fast-forwarded to it (only one branch per run, since base moves) and the branch deleted;
// failed, or the push to base declined (branch protection), the PR is raised instead.
func mergeBranch(ctx context.Context, o Options, s *runState, grp engine.Group, fp, input string, f force, sha string, pc platform.Client, g Git) (Action, *platform.PR, error) {
	a := Action{Branch: grp.Branch, Title: grp.Title, Status: StatusUpToDate}
	state, err := pc.Checks(ctx, sha)
	if err != nil {
		return a, nil, err
	}
	slog.DebugContext(ctx, "automerge branch checks", "branch", grp.Branch, "commit", short(sha), "state", state)
	switch {
	case state == platform.ChecksFailure:
		return raisePR(ctx, o, s, grp, fp, input, f, sha, "its checks failed", pc)
	case state != platform.ChecksSuccess:
		a.Automerge = AutomergeBranchWaiting
		return a, nil, nil
	case o.DryRun:
		a.Automerge = AutomergeWould
		return a, nil, nil
	case s.merged:
		a.Automerge = AutomergeBranchNextRun
		return a, nil, nil
	}
	err = g.Push(ctx, sha, s.base, s.head)
	switch {
	case errors.Is(err, gitx.ErrStale):
		slog.InfoContext(ctx, "base moved, merging the automerge branch next run", "branch", grp.Branch, "base", s.base, "err", err)
		a.Automerge = AutomergeBranchNextRun
		return a, nil, nil
	case errors.Is(err, gitx.ErrRejected):
		slog.WarnContext(ctx, "base declined the fast-forward, raising a PR instead", "branch", grp.Branch, "base", s.base, "err", err)
		return raisePR(ctx, o, s, grp, fp, input, f, sha, "`"+s.base+"` declined the push", pc)
	case err != nil:
		return a, nil, err
	}
	s.merged = true
	a.Automerge = AutomergeMerged
	slog.InfoContext(ctx, "automerge branch merged", "branch", grp.Branch, "base", s.base, "commit", short(sha))
	if err := g.DeleteBranch(ctx, grp.Branch); err != nil {
		// Not an error of the merge: the next run deletes the branch as obsolete.
		slog.WarnContext(ctx, "deleting the merged branch", "branch", grp.Branch, "err", err)
	}
	return a, nil, nil
}

// raisePR opens the PR of a group whose branch (head sha) was pushed without one, within the
// PR limits: a branch automerge branch that cannot be merged, why says why, or a branch whose
// checks prCreation waited for (why ""). Run then requests platform auto-merge for it.
func raisePR(ctx context.Context, o Options, s *runState, grp engine.Group, fp, input string, f force, sha, why string, pc platform.Client) (Action, *platform.PR, error) {
	a := Action{Branch: grp.Branch, Title: grp.Title}
	switch {
	case !f.any() && s.budget.full():
		a.Status = StatusSkipLimit
		return a, nil, nil
	case o.DryRun:
		a.Status = StatusWouldCreate
		return a, &platform.PR{}, nil
	}
	body := prBody(ctx, o.Notes, grp, fp, input)
	if why != "" {
		slog.InfoContext(ctx, "raising a PR for the automerge branch", "branch", grp.Branch, "reason", why)
		body = "This update is configured to merge its branch without a PR (`automergeType: branch`), but " + why + ", so deps opened this PR.\n\n" + body
	}
	np, err := pc.CreatePR(ctx, grp.Branch, s.base, grp.Title, body, grp.Draft)
	if err != nil {
		return a, nil, err
	}
	s.budget.created++
	a.Status, a.Number, a.URL = StatusCreated, np.Number, np.URL
	np.HeadSHA = cmp.Or(np.HeadSHA, sha)
	return a, &np, nil
}

// newPRLimit returns how many PRs this run may create (-1 = unlimited): the minimum of
// MaxNewPRs, the room below ConcurrentLimit and the room below HourlyLimit.
func newPRLimit(o Options, open, closed []platform.PR, now time.Time) int {
	limit := -1
	tighten := func(n int) {
		n = max(n, 0)
		if limit < 0 || n < limit {
			limit = n
		}
	}
	if o.MaxNewPRs > 0 {
		tighten(o.MaxNewPRs)
	}
	if o.ConcurrentLimit > 0 {
		tighten(o.ConcurrentLimit - len(open))
	}
	if o.HourlyLimit > 0 {
		recent := 0
		for _, pr := range slices.Concat(open, closed) {
			if !pr.CreatedAt.IsZero() && now.Sub(pr.CreatedAt) < time.Hour {
				recent++
			}
		}
		tighten(o.HourlyLimit - recent)
	}
	return limit
}

// ownAuthor reports whether a branch tip by author counts as the bot's own.
func ownAuthor(o Options, author string) bool {
	if strings.EqualFold(author, o.AuthorEmail) {
		return true
	}
	for _, e := range o.IgnoredAuthors {
		if strings.EqualFold(author, e) {
			return true
		}
	}
	return false
}

// scheduled reports whether every non-security update is in schedule for trigger t.
// Security updates are always in schedule.
func scheduled(ups []dep.Update, t schedule.Trigger) bool {
	for _, u := range ups {
		if len(u.Advisories) == 0 && !u.Schedule.Matches(t) {
			return false
		}
	}
	return true
}

// waitCrons returns the schedule entries (crons and windows) that put every non-security
// update of ups in schedule: the intersection of their schedules, in the order of the first one.
func waitCrons(ups []dep.Update) []string {
	var crons []string
	first := true
	for _, u := range ups {
		if len(u.Advisories) > 0 || u.Schedule == nil {
			continue
		}
		if first {
			crons, first = slices.Clone(u.Schedule.Entries()), false
			continue
		}
		crons = slices.DeleteFunc(crons, func(c string) bool { return !slices.Contains(u.Schedule.Entries(), c) })
	}
	return crons
}

// prOf returns the PR of prs with branch as its head, or nil.
func prOf(prs []platform.PR, branch string) *platform.PR {
	for i := range prs {
		if prs[i].Head == branch {
			return &prs[i]
		}
	}
	return nil
}

// listDeps lists the PRs in state whose head branch is under prefix.
func listDeps(ctx context.Context, pc platform.Client, state string, maxPages int, prefix string) ([]platform.PR, error) {
	prs, err := pc.ListPRs(ctx, state, maxPages)
	if err != nil {
		return nil, fmt.Errorf("list %s PRs: %w", state, err)
	}
	out := prs[:0]
	for _, p := range prs {
		if strings.HasPrefix(p.Head, prefix) {
			out = append(out, p)
		}
	}
	return out, nil
}

// build builds a group's updates (engine.Build) and runs their postUpgradeTasks on the
// result; a task failure fails the group.
func build(ctx context.Context, o Options, root, head string, ups []dep.Update, post map[string]engine.PostProcessor) ([]engine.FileChange, []error) {
	changes, errs := engine.Build(ctx, root, o.Managers, ups, post)
	if errs != nil {
		return changes, errs
	}
	changes, err := engine.BuildWithTasks(ctx, root, head, ups, changes, o.AllowTasks)
	if err != nil {
		return nil, []error{err}
	}
	return changes, nil
}

// fingerprint hashes the resulting file contents; changes are sorted by path by engine.Build.
func fingerprint(changes []engine.FileChange) string {
	h := sha256.New()
	for _, c := range changes {
		h.Write([]byte(c.Path))
		h.Write([]byte{0})
		h.Write(c.New)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func bodyFingerprint(body string) string {
	if m := fingerprintRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// prBody renders the body of grp's PR with content fingerprint fp and input fingerprint input
// ("" = none): the group's header, the update table and notes, its footer, then the unticked
// rebaseCheckbox and the markers.
func prBody(ctx context.Context, notes Notes, grp engine.Group, fp, input string) string {
	ups := grp.Updates
	var b strings.Builder
	if grp.Header != "" {
		b.WriteString(grp.Header + "\n\n")
	}
	var ages []string
	if notes == nil {
		b.WriteString("| File | From | To | Type |\n|---|---|---|---|\n")
	} else {
		b.WriteString("| File | From | To | Type | Age |\n|---|---|---|---|---|\n")
		// Each Age may be a registry request; look them up concurrently.
		ages = make([]string, len(ups))
		var wg sync.WaitGroup
		for i, u := range ups {
			wg.Go(func() { ages[i] = notes.Age(ctx, u) })
		}
		wg.Wait()
	}
	for i, u := range ups {
		if u.Maintenance() {
			fmt.Fprintf(&b, "| `%s` | | | lock file maintenance |", u.Dep.Name)
		} else {
			from, to := u.FromTo()
			typ := string(u.Type)
			if u.Lockfile {
				typ += " (lockfile)"
			}
			fmt.Fprintf(&b, "| `%s:%d` | `%s` | `%s` | %s |", u.Dep.File, u.Dep.Line, from, to, typ)
		}
		if notes != nil {
			fmt.Fprintf(&b, " %s |", ages[i])
		}
		b.WriteByte('\n')
	}
	if slices.ContainsFunc(ups, dep.Update.Maintenance) {
		b.WriteString("\nLock file maintenance moves every locked dependency to the newest version its manifest admits; the manifests are unchanged")
		if slices.ContainsFunc(ups, func(u dep.Update) bool { return u.Maintenance() && path.Base(u.Dep.File) == "go.mod" }) {
			b.WriteString(" (in `go.mod`, only the `// indirect` requirements move)")
		}
		b.WriteString(".\n")
	}
	moved := map[string]bool{}
	for _, u := range ups {
		if u.NewName == "" || moved[u.Dep.Name+" "+u.NewName] {
			continue
		}
		moved[u.Dep.Name+" "+u.NewName] = true
		if u.Type == dep.Replacement {
			fmt.Fprintf(&b, "\n`%s` is replaced by `%s` %s: it moved or was renamed upstream. The new package's API may differ, so the build may need fixes.\n", u.Dep.Name, u.NewName, u.NewVersion)
			continue
		}
		fmt.Fprintf(&b, "\n`%s` moves to the module path `%s`: the require and the import paths in the module's `.go` files are rewritten. The new major version's API may have changed, so the build may need fixes.\n", u.Dep.Name, u.NewName)
	}
	var adv []dep.Advisory
	for _, u := range ups {
		adv = append(adv, u.Advisories...)
	}
	if len(adv) > 0 {
		b.WriteString("\n### Security\n\n")
		seen := map[string]bool{}
		for _, a := range adv {
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			id := a.ID
			if a.URL != "" {
				id = "[" + a.ID + "](" + a.URL + ")"
			}
			fmt.Fprintf(&b, "- %s: %s\n", id, a.Summary)
		}
	}
	warned := map[string]bool{}
	for _, u := range ups {
		for _, w := range u.Warnings {
			line := "- `" + u.Dep.Name + "`: " + oneLine(w, dashboardErrorLen)
			if warned[line] {
				continue
			}
			if len(warned) == 0 {
				b.WriteString("\n### Warnings\n\nConsider replacing these dependencies rather than updating them.\n\n")
			}
			warned[line] = true
			b.WriteString(line + "\n")
		}
	}
	if notes != nil {
		if s := notes.Section(ctx, ups); s != "" {
			b.WriteString("\n")
			b.WriteString(s)
		}
	}
	if grp.Footer != "" {
		b.WriteString("\n" + grp.Footer + "\n")
	}
	b.WriteString("\n---\n\n" + rebaseCheckbox + "\n")
	fmt.Fprintf(&b, "\n<!-- deps:fingerprint=%s -->\n", fp)
	if m := versionsMarker(ups); m != "" {
		b.WriteString(m + "\n")
	}
	if input != "" {
		fmt.Fprintf(&b, "<!-- deps:input=%s -->\n", input)
	}
	return b.String()
}

func commitMessage(grp engine.Group) string {
	var b strings.Builder
	b.WriteString(grp.Title)
	b.WriteString("\n\n")
	for _, u := range grp.Updates {
		if u.Maintenance() {
			fmt.Fprintf(&b, "- %s: lock file maintenance\n", u.Dep.Name)
			continue
		}
		from, to := u.FromTo()
		fmt.Fprintf(&b, "- %s:%d %s -> %s\n", u.Dep.File, u.Dep.Line, from, to)
	}
	return b.String()
}

func short(s string) string { return s[:min(7, len(s))] }
