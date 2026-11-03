package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// logOpts select the diagnostics logger. Results (update/skip/pr lines, diffs) are not logs and
// are always printed; logs go to stderr next to them.
type logOpts struct {
	level  string
	format string
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
		h = slog.NewTextHandler(w, ho)
	case "json":
		h = slog.NewJSONHandler(w, ho)
	default:
		return &usageError{fmt.Errorf("--log-format: invalid value %q (text, json)", o.format)}
	}
	slog.SetDefault(slog.New(h))
	return nil
}
