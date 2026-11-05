package main

import (
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
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
	if want := "updates=3\nprs=2\nheld-back=0\nskipped=2\nerrors=1\n"; string(got) != want {
		t.Errorf("outputs = %q, want %q", got, want)
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
	if err := s.writeOutputs(p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := "updates=3\nprs=3\ncreated=1\nupdated=1\nclosed=1\nup-to-date=2\nautomerged=2\nprs-skipped=1\nheld-back=1\nskipped=3\nerrors=1\n"
	if string(got) != want {
		t.Errorf("outputs:\n%s\nwant:\n%s", got, want)
	}
}
