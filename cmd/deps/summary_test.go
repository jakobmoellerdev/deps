package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

func summaryFixture() *engine.Result {
	checkout := dep.Dep{Manager: "github-actions", File: ".github/workflows/ci.yml", Line: 6, Name: "actions/checkout", Version: "v6.0.0"}
	cobra := dep.Dep{Manager: "gomod", File: "go.mod", Line: 5, Name: "github.com/spf13/cobra", Version: "v1.9.1"}
	vuln := dep.Dep{Manager: "gomod", File: "go.mod", Line: 6, Name: "example.com/vuln", Version: "v1.0.0"}
	skipped := []dep.Dep{
		{File: "go.mod", Line: 7, Name: "example.com/ind1", Skip: "ignored by default (indirect; enable with a rule setting ignore: false)"},
		{File: "go.mod", Line: 8, Name: "example.com/ind2", Skip: "ignored by default (indirect; enable with a rule setting ignore: false)"},
		{File: ".github/workflows/ci.yml", Line: 8, Name: "./local", Skip: "local action"},
	}
	return &engine.Result{
		Updates: []dep.Update{
			{Dep: checkout, Type: dep.Major, NewVersion: "v7.0.1", Bucket: "major", DashboardApproval: true},
			{Dep: cobra, Type: dep.Minor, NewVersion: "v1.10.2", Automerge: true},
			{Dep: vuln, Type: dep.Patch, NewVersion: "v1.0.1", Advisories: []dep.Advisory{{ID: "GO-2026-1"}}},
		},
		Skipped:  skipped,
		Deps:     append([]dep.Dep{checkout, cobra, vuln}, skipped...),
		Pending:  []engine.Pending{{Dep: cobra, Version: "v1.11.0", Reason: "too new (released 2026-11-03, minimum 3 days)"}},
		Warnings: []engine.Warning{{Dep: vuln, Kind: engine.WarnAbandoned, Reason: "no release since 2020-01-01 (abandonmentThreshold 365 days)"}},
	}
}

