package main

import (
	"fmt"
	"log/slog"

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
	color := c.Enabled(tty, getenv)
	f = f.Resolve(tty, actionsLog.runner)
	h := logx.NewHandler(f, w, logx.Options{Level: lvl, Color: color})
	if f == logx.Actions {
		actionsLog.on = true
		h = &annotatingHandler{Handler: h, a: actionsLog}
	}
	slog.SetDefault(slog.New(h))
	return terminal{tty: tty, style: logx.NewStyle(color)}, nil
}
