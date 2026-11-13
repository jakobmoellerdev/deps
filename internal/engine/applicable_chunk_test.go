package engine

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
)

// applyFixture writes a workflow of n actions at v1 and returns the updates to v2, in file
// order, with their names.
func applyFixture(t testing.TB, n int) (string, []dep.Update, []string) {
	t.Helper()
	const file = ".github/workflows/a.yml"
	var wf strings.Builder
	wf.WriteString("jobs:\n  a:\n    steps:\n")
	for i := range n {
		fmt.Fprintf(&wf, "      - uses: org/a%d@v1\n", i)
	}
	root, _ := setup(t, "", map[string]string{file: wf.String()})
	ups := make([]dep.Update, n)
	names := make([]string, n)
	for i := range n {
		names[i] = fmt.Sprintf("org/a%d", i)
		ups[i] = dep.Update{
			Dep: dep.Dep{Manager: actions.Name, File: file, Line: i + 4, Name: names[i], Version: "v1",
				RefSpan: refSpan(t, root, file, names[i]+"@v1")},
			Type: dep.Major, NewVersion: "v2",
		}
	}
	return root, ups, names
}

// applyModel is a post-processor of the workflow fixture that cannot apply some of its
// updates: those in alone not at all, those in together only next to another update, as the
// inputs of a compiled requirements file admit a version only in some combinations. It fails
// for the first offender in file order and counts its runs.
type applyModel struct {
	names           []string
	alone, together map[string]bool
	runs            atomic.Int64
}

func (m *applyModel) post() map[string]PostProcessor {
	return map[string]PostProcessor{actions.Name: postFunc{f: func(_ context.Context, _ string, manifests, _ map[string][]byte) (map[string][]byte, error) {
		m.runs.Add(1)
		src := string(manifests["a.yml"])
		var applied []string
		for _, n := range m.names {
			if strings.Contains(src, n+"@v2") {
				applied = append(applied, n)
			}
		}
		for _, n := range applied {
			if m.alone[n] || (m.together[n] && len(applied) > 1) {
				return nil, &dep.SkipError{Reason: n + " cannot be applied"}
			}
		}
		return nil, nil
	}, scope: func(string, string) (string, bool) { return ".github/workflows", true }}}
}

// buildApplicableOneAtATime is BuildApplicable as it added the updates touching the failing
// files before the chunks: one at a time, a build per update. The chunked phase must reach
// the same result.
func buildApplicableOneAtATime(ctx context.Context, root string, managers []manager.Manager, updates []dep.Update, post map[string]PostProcessor) ([]FileChange, []SkippedUpdate, []error) {
	changes, errs := Build(ctx, root, managers, updates, post)
	files := skipFiles(errs)
	if len(files) == 0 {
		return changes, nil, errs
	}
	check := touching(root, managers, updates, files)
	alone := make([]string, len(updates))
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
	together := make([]string, len(updates))
	var accepted []dep.Update
	for i, t := range check {
		if !t {
			continue
		}
		if len(accepted) == 0 && own[i] {
			accepted = append(accepted, updates[i])
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
	if len(more) > 0 {
		changes, errs = Build(ctx, root, managers, updates, post)
	}
	return changes, append(skipped, more...), errs
}

// applyResult is what both algorithms must agree on.
type applyResult struct {
	changes []string
	skipped []string
	errs    []string
}

func runApplicable(t testing.TB, f func(context.Context, string, []manager.Manager, []dep.Update, map[string]PostProcessor) ([]FileChange, []SkippedUpdate, []error),
	root string, ups []dep.Update, m *applyModel,
) (applyResult, int64) {
	t.Helper()
	changes, skipped, errs := f(t.Context(), root, []manager.Manager{actions.New()}, ups, m.post())
	var r applyResult
	for _, c := range changes {
		r.changes = append(r.changes, c.Path+"\n"+string(c.New))
	}
	for _, s := range skipped {
		r.skipped = append(r.skipped, s.Update.Dep.Name+": "+s.Reason)
	}
	for _, e := range errs {
		r.errs = append(r.errs, e.Error())
	}
	return r, m.runs.Load()
}

// Whatever the post-processor cannot apply, the chunked together phase leaves out the same
// updates with the same reasons, in the same order, and returns the same changes and errors
// as the one-at-a-time scan it replaced.
func TestBuildApplicableChunksMatchOneAtATime(t *testing.T) {
	const n = 9
	root, ups, names := applyFixture(t, n)
	rnd := rand.New(rand.NewPCG(7, 11))
	for c := range 40 {
		alone, together := map[string]bool{}, map[string]bool{}
		for _, name := range names {
			switch rnd.IntN(8) {
			case 0:
				alone[name] = true
			case 1, 2:
				together[name] = true
			}
		}
		want, _ := runApplicable(t, buildApplicableOneAtATime, root, ups, &applyModel{names: names, alone: alone, together: together})
		got, _ := runApplicable(t, BuildApplicable, root, ups, &applyModel{names: names, alone: alone, together: together})
		if !slices.Equal(got.skipped, want.skipped) || !slices.Equal(got.errs, want.errs) || !slices.Equal(got.changes, want.changes) {
			t.Fatalf("case %d, alone %v, together %v:\n got %+v\nwant %+v", c, alone, together, got, want)
		}
	}
}

// The chunks reach that result in far fewer post-processor runs when few updates are
// inapplicable, and in no more than a handful extra when many are. The runs counted are all
// of them: the first build, one per update alone (those run in parallel), the together phase
// and the final build.
func TestBuildApplicableChunkRuns(t *testing.T) {
	for _, tt := range []struct {
		name          string
		n             int
		together      []int // the updates that cannot be applied next to another one
		every, offset int   // or every nth of them from offset, 0 = none
		old, want     int64 // post-processor runs, one at a time and in chunks
	}{
		{name: "3 updates, one inapplicable", n: 3, together: []int{1}, old: 7, want: 7},
		{name: "128 updates, none inapplicable", n: 128, old: 1, want: 1},
		{name: "128 updates, the first inapplicable", n: 128, together: []int{0}, old: 257, want: 264},
		{name: "128 updates, one in the middle", n: 128, together: []int{64}, old: 257, want: 144},
		{name: "128 updates, the last inapplicable", n: 128, together: []int{127}, old: 257, want: 143},
		{name: "128 updates, four inapplicable", n: 128, together: []int{3, 40, 64, 100}, old: 257, want: 186},
		{name: "128 updates, every fourth inapplicable", n: 128, every: 4, offset: 1, old: 257, want: 266},
		{name: "128 updates, every other inapplicable", n: 128, every: 2, offset: 1, old: 257, want: 264},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, ups, names := applyFixture(t, tt.n)
			together := map[string]bool{}
			for _, i := range tt.together {
				together[names[i]] = true
			}
			for i := tt.offset; tt.every > 0 && i < tt.n; i += tt.every {
				together[names[i]] = true
			}
			want, oldRuns := runApplicable(t, buildApplicableOneAtATime, root, ups, &applyModel{names: names, together: together})
			got, newRuns := runApplicable(t, BuildApplicable, root, ups, &applyModel{names: names, together: together})
			if !slices.Equal(got.skipped, want.skipped) || !slices.Equal(got.errs, want.errs) || !slices.Equal(got.changes, want.changes) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			if oldRuns != tt.old || newRuns != tt.want {
				t.Errorf("post-processor runs = %d one at a time and %d in chunks, want %d and %d", oldRuns, newRuns, tt.old, tt.want)
			}
		})
	}
}
