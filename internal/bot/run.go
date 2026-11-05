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
	// WriteTree returns the id of the tree of parent with changes applied, computed locally;
	// it equals the tree of the commit Commit creates from them.
	WriteTree(ctx context.Context, parent string, changes []engine.FileChange) (string, error)
	Commit(ctx context.Context, parent string, changes []engine.FileChange, msg, name, email string) (string, error)
	// Push wraps gitx.ErrStale when the branch moved away from expected and
	// gitx.ErrRejected when origin declined the update.
	Push(ctx context.Context, commit, branch, expected string) error
	DeleteBranch(ctx context.Context, branch string) error
	BranchTip(ctx context.Context, branch string) (gitx.Tip, error)
}

// Options configure Run.
type Options struct {
	Base, AuthorName, AuthorEmail string
	BranchPrefix                  string // managed branch prefix, ending in '/'; "" = config.DefaultBranchPrefix
	DryRun                        bool
	RebaseWhen                    string // config.RebaseWhens; "" = config.RebaseConflicted
	// UpdateNotScheduled keeps updating and rebasing open PRs outside their schedule; only
	// new PRs wait for a run triggered by one of their crons (Renovate's
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
	Now            time.Time         // reference time of prHourlyLimit; zero = time.Now()
	// Trigger is what started the run; groups are in schedule when it matches all their
	// non-security updates' schedules (schedule.Set.Matches).
	Trigger schedule.Trigger
	Notes   Notes // release ages and notes in PR bodies; nil = none
	// Dashboard maintains the dependency dashboard issue; nil leaves an existing one alone.
	Dashboard *DashboardOptions
}

// Notes renders release information for PR bodies (implemented by releasenotes.Notes).
type Notes interface {
	Section(ctx context.Context, ups []dep.Update) string
	Age(ctx context.Context, u dep.Update) string
}

