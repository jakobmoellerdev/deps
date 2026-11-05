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
// files are added one at a time in order, each left out when it makes their build fail with
// skips. Other errors are returned like Build's.
func BuildApplicable(ctx context.Context, root string, managers []manager.Manager, updates []dep.Update, post map[string]PostProcessor) ([]FileChange, []SkippedUpdate, []error) {
	changes, errs := Build(ctx, root, managers, updates, post)
	files := skipFiles(errs)
	if len(files) == 0 {
		return changes, nil, errs
	}
	// Alone: an update whose own build is only skipped cannot be applied.
	check := touching(root, managers, updates, files)
	alone := make([]string, len(updates))
	var wg sync.WaitGroup
	for i, t := range check {
		if t {
			wg.Go(func() {
				_, errs := Build(ctx, root, managers, updates[i:i+1], post)
				if reason, ok := skipOnly(errs); ok {
					alone[i] = reason
				}
			})
		}
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, []error{err}
	}
	updates, skipped := leaveOut(updates, alone)
	if len(skipped) > 0 {
		changes, errs = Build(ctx, root, managers, updates, post)
		if files = skipFiles(errs); len(files) == 0 {
			return changes, skipped, errs
		}
		check = touching(root, managers, updates, files)
	}
	// Together: add the updates touching the failing files one at a time.
	together := make([]string, len(updates))
	var accepted []dep.Update
	for i, t := range check {
		if !t {
			continue
		}
		_, errs := Build(ctx, root, managers, append(slices.Clip(accepted), updates[i]), post)
		if reason, ok := skipOnly(skipErrs(errs)); ok {
			together[i] = reason
		} else {
			accepted = append(accepted, updates[i])
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, []error{err}
		}
	}
	updates, more := leaveOut(updates, together)
	changes, errs = Build(ctx, root, managers, updates, post)
	return changes, append(skipped, more...), errs
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
