package main

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestPlanReportAndOutputs(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow, "go.mod": gomodSrc + "require example.com/missing v1.0.0\n"})
	out := filepath.Join(t.TempDir(), "out")
	t.Setenv("GITHUB_OUTPUT", out)
	t.Setenv("DEPS_REPORT", filepath.Join(t.TempDir(), "env.json"))
	report := filepath.Join(t.TempDir(), "report.json")

	_, stderr, code := execute(t, "plan", "--dir", dir, "--report", report)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var r jsonReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Command != "plan" || len(r.PRs) != 2 || r.Counts["updates"] != 3 || r.Counts["prs"] != 2 || r.Counts["errors"] != 1 {
		t.Errorf("report = %s", b)
	}
	if e := r.Errors[0]; e.File != "go.mod" || e.Line != 6 || !strings.HasPrefix(e.Message, "go.mod:6: example.com/missing: ") {
		t.Errorf("error = %+v", e)
	}
	if u := r.PRs[1].Updates[0]; u.Name != "example.com/a" || u.From != "v1.0.0" || u.To != "v1.1.0" || u.Type != "minor" || u.Line != 5 {
		t.Errorf("update = %+v", u)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(os.Getenv("DEPS_REPORT")), "env.json")); !os.IsNotExist(err) {
		t.Error("--report must win over DEPS_REPORT")
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "updates=3\nprs=2\nheld-back=0\nskipped=2\nrate-limited=0\nerrors=1\n"; string(got) != want {
		t.Errorf("outputs = %q, want %q", got, want)
	}
}

// A lookup that fails on an exhausted rate limit is a warning: listed apart from the errors,
// annotated as a warning and not failing plan.
func TestPlanRateLimitedIsWarning(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	f.mux.HandleFunc("GET proxy.test/example.com/limited/@v/list", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"go.mod": gomodSrc + "require example.com/limited v1.0.0\n"})
	out := filepath.Join(t.TempDir(), "out")
	t.Setenv("GITHUB_OUTPUT", out)
	t.Setenv("GITHUB_ACTIONS", "true")
	report := filepath.Join(t.TempDir(), "report.json")

	_, stderr, code := execute(t, "plan", "--dir", dir, "--report", report)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"warn\x1b[0m    go.mod:6: example.com/limited: GET https://proxy.test/example.com/limited/@v/list: HTTP 429: rate limited by proxy.test until ",
		"go.mod,line=6,title=deps::example.com/limited: ",
		"::warning file=",
		"  example.com/limited  skipped: rate limited by proxy.test until ",
		"1 skipped, \x1b[33m1 rate limited\x1b[0m, 0 errors (",
		"  Rate limited: 1 lookup (the next run looks them up again",
		"1 updates in 1 files, 1 skipped, 1 rate limited, 0 errors\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "::error") || strings.Contains(stderr, "error:") || strings.Contains(stderr, "\x1b[31merror") {
		t.Errorf("rate limit reported as an error:\n%s", stderr)
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var r jsonReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Errors) != 0 || len(r.RateLimited) != 1 || r.RateLimited[0].File != "go.mod" || r.RateLimited[0].Line != 6 ||
		r.Counts["rateLimited"] != 1 || r.Counts["errors"] != 0 {
		t.Errorf("report = %s", b)
	}
	if got, _ := os.ReadFile(out); !strings.Contains(string(got), "rate-limited=1\nerrors=0\n") {
		t.Errorf("outputs = %q", got)
	}
}

func TestPlanReportWriteFailureExits1(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow, "go.mod": gomodSrc})
	report := filepath.Join(t.TempDir(), "missing", "x.json")

	_, stderr, code := execute(t, "plan", "--dir", dir, "--report", report)
	if code != 1 || !strings.Contains(stderr, "deps: report: ") {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
}

func TestReportUnusedRules(t *testing.T) {
	res := summaryFixture()
	s := summary{Command: "plan", Res: res, Unused: []int{1, 3}}
	if r := s.report(); !slices.Equal(r.UnusedRules, []int{1, 3}) {
		t.Errorf("unusedRules = %v", r.UnusedRules)
	}
	s.Unused = nil
	if r := s.report(); r.UnusedRules == nil || len(r.UnusedRules) != 0 {
		t.Errorf("no unused rules: %#v, want []", r.UnusedRules)
	}
}

func TestRunCounts(t *testing.T) {
	res := summaryFixture()
	s := summary{Command: "run", Run: true, Res: res, Groups: engine.GroupUpdates(res.Updates, ""), Errors: []error{errors.New("x")}, Actions: []bot.Action{
		{Branch: "a", Status: bot.StatusCreated, Automerge: bot.AutomergeRequested},
		{Branch: "b", Status: bot.StatusRebased},
		{Branch: "c", Status: bot.StatusUpToDate, Automerge: bot.AutomergeMerged},
		{Branch: "d", Status: bot.StatusClosed},
		{Branch: "e", Status: bot.StatusSkipLimit},
		{Branch: "f", Status: bot.StatusUpToDate, Automerge: bot.AutomergeWaiting},
		{Title: "Dependency Dashboard", Status: bot.StatusCreated, Dashboard: true},
	}}
	p := filepath.Join(t.TempDir(), "out")
	if err := writeOutputs(p, s.counts()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "updates=3\nprs=3\ncreated=1\nupdated=1\nclosed=1\nup-to-date=2\nautomerged=2\nprs-skipped=1\nheld-back=1\nskipped=3\nrate-limited=0\nerrors=1\n"
	if string(got) != want {
		t.Errorf("outputs:\n%s\nwant:\n%s", got, want)
	}
}

func TestReportWarnings(t *testing.T) {
	res := summaryFixture()
	r := (&summary{Command: "plan", Res: res, Groups: engine.GroupUpdates(res.Updates, "")}).report()
	want := []jsonWarning{{Name: "example.com/vuln", File: "go.mod", Line: 6, Kind: "abandoned", Reason: "no release since 2020-01-01 (abandonmentThreshold 365 days)"}}
	if !slices.Equal(r.Warnings, want) {
		t.Errorf("warnings = %+v, want %+v", r.Warnings, want)
	}
}

// A multi-repository run whose repositories all fail still writes every step output.
func TestMultiRunCountsAllFailed(t *testing.T) {
	cleanEnv(t)
	p := filepath.Join(t.TempDir(), "out")
	t.Setenv("GITHUB_OUTPUT", p)
	m := newMultiRun("run")
	m.fail("o/r", errors.New("clone failed"))
	if err := m.write(io.Discard, "", 0); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "updates=0\nprs=0\ncreated=0\nupdated=0\nclosed=0\nup-to-date=0\nautomerged=0\nprs-skipped=0\nheld-back=0\nskipped=0\nrate-limited=0\nerrors=1\n"
	if string(got) != want {
		t.Errorf("outputs:\n%s\nwant:\n%s", got, want)
	}
}

// Repositories are listed once, where they first appear, whatever their case.
func TestExpandRepositoriesDedupe(t *testing.T) {
	got, err := expandRepositories(t.Context(), platform.Info{}, "", nil, []string{"o/r", "o/s", "O/R", "o/S", "o/t", "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"o/r", "o/s", "o/t"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