// Action statuses.
const (
	StatusCreated      = "created"
	StatusUpdated      = "updated"
	StatusRebased      = "rebased"
	StatusUpToDate     = "up-to-date"
	StatusWouldCreate  = "would-create"
	StatusWouldUpdate  = "would-update"
	StatusWouldRebase  = "would-rebase"
	StatusClosed       = "closed"
	StatusDeleted      = "deleted" // an obsolete branch without a PR (branch automerge)
	StatusSkipClosed   = "skipped: closed PR for this version"
	StatusSkipLimit    = "skipped: new PR limit reached"
	StatusSkipModified = "skipped: branch modified by someone else"
	StatusSkipSchedule = "skipped: outside schedule"
	StatusSkipIssues   = "skipped: issues disabled" // dashboard only: the repository has no issue tracker
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

// Action is what happened to one branch/PR, or to the dependency dashboard issue
// (Dashboard set, Status created, updated, up-to-date, would-create, would-update or
// StatusSkipIssues).
type Action struct {
	Branch, Title, Status, URL string
	Number                     int
	Automerge                  string   // automerge outcome; "" = not an automerge PR
	Waits                      []string // StatusSkipSchedule: the crons that would put the group in schedule
	Dashboard                  bool
}

// Display is Status with the automerge outcome appended ("created, automerge").
func (a Action) Display() string {
	if a.Automerge == "" {
		return a.Status
	}
	return a.Status + ", " + a.Automerge
}

var fingerprintRe = regexp.MustCompile(`<!-- deps:fingerprint=([0-9a-f]+) -->`)

// Run creates, updates and closes dependency PRs for res; post regenerates lockfiles per
// manager (engine.Build). Obsolete PRs are only closed when neither the engine nor the bot
// reported an error, so a transient failure never closes PRs.
func Run(ctx context.Context, o Options, root string, res *engine.Result, post map[string]engine.PostProcessor, pc platform.Client, g Git) ([]Action, []error) {
	base := o.Base
	if base == "" {
		b, err := pc.DefaultBranch(ctx)
		if err != nil {
			return nil, []error{fmt.Errorf("default branch: %w", err)}
		}
		base = b
	}
	head, err := g.RevParse(ctx, "HEAD")
	if err != nil {
		return nil, []error{err}
	}
	remote, err := g.LsRemote(ctx, "refs/heads/"+base)
	if err != nil {
		return nil, []error{err}
	}
	if tip := remote["refs/heads/"+base]; tip != head {
		return nil, []error{fmt.Errorf("checkout HEAD %s is not the tip of origin/%s (%s); check out the latest %s", short(head), base, short(tip), base)}
	}

	prefix := cmp.Or(o.BranchPrefix, config.DefaultBranchPrefix)
	branches, err := g.LsRemote(ctx, "refs/heads/"+prefix+"*")
	if err != nil {
		return nil, []error{err}
	}
	open, err := listDeps(ctx, pc, "open", 20, prefix)
	if err != nil {
		return nil, []error{err}
	}
	closed, err := listDeps(ctx, pc, "closed", 4, prefix)
	if err != nil {
		return nil, []error{err}
	}
	slog.InfoContext(ctx, "bot state", "base", base, "head", short(head), "depsBranches", len(branches), "openPRs", len(open), "closedPRs", len(closed))

	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	var errs []error
	var dash *dashboard
	dashDisabled := false
	if o.Dashboard != nil {
		d, err := loadDashboard(ctx, pc, o.Dashboard.Title)
		switch {
		case errors.Is(err, platform.ErrIssuesDisabled):
			slog.WarnContext(ctx, "dependency dashboard skipped", "err", err)
			dashDisabled = true
		case err != nil:
			errs = append(errs, fmt.Errorf("dependency dashboard: %w", err))
		default:
			dash = d
		}
	}
	groups := engine.GroupUpdates(res.Updates, prefix)
	built := make([][]engine.FileChange, len(groups))
	buildErrs := make([]error, len(groups))
	ran := make([]bool, len(groups))
	inSchedule := make([]bool, len(groups))
	forced := make([]force, len(groups))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range groups {
		forced[i] = dash.force(groups[i].Branch)
		inSchedule[i] = forced[i].any() || scheduled(groups[i].Updates, o.Trigger)
		_, branchExists := branches["refs/heads/"+groups[i].Branch]
		existing := hasPR(open, groups[i].Branch) || groups[i].AutomergeBranch && branchExists
		if !inSchedule[i] && !(o.UpdateNotScheduled && existing) {
			continue // nothing is pushed, so don't build
		}
		ran[i] = true
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			changes, errs := engine.Build(ctx, root, o.Managers, groups[i].Updates, post)
			built[i], buildErrs[i] = changes, errors.Join(errs...)
		})
	}
	wg.Wait()

	var actions []Action
	s := &runState{base: base, head: head, branches: branches, open: open, closed: closed, limit: newPRLimit(o, open, closed, now)}
	slog.DebugContext(ctx, "new PR limit", "limit", s.limit, "maxNewPRs", o.MaxNewPRs, "prConcurrentLimit", o.ConcurrentLimit, "prHourlyLimit", o.HourlyLimit)
	wanted := map[string]bool{}
	for i, grp := range groups {
		wanted[grp.Branch] = true // out-of-schedule branches are still wanted: keep their PRs open
		if buildErrs[i] != nil {
			errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, buildErrs[i]))
			continue
		}
		if ran[i] && len(built[i]) == 0 {
			// Nothing to change (a lockfile that lock file maintenance finds current): no
			// branch, and an open PR of it is obsolete.
			slog.InfoContext(ctx, "no changes", "branch", grp.Branch)
			delete(wanted, grp.Branch)
			continue
		}
		a, target, err := handle(ctx, o, s, grp, built[i], inSchedule[i], forced[i], pc, g)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, err))
			continue
		}
		if grp.Automerge && target != nil {
			if err := automerge(ctx, o, grp, &a, *target, pc); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, err))
			}
		}
		actions = append(actions, a)
	}

	switch {
	case o.DryRun:
	case len(errs) > 0 || len(res.Errors) > 0:
		slog.WarnContext(ctx, "not closing obsolete PRs because of errors", "errors", len(errs)+len(res.Errors))
	default:
		for _, pr := range open {
			if wanted[pr.Head] {
				continue
			}
			if err := pc.ClosePR(ctx, pr.Number); err != nil {
				errs = append(errs, fmt.Errorf("close #%d: %w", pr.Number, err))
				continue
			}
			if _, ok := branches["refs/heads/"+pr.Head]; ok {
				if err := g.DeleteBranch(ctx, pr.Head); err != nil {
					errs = append(errs, fmt.Errorf("delete %s: %w", pr.Head, err))
				}
			}
			actions = append(actions, Action{Branch: pr.Head, Title: pr.Title, Status: StatusClosed, URL: pr.URL, Number: pr.Number})
		}
		actions, errs = deleteObsoleteBranches(ctx, o, s, wanted, g, actions, errs)
	}
	switch {
	case dashDisabled:
		actions = append(actions, Action{Dashboard: true, Title: o.Dashboard.Title, Status: StatusSkipIssues})
	case dash != nil:
		a, err := dash.write(ctx, o, pc, renderDashboard(actions, slices.Concat(res.Errors, errs), res.Deps))
		if err != nil {
			errs = append(errs, fmt.Errorf("dependency dashboard: %w", err))
		} else {
			actions = append(actions, a)
		}
	}
	return actions, errs
}

