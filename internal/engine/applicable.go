package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// BuildError is a Build failure of the files it names: one file whose patch failed, or every
// manifest of a failed post-processing scope.
type BuildError struct {
	Files []string // slash paths relative to the root
	Err   error
}

func (e *BuildError) Error() string { return strings.Join(e.Files, ", ") + ": " + e.Err.Error() }

func (e *BuildError) Unwrap() error { return e.Err }

// SkippedUpdate is an update BuildApplicable left out because it cannot be applied.
type SkippedUpdate struct {
	Update dep.Update
	Reason string // the *dep.SkipError reasons, joined by "; "
}

// BuildApplicable is Build for a set of updates that may contain inapplicable ones (`apply`):
// when a post-processor reports a failure of some files as a *dep.SkipError (the inputs of a
// compiled requirements file do not admit a version), only the updates it applies to are
// left out instead of every update of those files. Each update touching the files (its dep's
// file or its manager.FileEditor files) is built alone; those whose own build fails only with
// skips are left out with the reasons, and the others are built again. If that build still
// fails with skips (updates that are inapplicable only together), the updates touching those
// files are added in chunks, in order: a chunk whose build is clean is accepted whole, a
// failing chunk is halved until one update is left, which is left out when its own build
// with the ones accepted before it fails with skips. Other errors are returned like Build's.
func BuildApplicable(ctx context.Context, root string, managers []manager.Manager, updates []dep.Update, post map[string]PostProcessor) ([]FileChange, []SkippedUpdate, []error) {
	changes, errs := Build(ctx, root, managers, updates, post)
	files := skipFiles(errs)
	if len(files) == 0 {
		return changes, nil, errs
	}
	// Alone: an update whose own build is only skipped cannot be applied.
	check := touching(root, managers, updates, files)
	alone := make([]string, len(updates))
	// own[i] is the verdict the together phase below reaches for updates[i] with nothing
	// accepted yet, which is this very build: it adds the first update without building it
	// again. An update the phase never built keeps false and is built there.
	own := make([]bool, len(updates))
	var wg sync.WaitGroup
	for i, t := range check {
		if t {
			wg.Go(func() {
				_, errs := Build(ctx, root, managers, updates[i:i+1], post)
				if reason, ok := skipOnly(errs); ok {
					alone[i] = reason
				}
				_, skipped := skipOnly(skipErrs(errs))
				own[i] = !skipped
			})
		}
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, []error{err}
	}
	updates, skipped := leaveOut(updates, alone)
	own = keptFlags(own, alone)
	if len(skipped) > 0 {
		changes, errs = Build(ctx, root, managers, updates, post)
		if files = skipFiles(errs); len(files) == 0 {
			return changes, skipped, errs
		}
		check = touching(root, managers, updates, files)
	}
	// Together: the updates touching the failing files are added in chunks. A chunk that
	// builds clean accepts all of its updates at once and doubles the size of the next one, a
	// chunk that fails is halved until one update is left, which the build of the updates
	// accepted before it decides on its own. That is the very build the one-at-a-time scan
	// would have run for it, with the same updates accepted before it, so the updates left
	// out and their reasons are the same; a clean chunk only accepts updates that the scan
	// would have accepted too, since its build holds every one of them. With a handful of
	// inapplicable updates among many this takes O(k log n) builds instead of one per update,
	// each of them a package manager run of seconds.
	together := make([]string, len(updates))
	var accepted []dep.Update
	idx := make([]int, 0, len(check))
	for i, t := range check {
		if t {
			idx = append(idx, i)
		}
	}
	size := len(idx)
	if size == len(updates) {
		// The last build was this very set and it failed with skips: halve it right away.
		size /= 2
	}
	// grow is the size of the chunk to try after one of n updates built clean: twice as many,
	// but no more than the run of applicable updates expected between two inapplicable ones,
	// ln2 / p for p the share of them seen so far. A scope whose updates are inapplicable left
	// and right settles at one at a time that way, instead of failing chunk after chunk, and
	// one whose updates are all applicable but a few keeps doubling. decided counts the
	// updates the loop has settled and left how many of them it had to leave out; the eight
	// applicable ones added to decided keep the first one left out from cutting the chunks
	// short on their own.
	var decided, left int
	grow := func(n int) int {
		switch {
		case len(idx) < 3:
			return 1 // too few left for a chunk to save more builds than a failing one costs
		case left == 0:
			return 2 * n
		}
		if run := 7 * (decided + 8) / (10 * (left + 1)); run >= 4 {
			return min(2*n, run)
		}
		return 1
	}
	for size = max(size, 1); len(idx) > 0; {
		n := min(size, len(idx))
		if n == 1 {
			i := idx[0]
			idx, decided = idx[1:], decided+1
			if len(accepted) == 0 && own[i] {
				// Its own build is the one the alone phase above ran, which found no skip.
				accepted, size = append(accepted, updates[i]), grow(1)
				continue
			}
			_, errs := Build(ctx, root, managers, append(slices.Clip(accepted), updates[i]), post)
			if err := ctx.Err(); err != nil {
				return nil, nil, []error{err}
			}
			if reason, ok := skipOnly(skipErrs(errs)); ok {
				together[i], left, size = reason, left+1, 1
			} else {
				accepted, size = append(accepted, updates[i]), grow(1)
			}
			continue
		}
		batch := slices.Clip(accepted)
		for _, i := range idx[:n] {
			batch = append(batch, updates[i])
		}
		_, errs := Build(ctx, root, managers, batch, post)
		if err := ctx.Err(); err != nil {
			return nil, nil, []error{err}
		}
		if _, ok := skipOnly(skipErrs(errs)); ok {
			size = n / 2
			continue
		}
		accepted, idx, decided = batch, idx[n:], decided+n
		size = grow(n)
	}
	updates, more := leaveOut(updates, together)
	if len(more) > 0 {
		// With nothing left out the updates are those of the last full build, whose changes
		// and errors still stand: only a changed set needs another one.
		changes, errs = Build(ctx, root, managers, updates, post)
	}
	return changes, append(skipped, more...), errs
}

