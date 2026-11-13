package bot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
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
	body := prBody(t.Context(), Options{Notes: n}, engine.Group{Updates: []dep.Update{u}}, "fp", "")
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

// bigNotes answers a release notes section of one <details> block per update.
type bigNotes struct{ fakeNotes }

func (*bigNotes) Section(_ context.Context, ups []dep.Update) string {
	var b strings.Builder
	b.WriteString("### Release notes\n")
	for _, u := range ups {
		fmt.Fprintf(&b, "\n<details>\n<summary>Release notes: %s</summary>\n\n%s\n\n</details>\n", u.Dep.Name, strings.Repeat("note ", 100))
	}
	return b.String()
}

// A group of hundreds of updates with advisories, warnings and release notes renders a body
// within prBodyMaxBytes: GitHub refuses a longer one with 422 "body is too long", so deps
// would push the branch and retry the PR on every run. The markers it reads back and the
// rebase checkbox survive the cut, every <details> block opened in the notes is closed, and
// the cut lists say so.
func TestPRBodyBudget(t *testing.T) {
	ups := make([]dep.Update, 600)
	for i := range ups {
		ups[i] = dep.Update{
			Dep: dep.Dep{
				Manager: "gomod", File: fmt.Sprintf("services/service%03d/go.mod", i), Line: i + 3,
				Name: fmt.Sprintf("github.com/example/long-module-name-%03d", i), Version: "v1.0.0",
			},
			Type: dep.Minor, NewVersion: "v1.1.0",
			Advisories: []dep.Advisory{{ID: fmt.Sprintf("GO-2026-%04d", i), Summary: strings.Repeat("summary ", 8)}},
			Warnings:   []string{fmt.Sprintf("long-module-name-%03d is deprecated by its author", i)},
		}
	}
	grp := engine.Group{Updates: ups, Header: "Header.", Footer: "Footer."}
	body := prBody(t.Context(), Options{Notes: &bigNotes{}}, grp, "abc", "in")
	// GitHub refuses a longer body with 422 "body is too long".
	const githubMaxBody = 65536
	if len(body) > githubMaxBody {
		t.Errorf("body is %d bytes, want at most %d", len(body), githubMaxBody)
	}
	for _, want := range []string{
		"| `services/service000/go.mod:3` | `v1.0.0` | `v1.1.0` | minor |",
		"more not listed (pull request size limit)",
		rebaseCheckbox, "<!-- deps:fingerprint=abc -->", "<!-- deps:input=in -->",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q (%d bytes)", want, len(body))
		}
	}
	if open, closed := strings.Count(body, "<details>"), strings.Count(body, "</details>"); open != closed {
		t.Errorf("%d <details> blocks opened, %d closed", open, closed)
	}
	// Closing a PR ignores the versions its marker records, so they are all kept.
	if got := bodyVersions(body); len(got) != len(ups) {
		t.Errorf("versions marker records %d updates, want %d", len(got), len(ups))
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
		prBody(b.Context(), Options{Notes: n}, engine.Group{Updates: ups}, "fp", "")
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
			if _, errs := Run(t.Context(), o, []Base{{Root: root, Res: res, Git: g}}, nil, &tt.pc); len(errs) > 0 {
				t.Fatal(errs)
			}
			if n.sections != tt.want {
				t.Errorf("Section called %d times, want %d", n.sections, tt.want)
			}
		})
	}
}
