package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
)

// logOpts select the diagnostics logger and the colors of stderr. Results (update/skip/pr
// lines, diffs) are not logs and are always printed; logs go to stderr next to them.
type logOpts struct {
	level  string
	format string // --log-format, logx.Format
	color  string // --color, logx.ColorMode
}

// terminal is how stderr renders the results: on a terminal (tty) the summary alone lists the
// updates, grouped by PR, instead of also one flat update line each; style colors them.
// Piped stderr keeps the documented line format byte for byte.
type terminal struct {
	tty   bool
	style logx.Style
}

// errorLabel starts an error line: "error", red with colors.
func (t terminal) errorLabel() string { return t.style.Paint(logx.Red, "error") }

// setupLogger installs the default slog logger writing to w and returns how the results on
// w are rendered. tty tells whether w is a terminal.
func setupLogger(o logOpts, w *logx.Writer, tty bool, getenv func(string) string) (terminal, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(o.level)); err != nil {
		return terminal{}, &usageError{fmt.Errorf("--log-level: invalid value %q (debug, info, warn, error)", o.level)}
	}
	f, err := logx.ParseFormat(o.format)
	if err != nil {
		return terminal{}, &usageError{fmt.Errorf("--log-format: %w", err)}
	}
	c, err := logx.ParseColor(o.color)
	if err != nil {
		return terminal{}, &usageError{fmt.Errorf("--color: %w", err)}
	}
	f = f.Resolve(tty, actionsLog.runner)
	color := c.Enabled(tty, getenv)
	if f == logx.Actions && c == logx.ColorAuto {
		// GitHub and Gitea render ANSI colors in job logs, whatever TERM says.
		color = color || getenv("NO_COLOR") == ""
	}
	ho := logx.Options{Level: lvl, Color: color}
	// Records of a PR (bot.Options.PRLog) are held per PR and written as one block.
	var h slog.Handler = logx.NewBufferHandler(logx.NewHandler(f, w, ho), func(b io.Writer) slog.Handler {
		return logx.NewHandler(f, logx.NewWriter(b), ho)
	})
	if f == logx.Actions {
		actionsLog.on = true
		h = &annotatingHandler{Handler: h, a: actionsLog}
	}
	slog.SetDefault(slog.New(h))
	return terminal{tty: tty, style: logx.NewStyle(color)}, nil
}

// prLog is bot.Options.PRLog: the records of each PR are held in a logx.Buffer and written to
// w as one block when the PR is done, or when ctx ends first; with the actions format inside a
// log group titled `<branch>: <status> (<duration>)`.
func prLog(w io.Writer) func(context.Context, string) (context.Context, func(string, time.Duration)) {
	return func(ctx context.Context, branch string) (context.Context, func(string, time.Duration)) {
		b := &logx.Buffer{}
		flush := func(title string) {
			header, footer := "", ""
			if actionsLog.on {
				header, footer = "::group::"+escapeData(title)+"\n", "::endgroup::\n"
			}
			_ = b.Flush(w, header, footer) // a failed stderr write has nowhere to be reported
		}
		stop := context.AfterFunc(ctx, func() { flush(branch + ": canceled") })
		return logx.WithBuffer(ctx, b), func(status string, d time.Duration) {
			stop()
			flush(fmt.Sprintf("%s: %s (%s)", branch, status, logx.RoundDuration(d)))
		}
	}
}