func TestSummaryRender(t *testing.T) {
	res := summaryFixture()
	groups := engine.GroupUpdates(res.Updates, "")
	tests := []struct {
		name         string
		s            summary
		text, md     []string
		notText      []string
		textExact    string // non-empty = whole text output
		markdownHead string // prefix of the markdown
	}{
		{
			name: "plan with groups",
			s:    summary{Command: "plan", Res: res, Groups: groups, Elapsed: 1500 * time.Millisecond, ShowSkipped: true},
			text: []string{
				"\nSummary: 6 dependencies, 3 updates in 3 PRs, 1 held back by minimumReleaseAge, 3 skipped, 0 errors (1.5s)\n",
				"  PR deps/github-actions/actions-checkout-major  chore(deps): update actions/checkout to v7.0.1  [dashboard approval]\n",
				"      actions/checkout  .github/workflows/ci.yml:6  v6.0.0 -> v7.0.1  (major)\n",
				"  PR deps/gomod/github.com-spf13-cobra  chore(deps): update github.com/spf13/cobra to v1.10.2  [automerge]\n",
				"      example.com/vuln  go.mod:6  v1.0.0 -> v1.0.1  (patch, security: GO-2026-1)\n",
				"  Held back by minimumReleaseAge:\n      github.com/spf13/cobra  go.mod:5  v1.11.0  too new (released 2026-11-03, minimum 3 days)\n",
				"  Deprecated or abandoned:\n      example.com/vuln  go.mod:6  abandoned: no release since 2020-01-01 (abandonmentThreshold 365 days)\n",
				"  Skipped (--show-skipped lists each dependency):\n" +
					"       2  ignored by default (indirect; enable with a rule setting ignore: false)\n" +
					"       1  local action  (actions in this repository (./…) have no upstream version)\n",
			},
			notText:      []string{"example.com/ind1"},
			markdownHead: "## deps plan\n\n**6 dependencies, 3 updates in 3 PRs, 1 held back by minimumReleaseAge, 3 skipped, 0 errors (1.5s)**\n\n### Pull requests `deps run` would open\n\n#### chore(deps): update actions/checkout to v7.0.1\n\nBranch `deps/github-actions/actions-checkout-major` · dashboard approval\n\n| Dependency | File | From | To | Type |\n|---|---|---|---|---|\n| `actions/checkout` | `.github/workflows/ci.yml:6` | `v6.0.0` | `v7.0.1` | major |\n",
			md: []string{
				"Branch `deps/gomod/github.com-spf13-cobra` · automerge\n",
				"| `example.com/vuln` | `go.mod:6` | `v1.0.0` | `v1.0.1` | patch, security: GO-2026-1 |\n",
				"### Held back by minimumReleaseAge\n\n<details><summary>1 release held back (too new, or without a release time under timestamp-required)</summary>\n\n| Dependency | File | Version | Reason |\n|---|---|---|---|\n| `github.com/spf13/cobra` | `go.mod:5` | `v1.11.0` | too new (released 2026-11-03, minimum 3 days) |\n",
				"| ignored by default (indirect; enable with a rule setting ignore: false) | 2 |  |\n| local action | 1 | actions in this repository (`./…`) have no upstream version |",
				"<details><summary>Skipped dependencies</summary>\n\n**ignored by default (indirect; enable with a rule setting ignore: false)**\n\n- `example.com/ind1` `go.mod:7`\n- `example.com/ind2` `go.mod:8`\n",
			},
		},
		{
			name: "run with mixed statuses",
			s: summary{Command: "run", Run: true, Res: res, Groups: groups, Elapsed: time.Second, Actions: []bot.Action{
				{Branch: groups[0].Branch, Title: groups[0].Title, Status: bot.StatusCreated, URL: "https://x/pulls/7", Number: 7},
				{Branch: groups[1].Branch, Title: groups[1].Title, Status: bot.StatusSkipSchedule, Waits: []string{"0 5 * * 1"}},
				{Branch: groups[2].Branch, Title: groups[2].Title, Status: bot.StatusUpToDate, URL: "https://x/pulls/3", Number: 3, Automerge: bot.AutomergeEnabled},
				{Branch: "deps/gomod/old", Title: "chore(deps): update old", Status: bot.StatusClosed, URL: "https://x/pulls/2", Number: 2},
				{Dashboard: true, Title: "Dependency Dashboard", Status: bot.StatusUpdated, URL: "https://x/issues/1", Number: 1},
			}},
			text: []string{
				"PRs: 1 created, 1 skipped: outside schedule, 1 up-to-date, 1 closed,",
				"  PR deps/github-actions/actions-checkout-major  chore(deps): update actions/checkout to v7.0.1  [created https://x/pulls/7]\n",
				"[skipped: outside schedule; waiting for 0 5 * * 1]\n",
				"[up-to-date https://x/pulls/3; automerge enabled]\n",
				"  closed  deps/gomod/old  https://x/pulls/2\n",
				"  updated  Dependency Dashboard  https://x/issues/1\n",
				"  Held back by minimumReleaseAge: 1 (--show-skipped lists each)\n",
				"  Deprecated or abandoned: 1 (--show-skipped lists each)\n",
			},
			md: []string{
				"## deps run\n",
				"### Pull requests\n\n",
				"Branch `deps/github-actions/actions-checkout-major` · created [#7](https://x/pulls/7)\n",
				"· skipped: outside schedule · waiting for 0 5 * * 1\n",
				"· up-to-date [#3](https://x/pulls/3) · automerge enabled\n",
				"### Other pull request actions\n\n| PR | Status | Link |\n|---|---|---|\n| chore(deps): update old | closed | [#2](https://x/pulls/2) |\n| dashboard: Dependency Dashboard | updated | [#1](https://x/issues/1) |\n",
			},
		},
		{
			name: "run with an unprocessed group and errors",
			s: summary{Command: "run --dry-run", Run: true, Res: &engine.Result{Updates: res.Updates[:1], Deps: res.Deps[:1]},
				Groups: groups[:1], Errors: []error{errors.New("go.mod:5: example.com/a: boom"), errors.New("push: denied")}},
			text: []string{"1 dependency, 1 update in 1 PR, 0 skipped, 2 errors (0s)\n", "[not processed (see errors)]\n"},
			md:   []string{"## deps run --dry-run\n", "### Errors\n\n```\ngo.mod:5: example.com/a: boom\npush: denied\n```\n"},
		},
		{
			name: "run with an inapplicable update",
			s: summary{Command: "run", Run: true, Res: &engine.Result{Updates: res.Updates[:1], Deps: res.Deps[:1], Skipped: []dep.Dep{{Name: "actions/checkout", Skip: "module path changed to x (y@v7.0.1)"}}},
				Groups: groups[:1], Actions: []bot.Action{{Branch: groups[0].Branch, Status: bot.StatusSkipApply, Reason: "module path changed to x (y@v7.0.1)"}}},
			text: []string{
				"0 errors (0s)\n", "[skipped: update cannot be applied; module path changed to x (y@v7.0.1)]\n",
				"       1  module path changed to x (y@v7.0.1)  (the new version's go.mod declares another module path; require that path to update)\n",
			},
		},
		{
			name:         "empty",
			s:            summary{Command: "plan", Res: &engine.Result{Deps: res.Deps, Skipped: res.Skipped}},
			textExact:    "\nSummary: No updates: 3 dependencies up to date, 3 skipped.\n  Skipped (--show-skipped lists each dependency):\n",
			markdownHead: "## deps plan\n\n**No updates: 3 dependencies up to date, 3 skipped.**\n\n### Skipped\n",
		},
		{
			name:      "empty without skips",
			s:         summary{Command: "apply", Res: &engine.Result{Deps: res.Deps[:1]}},
			textExact: "\nSummary: No updates: 1 dependency up to date, 0 skipped.\n",
			md:        []string{"## deps apply\n\n**No updates: 1 dependency up to date, 0 skipped.**\n\n"},
		},
		{
			name:      "rules that matched no dependency",
			s:         summary{Command: "plan", Res: &engine.Result{Deps: res.Deps[:1]}, Unused: []int{0, 3}},
			textExact: "warn    rules[0] matched no dependency\nwarn    rules[3] matched no dependency\n\nSummary: No updates: 1 dependency up to date, 0 skipped.\n",
			md:        []string{"### Warnings\n\n- `rules[0]` matched no dependency\n- `rules[3]` matched no dependency\n\n"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.s.writeText(&buf)
			text := buf.String()
			if tc.textExact != "" && !strings.HasPrefix(text, tc.textExact) {
				t.Errorf("text does not start with %q:\n%s", tc.textExact, text)
			}
			for _, want := range tc.text {
				if !strings.Contains(text, want) {
					t.Errorf("text lacks %q:\n%s", want, text)
				}
			}
			for _, bad := range tc.notText {
				if strings.Contains(text, bad) {
					t.Errorf("text contains %q:\n%s", bad, text)
				}
			}
			md := tc.s.markdown()
			if !strings.HasPrefix(md, tc.markdownHead) {
				t.Errorf("markdown does not start with %q:\n%s", tc.markdownHead, md)
			}
			for _, want := range tc.md {
				if !strings.Contains(md, want) {
					t.Errorf("markdown lacks %q:\n%s", want, md)
				}
			}
		})
	}
}

