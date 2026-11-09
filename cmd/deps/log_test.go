package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
)

// TestReportLayout: piped, every update gets a flat line; on a terminal the summary alone
// lists them, grouped by PR; error lines stay either way, red with colors.
func TestReportLayout(t *testing.T) {
	res := summaryFixture()
	errs := []error{errors.New("go.mod:9: example.com/x: boom")}
	var piped, tty, color bytes.Buffer
	report(&piped, terminal{}, false, res, errs, nil)
	report(&tty, terminal{tty: true}, false, res, errs, nil)
	report(&color, terminal{tty: true, style: logx.NewStyle(true)}, false, res, errs, nil)
	if n := strings.Count(piped.String(), "update  "); n != len(res.Updates) {
		t.Errorf("piped: %d update lines, want %d:\n%s", n, len(res.Updates), piped.String())
	}
	if got, want := tty.String(), "error   go.mod:9: example.com/x: boom\n"; got != want {
		t.Errorf("terminal:\n%q\nwant\n%q", got, want)
	}
	if got, want := color.String(), "\x1b[31merror\x1b[0m   go.mod:9: example.com/x: boom\n"; got != want {
		t.Errorf("color:\n%q\nwant\n%q", got, want)
	}
}

func TestSummaryColors(t *testing.T) {
	res := summaryFixture()
	s := summary{Command: "plan", Res: res, Groups: engine.GroupUpdates(res.Updates, ""), Errors: []error{errors.New("boom")}, Elapsed: time.Second}
	var plainOut bytes.Buffer
	s.writeText(&plainOut)
	if strings.Contains(plainOut.String(), "\x1b[") {
		t.Fatalf("colors without a style:\n%q", plainOut.String())
	}
	s.Style = logx.NewStyle(true)
	var b bytes.Buffer
	s.writeText(&b)
	got := b.String()
	for _, want := range []string{
		"\x1b[1mSummary:\x1b[0m ",
		", \x1b[31m1 error\x1b[0m (1s)",
		"  PR \x1b[1;35mdeps/github-actions/actions-checkout-major\x1b[0m  chore(deps): update actions/checkout to v7.0.1  [dashboard approval]\n",
		"      actions/checkout  \x1b[2m.github/workflows/ci.yml:6\x1b[0m  v6.0.0 -> v7.0.1  (\x1b[31mmajor\x1b[0m)\n",
		"(\x1b[33mminor\x1b[0m)",
		"(\x1b[32mpatch\x1b[0m, \x1b[1;31msecurity: GO-2026-1\x1b[0m)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%q", want, got)
		}
	}
	if md := s.markdown(); strings.Contains(md, "\x1b[") {
		t.Errorf("markdown has colors:\n%q", md)
	}
}

// TestLogFormatSelection: the flag beats $DEPS_LOG_FORMAT, which beats auto (text when
// piped); --color beats the environment.
func TestLogFormatSelection(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".github/workflows/ci.yml": workflow})
	const (
		text   = `level=INFO msg="actions default host" host=github.com source="platform default"`
		pretty = ` INF actions default host  host=github.com source="platform default"`
	)
	run := func(args ...string) string {
		t.Helper()
		_, stderr, _ := execute(t, append([]string{"plan", "--dir", dir, "--log-level", "info"}, args...)...)
		return stderr
	}
	if got := run(); !strings.Contains(got, text) || strings.Contains(got, "\x1b[") {
		t.Errorf("auto piped is not plain text:\n%s", got)
	}
	if got := run("--log-format", "pretty"); !strings.Contains(got, pretty) || strings.Contains(got, "level=") {
		t.Errorf("--log-format pretty:\n%s", got)
	}
	t.Setenv("DEPS_LOG_FORMAT", "pretty")
	if got := run(); !strings.Contains(got, pretty) {
		t.Errorf("$DEPS_LOG_FORMAT=pretty:\n%s", got)
	}
	if got := run("--log-format", "text"); !strings.Contains(got, text) {
		t.Errorf("flag did not override $DEPS_LOG_FORMAT:\n%s", got)
	}
	t.Setenv("DEPS_LOG_FORMAT", "")
	if got := run("--color", "always"); !strings.Contains(got, "\x1b[1mSummary:\x1b[0m") || !strings.Contains(got, "update  .github/workflows/ci.yml:") {
		t.Errorf("--color always piped: want colors and the piped layout:\n%s", got)
	}
	t.Setenv("FORCE_COLOR", "1")
	if got := run(); !strings.Contains(got, "\x1b[") {
		t.Errorf("FORCE_COLOR piped has no colors:\n%s", got)
	}
	if got := run("--color", "never"); strings.Contains(got, "\x1b[") {
		t.Errorf("--color never has colors:\n%s", got)
	}
}

func TestTextLogRoundsDurations(t *testing.T) {
	defer slog.SetDefault(slog.Default())
	for _, tc := range []struct {
		format string
		d      time.Duration
		want   string
	}{
		{"text", 12345678 * time.Nanosecond, "duration=12.3ms"},
		{"text", 1234567890 * time.Nanosecond, "duration=1.235s"},
		{"json", 12345678 * time.Nanosecond, `"duration":12345678`},
	} {
		var b bytes.Buffer
		if _, err := setupLogger(logOpts{level: "info", format: tc.format, color: "never"}, logx.NewWriter(&b), false, false, func(string) string { return "" }); err != nil {
			t.Fatal(err)
		}
		slog.Info("x", "duration", tc.d)
		if !strings.Contains(b.String(), tc.want) {
			t.Errorf("%s %v: %q lacks %q", tc.format, tc.d, b.String(), tc.want)
		}
	}
}

// A PR's records come out as one block when it is done: plain, or with the actions format
// inside a group titled with branch, status and duration; when the context ends first, at
// once.
func TestPRLog(t *testing.T) {
	defer slog.SetDefault(slog.Default())
	t.Cleanup(func() { actionsLog = &annotator{} })
	for _, tc := range []struct {
		actions bool
		want    string
	}{
		{false, "level=INFO msg=free\nlevel=INFO msg=a\nlevel=INFO msg=b\n"},
		{true, "INF free\n::group::deps/x: created (1.5s)\nINF a\nINF b\n::endgroup::\n"},
	} {
		var b bytes.Buffer
		w := logx.NewWriter(&b)
		actionsLog = &annotator{}
		format := "text"
		if tc.actions {
			format = "actions"
		}
		if _, err := setupLogger(logOpts{level: "info", format: format, color: "never"}, w, false, false, func(string) string { return "" }); err != nil {
			t.Fatal(err)
		}
		ctx, done := prLog(w)(t.Context(), "deps/x")
		slog.InfoContext(ctx, "a")
		slog.Info("free")
		slog.InfoContext(ctx, "b")
		done("created", 1500*time.Millisecond)
		if got := regexp.MustCompile(`time=\S+ `).ReplaceAllString(b.String(), ""); got != tc.want {
			t.Errorf("actions=%v:\n%q\nwant\n%q", tc.actions, got, tc.want)
		}
	}
	var b syncBuffer
	w := logx.NewWriter(&b)
	actionsLog = &annotator{on: true, w: w}
	parent, cancel := context.WithCancel(t.Context())
	ctx, done := prLog(w)(parent, "deps/y")
	slog.InfoContext(ctx, "c")
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(b.String(), "::endgroup::") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	done("error", time.Second)
	if got := regexp.MustCompile(`time=\S+ `).ReplaceAllString(b.String(), ""); got != "::group::deps/y: canceled\nINF c\n::endgroup::\n" {
		t.Errorf("canceled: %q", got)
	}
}