// keptFlags returns the elements of flags whose update leaveOut keeps, in order.
func keptFlags(flags []bool, reasons []string) []bool {
	out := flags[:0]
	for i, f := range flags {
		if reasons[i] == "" {
			out = append(out, f)
		}
	}
	return out
}

// touching reports, per update, whether it edits one of files (its dep's file or its
// manager.FileEditor files).
func touching(root string, managers []manager.Manager, updates []dep.Update, files map[string]bool) []bool {
	out := make([]bool, len(updates))
	for i, u := range updates {
		fs, err := groupFiles(root, managers, []dep.Update{u})
		if err != nil {
			fs = []managedFile{{u.Dep.Manager, u.Dep.File}}
		}
		out[i] = slices.ContainsFunc(fs, func(f managedFile) bool { return files[f.path] })
	}
	return out
}

// skipErrs returns the errors of errs that are skips.
func skipErrs(errs []error) []error {
	return slices.DeleteFunc(slices.Clone(errs), func(e error) bool {
		_, ok := errors.AsType[*dep.SkipError](e)
		return !ok
	})
}

// leaveOut splits updates into those without a reason and the skipped ones.
func leaveOut(updates []dep.Update, reasons []string) (kept []dep.Update, skipped []SkippedUpdate) {
	for i, u := range updates {
		if reasons[i] != "" {
			skipped = append(skipped, SkippedUpdate{Update: u, Reason: reasons[i]})
		} else {
			kept = append(kept, u)
		}
	}
	return kept, skipped
}

// skipFiles returns the files of the Build errors that are skips.
func skipFiles(errs []error) map[string]bool {
	var files map[string]bool
	for _, e := range errs {
		be, ok := errors.AsType[*BuildError](e)
		if !ok {
			continue
		}
		if _, ok := errors.AsType[*dep.SkipError](be.Err); !ok {
			continue
		}
		if files == nil {
			files = map[string]bool{}
		}
		for _, f := range be.Files {
			files[f] = true
		}
	}
	return files
}

// skipOnly returns the reasons of errs, deduplicated and joined by "; ", when every one is a
// *dep.SkipError.
func skipOnly(errs []error) (string, bool) {
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