func TestMarkdownTableCellsEscaped(t *testing.T) {
	react := dep.Dep{Manager: "npm", File: "a|b/package.json", Line: 3, Name: "react", Version: "^1 || ^2", Range: "^1 || ^2"}
	u := dep.Update{Dep: react, Type: dep.Pin, NewVersion: "2.0.0"}
	res := &engine.Result{Updates: []dep.Update{u}, Deps: []dep.Dep{react},
		Pending: []engine.Pending{{Dep: react, Version: "3|0", Reason: "too new"}}}
	groups := engine.GroupUpdates(res.Updates, "")
	const row = "| `react` | `a\\|b/package.json:3` | `^1 \\|\\| ^2` | `2.0.0` | pin |\n"
	s := summary{Command: "plan", Res: res, Groups: groups}
	md := s.markdown()
	for _, want := range []string{row, "| `react` | `a\\|b/package.json:3` | `3\\|0` | too new |\n"} {
		if !strings.Contains(md, want) {
			t.Errorf("summary lacks %q:\n%s", want, md)
		}
	}
	var b strings.Builder
	writePRTable(&b, previewPR{Branch: groups[0].Branch, Updates: groups[0].Updates})
	if !strings.Contains(b.String(), row) {
		t.Errorf("preview lacks %q:\n%s", row, b.String())
	}
}

