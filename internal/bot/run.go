// Package bot turns resolved updates into one branch and one pull request per dependency.
package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// Git is the git plumbing the bot needs; implemented by gitx.Repo.
type Git interface {
	RevParse(ctx context.Context, rev string) (string, error)
	RemoteURL(ctx context.Context) (string, error)
	LsRemote(ctx context.Context, pattern string) (map[string]string, error)
	Commit(ctx context.Context, parent string, changes []engine.FileChange, msg, name, email string) (string, error)
	Push(ctx context.Context, commit, branch, expected string) error
	DeleteBranch(ctx context.Context, branch string) error
	BranchAuthorEmail(ctx context.Context, branch string) (string, error)
}

// Options configure Run.
type Options struct {
	Base, AuthorName, AuthorEmail string
	DryRun                        bool
	MaxNewPRs                     int
	Managers                      []manager.Manager // edit the updates' files (engine.Build)
	Now                           time.Time         // schedule evaluation time; zero = time.Now()
}

// Action statuses.
const (
	StatusCreated      = "created"
	StatusUpdated      = "updated"
	StatusUpToDate     = "up-to-date"
	StatusWouldCreate  = "would-create"
	StatusWouldUpdate  = "would-update"
	StatusClosed       = "closed"
	StatusSkipClosed   = "skipped: closed PR for this version"
	StatusSkipLimit    = "skipped: new PR limit reached"
	StatusSkipModified = "skipped: branch modified by someone else"
	StatusSkipSchedule = "skipped: outside schedule"
)

// Action is what happened to one branch/PR.
type Action struct {
	Branch, Title, Status, URL string
	Number                     int
}

const branchPrefix = "deps/"

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

	branches, err := g.LsRemote(ctx, "refs/heads/"+branchPrefix+"*")
	if err != nil {
		return nil, []error{err}
	}
	open, err := listDeps(ctx, pc, "open", 20)
	if err != nil {
		return nil, []error{err}
	}
	closed, err := listDeps(ctx, pc, "closed", 4)
	if err != nil {
		return nil, []error{err}
	}
	slog.InfoContext(ctx, "bot state", "base", base, "head", short(head), "depsBranches", len(branches), "openPRs", len(open), "closedPRs", len(closed))

	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	groups := engine.GroupUpdates(res.Updates)
	built := make([][]engine.FileChange, len(groups))
	buildErrs := make([]error, len(groups))
	inSchedule := make([]bool, len(groups))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i := range groups {
		inSchedule[i] = scheduled(groups[i].Updates, now)
		if !inSchedule[i] {
			continue // nothing is pushed, so don't build
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			changes, errs := engine.Build(ctx, root, o.Managers, groups[i].Updates, post)
			built[i], buildErrs[i] = changes, errors.Join(errs...)
		})
	}
	wg.Wait()

	var actions []Action
	var errs []error
	created := 0
	wanted := map[string]bool{}
	for i, grp := range groups {
		wanted[grp.Branch] = true // out-of-schedule branches are still wanted: keep their PRs open
		if buildErrs[i] != nil {
			errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, buildErrs[i]))
			continue
		}
		a, err := handle(ctx, o, base, head, grp, built[i], inSchedule[i], branches, open, closed, &created, pc, g)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", grp.Branch, err))
			continue
		}
		actions = append(actions, a)
	}

	if len(errs) > 0 || len(res.Errors) > 0 || o.DryRun {
		if !o.DryRun {
			slog.WarnContext(ctx, "not closing obsolete PRs because of errors", "errors", len(errs)+len(res.Errors))
		}
		return actions, errs
	}
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
	return actions, errs
}

