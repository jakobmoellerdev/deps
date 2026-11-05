package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

type fakeNotes struct{ sections int }

func (f *fakeNotes) Section(_ context.Context, ups []dep.Update) string {
	f.sections++
	return "### Release notes\n\nnotes for " + ups[0].Dep.Name + "\n"
}

func (*fakeNotes) Age(_ context.Context, u dep.Update) string { return "age of " + u.NewVersion }

func TestPRBodyNotes(t *testing.T) {
	n := &fakeNotes{}
	u := dep.Update{Dep: dep.Dep{File: "go.mod", Line: 3, Name: "m", Version: "v1.0.0"}, Type: dep.Minor, NewVersion: "v1.1.0"}
	body := prBody(t.Context(), n, []dep.Update{u}, "fp", "")
	for _, want := range []string{
		"| File | From | To | Type | Age |\n",
		"| `go.mod:3` | `v1.0.0` | `v1.1.0` | minor | age of v1.1.0 |\n",
		"notes for m\n",
		"<!-- deps:fingerprint=fp -->",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "notes for m") > strings.Index(body, "deps:fingerprint") {
		t.Error("fingerprint marker must stay last")
	}
}

// slowNotes answers Age after a registry-like delay.
type slowNotes struct{ fakeNotes }

func (*slowNotes) Age(_ context.Context, u dep.Update) string {
	time.Sleep(5 * time.Millisecond)
	return "age of " + u.NewVersion
}

func BenchmarkPRBodyAges(b *testing.B) {
	ups := make([]dep.Update, 10)
	for i := range ups {
		ups[i] = dep.Update{Dep: dep.Dep{File: "go.mod", Line: i + 3, Name: fmt.Sprintf("m%d", i), Version: "v1.0.0"}, Type: dep.Minor, NewVersion: "v1.1.0"}
	}
	n := &slowNotes{}
	for b.Loop() {
		prBody(b.Context(), n, ups, "fp", "")
	}
}

// Notes must only be fetched for PRs that are written, never for up-to-date or dry-run groups.
func TestRunFetchesNotesOnlyForWrittenPRs(t *testing.T) {
	root, res := fixture(t)
	fpA := fingerprintOf(t, root, res, "a/x")
	yes := true
	for _, tt := range []struct {
		name string
		o    Options
		pc   fakePC
		want int
	}{
		{"create both", Options{}, fakePC{}, 2},
		{"one up to date", Options{}, fakePC{open: []platform.PR{{Number: 1, Head: branchA, Body: "<!-- deps:fingerprint=" + fpA + " -->", Mergeable: &yes}}}, 1},
		{"dry run", Options{DryRun: true}, fakePC{}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := &fakeNotes{}
			o := tt.o
			o.Notes, o.AuthorEmail, o.MaxNewPRs, o.Managers = n, email, 10, managers
			g := &fakeGit{}
			if _, errs := Run(t.Context(), o, root, res, nil, &tt.pc, g); len(errs) > 0 {
				t.Fatal(errs)
			}
			if n.sections != tt.want {
				t.Errorf("Section called %d times, want %d", n.sections, tt.want)
			}
		})
	}
}