func TestSummaryStepFile(t *testing.T) {
	s := summary{Command: "plan", Res: &engine.Result{}}
	dir := t.TempDir()
	tests := []struct {
		name          string
		github, gitea string
		want          string // file that must receive the markdown; "" = none
	}{
		{"none", "", "", ""},
		{"github", filepath.Join(dir, "gh.md"), "", filepath.Join(dir, "gh.md")},
		{"gitea", "", filepath.Join(dir, "gitea.md"), filepath.Join(dir, "gitea.md")},
		{"github wins", filepath.Join(dir, "both-gh.md"), filepath.Join(dir, "both-gitea.md"), filepath.Join(dir, "both-gh.md")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_STEP_SUMMARY", tc.github)
			t.Setenv("GITEA_STEP_SUMMARY", tc.gitea)
			var stderr bytes.Buffer
			s.write(&stderr)
			s.write(&stderr) // appends
			if !strings.Contains(stderr.String(), "Summary: No updates") {
				t.Errorf("stderr = %q", stderr.String())
			}
			if tc.want == "" {
				return
			}
			got, err := os.ReadFile(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(got), "## deps plan\n") != 2 {
				t.Errorf("%s:\n%s", tc.want, got)
			}
			if tc.gitea != "" && tc.gitea != tc.want {
				if _, err := os.Stat(tc.gitea); !os.IsNotExist(err) {
					t.Errorf("GITEA_STEP_SUMMARY written although GITHUB_STEP_SUMMARY is set")
				}
			}
		})
	}
	t.Run("unwritable", func(t *testing.T) {
		t.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(dir, "missing", "x.md"))
		var stderr bytes.Buffer
		s.write(&stderr)
		if !strings.Contains(stderr.String(), "deps: step summary: ") {
			t.Errorf("stderr = %q", stderr.String())
		}
	})
}

