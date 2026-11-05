// Package logx renders deps' stderr: the selection of the log format and of colors, the
// shared terminal writer and the compact terminal log handler.
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"
)

// Format is a --log-format value.
type Format string

const (
	Auto   Format = "auto"   // Pretty on a terminal, else Text
	Pretty Format = "pretty" // compact colored records for terminals (PrettyHandler)
	Text   Format = "text"   // slog.TextHandler, the documented line format
	JSON   Format = "json"   // slog.JSONHandler
)

// Formats are the accepted --log-format values.
var Formats = []Format{Auto, Pretty, Text, JSON}

// ParseFormat parses a --log-format value, ignoring case.
func ParseFormat(s string) (Format, error) {
	f := Format(strings.ToLower(s))
	if slices.Contains(Formats, f) {
		return f, nil
	}
	return "", fmt.Errorf("invalid value %q (%s)", s, join(Formats))
}

// Resolve returns the concrete format: Auto becomes Pretty when stderr is a terminal, else
// Text; the other formats stay.
func (f Format) Resolve(tty bool) Format {
	if f != Auto {
		return f
	}
	if tty {
		return Pretty
	}
	return Text
}

// Options configure the handler of NewHandler.
type Options struct {
	Level slog.Leveler
	Color bool      // Pretty only: ANSI colors
	Start time.Time // Pretty only: origin of the elapsed time; zero = when the program started
}

// NewHandler returns the handler of a resolved format writing to w.
func NewHandler(f Format, w *Writer, o Options) slog.Handler {
	switch f {
	case Pretty:
		return NewPrettyHandler(w, o)
	case JSON:
		return slog.NewJSONHandler(w, &slog.HandlerOptions{Level: o.Level})
	default:
		return slog.NewTextHandler(w, &slog.HandlerOptions{Level: o.Level, ReplaceAttr: roundDurationAttr})
	}
}

// roundDurationAttr rounds duration attrs of text records (RoundDuration): 12.3ms, 1.235s
// instead of 12.345678ms. JSON keeps nanoseconds.
func roundDurationAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindDuration {
		return a
	}
	return slog.Duration(a.Key, RoundDuration(a.Value.Duration()))
}

// ColorMode is a --color value.
type ColorMode string

const (
	ColorAuto   ColorMode = "auto"   // on for a terminal, unless the environment says otherwise
	ColorAlways ColorMode = "always" // on, also when piped
	ColorNever  ColorMode = "never"  // off
)

// ColorModes are the accepted --color values.
var ColorModes = []ColorMode{ColorAuto, ColorAlways, ColorNever}

// ParseColor parses a --color value, ignoring case.
func ParseColor(s string) (ColorMode, error) {
	c := ColorMode(strings.ToLower(s))
	if slices.Contains(ColorModes, c) {
		return c, nil
	}
	return "", fmt.Errorf("invalid value %q (%s)", s, join(ColorModes))
}

// Enabled reports whether output is colored. Always and never are final. Auto is on when
// CLICOLOR_FORCE or FORCE_COLOR is set (to anything but "0" or "false"), else on for a
// terminal unless NO_COLOR is set or TERM is "dumb".
func (c ColorMode) Enabled(tty bool, getenv func(string) string) bool {
	switch c {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	}
	if forced(getenv("CLICOLOR_FORCE")) || forced(getenv("FORCE_COLOR")) {
		return true
	}
	return tty && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

func forced(v string) bool { return v != "" && v != "0" && !strings.EqualFold(v, "false") }

// IsTerminal reports whether w is a terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(f.Fd())) //nolint:gosec // file descriptors fit an int
}

// RoundDuration rounds d for display: to 0.1ms below a second, else to the millisecond.
func RoundDuration(d time.Duration) time.Duration {
	if d < time.Second && d > -time.Second {
		return d.Round(100 * time.Microsecond)
	}
	return d.Round(time.Millisecond)
}

func join[T ~string](vs []T) string {
	s := make([]string, len(vs))
	for i, v := range vs {
		s[i] = string(v)
	}
	return strings.Join(s, ", ")
}