// deleteObsoleteBranches deletes the managed branches no group wants that have no PR, open
// or closed: the branches of branch automerge groups whose update is gone, or whose merged
// branch could not be deleted. Branches someone else committed to are kept.
func deleteObsoleteBranches(ctx context.Context, o Options, s *runState, wanted map[string]bool, g Git, actions []Action, errs []error) ([]Action, []error) {
	for _, ref := range slices.Sorted(maps.Keys(s.branches)) {
		b := strings.TrimPrefix(ref, "refs/heads/")
		if wanted[b] || hasPR(s.open, b) || hasPR(s.closed, b) {
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

// force is what the dependency dashboard checkboxes request for one branch.
type force struct {
	create   bool // create the PR (or automerge branch) now: bypass schedule and PR limits
	recreate bool // create a PR again although one for this version was closed
	rebase   bool // rebuild the branch even if unchanged or modified by someone else
	pr       bool // create a PR instead of merging the branch (branch automerge)
}

func (f force) any() bool { return f.create || f.recreate || f.rebase || f.pr }

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

// runState is what Run shares between the groups it handles.
type runState struct {
	base, head   string            // base branch and its tip, the checkout's HEAD
	branches     map[string]string // managed branches on origin: ref -> sha
	open, closed []platform.PR
	limit        int  // new PRs and automerge branches this run may create; -1 = unlimited
	created      int  // new PRs and automerge branches created so far
	merged       bool // a branch was merged into base, which moved: no further branch merges
}

// handle creates, updates or skips the PR of one group, or the branch of a branch automerge
// group without a PR (handleBranch). target is the PR an automerge group requests auto-merge
// for: the open, created or pushed PR (with the pushed head), an empty PR in a dry run, nil
// when the group was skipped or has no PR.
func handle(ctx context.Context, o Options, s *runState, grp engine.Group, changes []engine.FileChange, inSchedule bool, f force,
	pc platform.Client, g Git,
) (a Action, target *platform.PR, err error) {
	a = Action{Branch: grp.Branch, Title: grp.Title}

	var pr *platform.PR
	for i := range s.open {
		if s.open[i].Head == grp.Branch {
			pr = &s.open[i]
			break
		}
	}
	if pr != nil {
		a.Number, a.URL = pr.Number, pr.URL
	} else if !f.recreate {
		for _, c := range s.closed {
			if c.Head == grp.Branch && !c.Merged && c.Title == grp.Title {
				a.Status, a.Number, a.URL = StatusSkipClosed, c.Number, c.URL
				return a, nil, nil
			}
		}
	}
	ref := "refs/heads/" + grp.Branch
	expected, exists := s.branches[ref]
	// Branch automerge opens no PR; once one is open (its checks failed, or the dashboard
	// asked for it), that PR is automerged like with automergeType pr.
	branchMode := pr == nil && grp.AutomergeBranch && !f.pr
	switch {
	case inSchedule:
	case pr != nil && o.UpdateNotScheduled:
		slog.DebugContext(ctx, "outside schedule, updating the open PR", "branch", grp.Branch, "number", pr.Number)
	case branchMode && exists && o.UpdateNotScheduled:
		slog.DebugContext(ctx, "outside schedule, updating the automerge branch", "branch", grp.Branch)
	default:
		a.Status, a.Waits = StatusSkipSchedule, waitCrons(grp.Updates)
		slog.DebugContext(ctx, "outside schedule", "branch", grp.Branch, "waits", a.Waits)
		return a, nil, nil
	}

	// The branch tip is fetched at most once, and only when its author, parents or tree are
	// needed.
	var t gitx.Tip
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

	fp := fingerprint(changes)
	rebase := false
	matched := false
	if pr != nil {
		prFP := bodyFingerprint(pr.Body)
		matched = prFP == fp
		slog.DebugContext(ctx, "open PR found", "branch", grp.Branch, "number", pr.Number, "fingerprint", fp, "prFingerprint", prFP)
	}
	switch {
	case matched && f.rebase:
		slog.DebugContext(ctx, "rebase requested on the dependency dashboard", "branch", grp.Branch, "number", pr.Number)
		rebase = true
	case matched:
		if o.RebaseWhen == config.RebaseNever {
			a.Status = StatusUpToDate
			return a, pr, nil
		}
		mergeable := pr.Mergeable
		if mergeable == nil {
			full, err := pc.GetPR(ctx, pr.Number)
			if err != nil {
				return a, nil, err
			}
			mergeable = full.Mergeable
		}
		if mergeable != nil && !*mergeable {
			slog.DebugContext(ctx, "PR not mergeable, rebuilding", "branch", grp.Branch, "number", pr.Number)
		} else {
			if o.RebaseWhen != config.RebaseBehindBase || !exists {
				a.Status = StatusUpToDate
				return a, pr, nil
			}
			if err := tip(); err != nil {
				return a, nil, err
			}
			// deps branches are one commit on top of base; any other parent means base moved.
			if ownAuthor(o, t.Author) && slices.Equal(t.Parents, []string{s.head}) {
				a.Status = StatusUpToDate
				return a, pr, nil
			}
			slog.DebugContext(ctx, "branch behind base, rebasing", "branch", grp.Branch, "number", pr.Number, "head", short(s.head))
			rebase = true
		}
	}
	// Dashboard requests bypass the PR limits; an existing automerge branch is not new.
	if pr == nil && !(branchMode && exists) && !f.any() && s.limit >= 0 && s.created >= s.limit {
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
	if branchMode {
		return handleBranch(ctx, o, s, grp, changes, fp, f, expected, exists, t, pc, g)
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
		return a, pr, nil
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
		pushed := *pr
		pushed.HeadSHA = commit
		target = &pushed
	}
	if rebase {
		// Content and fingerprint are unchanged, so the PR title and body stay as they are.
		a.Status = StatusRebased
		return a, target, nil
	}
	// Notes are fetched only for PRs that are actually written; the fingerprint covers file
	// contents only, so changing upstream notes never churns PRs.
	body := prBody(ctx, o.Notes, grp.Updates, fp)
	if pr != nil {
		if err := pc.UpdatePR(ctx, pr.Number, grp.Title, body); err != nil {
			return a, nil, err
		}
		a.Status = StatusUpdated
		return a, target, nil
	}
	np, err := pc.CreatePR(ctx, grp.Branch, s.base, grp.Title, body)
	if err != nil {
		return a, nil, err
	}
	s.created++
	a.Status, a.Number, a.URL = StatusCreated, np.Number, np.URL
	np.HeadSHA = cmp.Or(np.HeadSHA, commit)
	return a, &np, nil
}

// handleBranch handles a branch automerge group without a PR (Renovate's automergeType
// branch). The branch, one commit on top of base, is pushed and rebuilt like a PR branch, and
// always when base moved: only a branch directly on base can be fast-forwarded. An unchanged
// branch is merged by mergeBranch once its checks pass. t is the branch tip, fetched when the
// branch exists and no rebuild was requested.
func handleBranch(ctx context.Context, o Options, s *runState, grp engine.Group, changes []engine.FileChange, fp string, f force,
	expected string, exists bool, t gitx.Tip, pc platform.Client, g Git,
) (a Action, target *platform.PR, err error) {
	a = Action{Branch: grp.Branch, Title: grp.Title}
	if exists && !f.rebase {
		tree, err := g.WriteTree(ctx, s.head, changes)
		if err != nil {
			return a, nil, err
		}
		if slices.Equal(t.Parents, []string{s.head}) && t.Tree == tree {
			return mergeBranch(ctx, o, s, grp, fp, f, expected, pc, g)
		}
		slog.DebugContext(ctx, "automerge branch changed or behind base, rebuilding", "branch", grp.Branch, "parents", t.Parents, "head", short(s.head))
	}
	switch {
	case !exists:
		a.Status = StatusCreated
	case f.rebase:
		a.Status = StatusRebased
	default:
		a.Status = StatusUpdated
	}
	if o.DryRun {
		a.Status = map[string]string{StatusCreated: StatusWouldCreate, StatusRebased: StatusWouldRebase, StatusUpdated: StatusWouldUpdate}[a.Status]
		a.Automerge = AutomergeWould
		return a, nil, nil
	}
	commit, err := g.Commit(ctx, s.head, changes, commitMessage(grp), o.AuthorName, o.AuthorEmail)
	if err != nil {
		return a, nil, err
	}
	slog.DebugContext(ctx, "pushing automerge branch", "branch", grp.Branch, "commit", short(commit), "expected", short(expected))
	if err := g.Push(ctx, commit, grp.Branch, expected); err != nil {
		return a, nil, err
	}
	if !exists {
		s.created++
	}
	a.Automerge = AutomergeBranchWaiting
	return a, nil, nil
}

// mergeBranch acts on an unchanged branch automerge branch, one commit (sha) on top of base,
// by the state of its checks: still running or none reported, it waits; passed, base is
// fast-forwarded to it (only one branch per run, since base moves) and the branch deleted;
// failed, or the push to base declined (branch protection), the PR is raised instead.
func mergeBranch(ctx context.Context, o Options, s *runState, grp engine.Group, fp string, f force, sha string, pc platform.Client, g Git) (Action, *platform.PR, error) {
	a := Action{Branch: grp.Branch, Title: grp.Title, Status: StatusUpToDate}
	state, err := pc.Checks(ctx, sha)
	if err != nil {
		return a, nil, err
	}
	slog.DebugContext(ctx, "automerge branch checks", "branch", grp.Branch, "commit", short(sha), "state", state)
	switch {
	case state == platform.ChecksFailure:
		return raisePR(ctx, o, s, grp, fp, f, sha, "its checks failed", pc)
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
		return raisePR(ctx, o, s, grp, fp, f, sha, "`"+s.base+"` declined the push", pc)
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

// raisePR opens the PR of a branch automerge group whose branch (head sha) cannot be merged,
// why says why, within the PR limits. Run then requests platform auto-merge for it.
func raisePR(ctx context.Context, o Options, s *runState, grp engine.Group, fp string, f force, sha, why string, pc platform.Client) (Action, *platform.PR, error) {
	a := Action{Branch: grp.Branch, Title: grp.Title}
	switch {
	case !f.any() && s.limit >= 0 && s.created >= s.limit:
		a.Status = StatusSkipLimit
		return a, nil, nil
	case o.DryRun:
		a.Status = StatusWouldCreate
		return a, &platform.PR{}, nil
	}
	slog.InfoContext(ctx, "raising a PR for the automerge branch", "branch", grp.Branch, "reason", why)
	body := "This update is configured to merge its branch without a PR (`automergeType: branch`), but " + why + ", so deps opened this PR.\n\n" +
		prBody(ctx, o.Notes, grp.Updates, fp)
	np, err := pc.CreatePR(ctx, grp.Branch, s.base, grp.Title, body)
	if err != nil {
		return a, nil, err
	}
	s.created++
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

// waitCrons returns the crons that put every non-security update of ups in schedule:
// the intersection of their schedules, in the order of the first one.
func waitCrons(ups []dep.Update) []string {
	var crons []string
	first := true
	for _, u := range ups {
		if len(u.Advisories) > 0 || u.Schedule == nil {
			continue
		}
		if first {
			crons, first = slices.Clone(u.Schedule.Crons()), false
			continue
		}
		crons = slices.DeleteFunc(crons, func(c string) bool { return !slices.Contains(u.Schedule.Crons(), c) })
	}
	return crons
}

// hasPR reports whether an open PR has branch as its head.
func hasPR(open []platform.PR, branch string) bool {
	return slices.ContainsFunc(open, func(p platform.PR) bool { return p.Head == branch })
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

func prBody(ctx context.Context, notes Notes, ups []dep.Update, fp string) string {
	var b strings.Builder
	if notes == nil {
		b.WriteString("| File | From | To | Type |\n|---|---|---|---|\n")
	} else {
		b.WriteString("| File | From | To | Type | Age |\n|---|---|---|---|---|\n")
	}
	for _, u := range ups {
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
			fmt.Fprintf(&b, " %s |", notes.Age(ctx, u))
		}
		b.WriteByte('\n')
	}
	if slices.ContainsFunc(ups, dep.Update.Maintenance) {
		b.WriteString("\nLock file maintenance moves every locked dependency to the newest version its manifest admits; the manifests are unchanged.\n")
	}
	moved := map[string]bool{}
	for _, u := range ups {
		if u.NewName != "" && !moved[u.Dep.Name+" "+u.NewName] {
			moved[u.Dep.Name+" "+u.NewName] = true
			fmt.Fprintf(&b, "\n`%s` moves to the module path `%s`: the require and the import paths in the module's `.go` files are rewritten. The new major version's API may have changed, so the build may need fixes.\n", u.Dep.Name, u.NewName)
		}
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
	if notes != nil {
		if s := notes.Section(ctx, ups); s != "" {
			b.WriteString("\n")
			b.WriteString(s)
		}
	}
	fmt.Fprintf(&b, "\n<!-- deps:fingerprint=%s -->\n", fp)
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
