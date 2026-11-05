package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// logOpts select the diagnostics logger. Results (update/skip/pr lines, diffs) are not logs and
// are always printed; logs go to stderr next to them.
type logOpts struct {
	level  string
	format string
}

// setupLogger installs the default slog logger writing to w.
func setupLogger(o logOpts, w io.Writer) error {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(o.level)); err != nil {
		return &usageError{fmt.Errorf("--log-level: invalid value %q (debug, info, warn, error)", o.level)}
	}
	ho := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch strings.ToLower(o.format) {
	case "text":
		h = slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl, ReplaceAttr: roundDuration})
	case "json":
		h = slog.NewJSONHandler(w, ho)
	default:
		return &usageError{fmt.Errorf("--log-format: invalid value %q (text, json)", o.format)}
	}
	if actionsLog.on {
		h = &annotatingHandler{Handler: h, a: actionsLog}
	}
	slog.SetDefault(slog.New(h))
	return nil
}

// roundDuration shortens duration attrs in text logs to a readable precision: 0.1ms below a
// second, 1ms above (12.3ms, 1.234s instead of 12.345678ms). JSON keeps nanoseconds.
func roundDuration(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindDuration {
		return a
	}
	d := a.Value.Duration()
	if d < time.Second && d > -time.Second {
		d = d.Round(100 * time.Microsecond)
	} else {
		d = d.Round(time.Millisecond)
	}
	return slog.Duration(a.Key, d)
}