func TestSummaryMarkdownFit(t *testing.T) {
	res := summaryFixture()
	for i := range 2000 {
		res.Skipped = append(res.Skipped, dep.Dep{File: "go.mod", Line: 100 + i, Name: "example.com/skipped-module-with-a-long-name", Skip: "local action"})
	}
	res.Deps = append(res.Deps, res.Skipped[3:]...)
	s := summary{Command: "plan", Res: res, Groups: engine.GroupUpdates(res.Updates, ""), Errors: []error{errors.New("boom")}}
	full := s.markdown()
	if got := s.markdownFit(len(full)); got != full {
		t.Error("a summary within the limit must be unchanged")
	}
	levels := []struct {
		level      int
		has, lacks []string
	}{
		{mdNoSkippedList, []string{"| local action | 2001 |", "The skipped dependencies are left out", "v1.11.0", "| `actions/checkout` |", "boom"}, []string{"skipped-module"}},
		{mdNoPendingList, []string{"1 release held back (list left out", "| `actions/checkout` |", "boom"}, []string{"v1.11.0"}},
		{mdNoPRTables, []string{"#### chore(deps): update actions/checkout to v7.0.1", "1 update (table left out", "boom"}, []string{"| `actions/checkout` |"}},
	}
	for _, l := range levels {
		md := s.markdownAt(l.level)
		if got := s.markdownFit(len(md)); got != md {
			t.Errorf("level %d: fit(%d) did not pick it:\n%s", l.level, len(md), got)
		}
		for _, w := range l.has {
			if !strings.Contains(md, w) {
				t.Errorf("level %d lacks %q", l.level, w)
			}
		}
		for _, w := range l.lacks {
			if strings.Contains(md, w) {
				t.Errorf("level %d contains %q", l.level, w)
			}
		}
	}
	compact := s.markdownAt(mdNoPRTables)
	cut := s.markdownFit(len(compact) - 1)
	if len(cut) >= len(compact) || !strings.HasSuffix(cut, "the job log has everything.**\n") || !strings.HasPrefix(cut, "## deps plan\n") {
		t.Errorf("truncated summary (%d bytes, limit %d):\n%s", len(cut), len(compact)-1, cut)
	}
	if got := s.markdownFit(10); got != "" {
		t.Errorf("fit(10) = %q, want nothing", got)
	}
}

func TestSummaryStepFileLimit(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	res := summaryFixture()
	p := filepath.Join(t.TempDir(), "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", p)
	t.Setenv("GITEA_STEP_SUMMARY", "")
	s := summary{Command: "plan", Res: res, Groups: engine.GroupUpdates(res.Updates, "")}
	// fill makes the file as if earlier commands of the step used all but room bytes.
	fill := func(room int) {
		t.Helper()
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), stepSummaryLimit-room), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}
	fill(len(s.markdownAt(mdNoSkippedList)))
	var stderr bytes.Buffer
	s.write(&stderr)
	if got := read(); len(got) > stepSummaryLimit || !strings.HasSuffix(got, s.markdownAt(mdNoSkippedList)) {
		t.Errorf("file is %d bytes (limit %d), stderr %q", len(got), stepSummaryLimit, stderr.String())
	}

	// Without room for even the truncation note, nothing is appended and deps says so.
	fill(10)
	stderr.Reset()
	s.write(&stderr)
	if got := read(); len(got) != stepSummaryLimit-10 || !strings.Contains(stderr.String(), "deps: step summary: no room\n") {
		t.Errorf("summary: file is %d bytes (limit %d), stderr %q", len(got), stepSummaryLimit, stderr.String())
	}
	work := t.TempDir()
	writeFiles(t, work, map[string]string{"go.mod": gomodSrc})
	fill(10)
	_, errOut, code := execute(t, "preview", "--dir", work, "--base-dir", work)
	if got := read(); code != 0 || len(got) != stepSummaryLimit-10 || !strings.Contains(errOut, "deps: step summary: no room\n") {
		t.Errorf("preview: exit %d, file is %d bytes (limit %d), stderr:\n%s", code, len(got), stepSummaryLimit, errOut)
	}
	// The preview counts what the file already holds.
	fill(len(previewTruncated) + 5)
	if _, errOut, code = execute(t, "preview", "--dir", work, "--base-dir", work); code != 0 {
		t.Fatalf("preview: exit %d\n%s", code, errOut)
	}
	if got := read(); len(got) != stepSummaryLimit || !strings.HasSuffix(got, "x## de"+previewTruncated) {
		t.Errorf("preview: file is %d bytes (limit %d), ends %q", len(got), stepSummaryLimit, got[max(len(got)-200, 0):])
	}

	// A cut without a line boundary stays on a rune boundary.
	if got := truncateMarkdown(strings.Repeat("é", 100), len(previewTruncated)+5, previewTruncated); !utf8.ValidString(got) || len(got) > len(previewTruncated)+5 {
		t.Errorf("truncateMarkdown = %q", got)
	}
}