func handle(ctx context.Context, o Options, base, head string, grp engine.Group, changes []engine.FileChange, inSchedule bool,
	branches map[string]string, open, closed []platform.PR, created *int, pc platform.Client, g Git,
) (Action, error) {
	a := Action{Branch: grp.Branch, Title: grp.Title}

	var pr *platform.PR
	for i := range open {
		if open[i].Head == grp.Branch {
			pr = &open[i]
			break
		}
	}
	if pr != nil {
		a.Number, a.URL = pr.Number, pr.URL
	} else {
		for _, c := range closed {
			if c.Head == grp.Branch && !c.Merged && c.Title == grp.Title {
				a.Status, a.Number, a.URL = StatusSkipClosed, c.Number, c.URL
				return a, nil
			}
		}
	}
	if !inSchedule {
		slog.DebugContext(ctx, "outside schedule", "branch", grp.Branch)
		a.Status = StatusSkipSchedule
		return a, nil
	}

	fp := fingerprint(changes)
	body := prBody(grp.Updates, fp)
	if pr != nil {
		slog.DebugContext(ctx, "open PR found", "branch", grp.Branch, "number", pr.Number, "fingerprint", fp, "prFingerprint", bodyFingerprint(pr.Body))
	}
	if pr != nil && bodyFingerprint(pr.Body) == fp {
		mergeable := pr.Mergeable
		if mergeable == nil {
			full, err := pc.GetPR(ctx, pr.Number)
			if err != nil {
				return a, err
			}
			mergeable = full.Mergeable
		}
		if mergeable == nil || *mergeable {
			a.Status = StatusUpToDate
			return a, nil
		}
		slog.DebugContext(ctx, "PR not mergeable, rebuilding", "branch", grp.Branch, "number", pr.Number)
	}
	if pr == nil && *created >= o.MaxNewPRs {
		a.Status = StatusSkipLimit
		return a, nil
	}
	ref := "refs/heads/" + grp.Branch
	expected, exists := branches[ref]
	if exists {
		email, err := g.BranchAuthorEmail(ctx, grp.Branch)
		if err != nil {
			return a, err
		}
		slog.DebugContext(ctx, "branch author", "branch", grp.Branch, "email", email, "want", o.AuthorEmail)
		if email != o.AuthorEmail {
			a.Status = StatusSkipModified
			return a, nil
		}
	}
	if o.DryRun {
		a.Status = StatusWouldCreate
		if pr != nil {
			a.Status = StatusWouldUpdate
		}
		return a, nil
	}

	commit, err := g.Commit(ctx, head, changes, commitMessage(grp), o.AuthorName, o.AuthorEmail)
	if err != nil {
		return a, err
	}
	slog.DebugContext(ctx, "pushing", "branch", grp.Branch, "commit", short(commit), "expected", short(expected))
	if err := g.Push(ctx, commit, grp.Branch, expected); err != nil {
		return a, err
	}
	if pr != nil {
		if err := pc.UpdatePR(ctx, pr.Number, grp.Title, body); err != nil {
			return a, err
		}
		a.Status = StatusUpdated
		return a, nil
	}
	np, err := pc.CreatePR(ctx, grp.Branch, base, grp.Title, body)
	if err != nil {
		return a, err
	}
	*created++
	a.Status, a.Number, a.URL = StatusCreated, np.Number, np.URL
	return a, nil
}

// scheduled reports whether every non-security update is in its schedule at now.
// Security updates are always in schedule.
func scheduled(ups []dep.Update, now time.Time) bool {
	for _, u := range ups {
		if len(u.Advisories) == 0 && !u.Schedule.Contains(now) {
			return false
		}
	}
	return true
}

func listDeps(ctx context.Context, pc platform.Client, state string, maxPages int) ([]platform.PR, error) {
	prs, err := pc.ListPRs(ctx, state, maxPages)
	if err != nil {
		return nil, fmt.Errorf("list %s PRs: %w", state, err)
	}
	out := prs[:0]
	for _, p := range prs {
		if strings.HasPrefix(p.Head, branchPrefix) {
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

func prBody(ups []dep.Update, fp string) string {
	var b strings.Builder
	b.WriteString("| File | From | To | Type |\n|---|---|---|---|\n")
	for _, u := range ups {
		from, to := u.FromTo()
		fmt.Fprintf(&b, "| `%s:%d` | `%s` | `%s` | %s |\n", u.Dep.File, u.Dep.Line, from, to, u.Type)
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
	fmt.Fprintf(&b, "\n<!-- deps:fingerprint=%s -->\n", fp)
	return b.String()
}

func commitMessage(grp engine.Group) string {
	var b strings.Builder
	b.WriteString(grp.Title)
	b.WriteString("\n\n")
	for _, u := range grp.Updates {
		from, to := u.FromTo()
		fmt.Fprintf(&b, "- %s:%d %s -> %s\n", u.Dep.File, u.Dep.Line, from, to)
	}
	return b.String()
}

func short(s string) string { return s[:min(7, len(s))] }
