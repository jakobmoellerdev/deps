package logx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFormatSelection(t *testing.T) {
	tests := []struct {
		in             string
		tty, pipe, act Format
	}{
		{"auto", Pretty, Text, Actions},
		{"AUTO", Pretty, Text, Actions},
		{"pretty", Pretty, Pretty, Pretty},
		{"text", Text, Text, Text},
		{"json", JSON, JSON, JSON},
		{"actions", Actions, Actions, Actions},
	}
	for _, tt := range tests {
		f, err := ParseFormat(tt.in)
		if err != nil {
			t.Fatalf("%s: %v", tt.in, err)
		}
		if got := f.Resolve(true, false); got != tt.tty {
			t.Errorf("%s on a terminal = %s, want %s", tt.in, got, tt.tty)
		}
		if got := f.Resolve(false, false); got != tt.pipe {
			t.Errorf("%s piped = %s, want %s", tt.in, got, tt.pipe)
		}
		if got := f.Resolve(true, true); got != tt.act {
			t.Errorf("%s on an Actions runner = %s, want %s", tt.in, got, tt.act)
		}
	}
	if _, err := ParseFormat("xml"); err == nil || err.Error() != `invalid value "xml" (auto, pretty, text, json, actions)` {
		t.Errorf("xml: %v", err)
	}
}

func TestColorEnabled(t *testing.T) {
	tests := []struct {
		name string
		mode string
		tty  bool
		env  map[string]string
		want bool
	}{
		{"auto terminal", "auto", true, nil, true},
		{"auto piped", "auto", false, nil, false},
		{"NO_COLOR", "auto", true, map[string]string{"NO_COLOR": "1"}, false},
		{"TERM dumb", "auto", true, map[string]string{"TERM": "dumb"}, false},
		{"TERM xterm", "auto", true, map[string]string{"TERM": "xterm-256color"}, true},
		{"CLICOLOR_FORCE piped", "auto", false, map[string]string{"CLICOLOR_FORCE": "1"}, true},
		{"FORCE_COLOR beats NO_COLOR", "auto", true, map[string]string{"FORCE_COLOR": "1", "NO_COLOR": "1"}, true},
		{"FORCE_COLOR=0", "auto", false, map[string]string{"FORCE_COLOR": "0"}, false},
		{"FORCE_COLOR=false", "auto", false, map[string]string{"FORCE_COLOR": "false"}, false},
		{"always piped with NO_COLOR", "always", false, map[string]string{"NO_COLOR": "1"}, true},
		{"never beats FORCE_COLOR", "never", true, map[string]string{"FORCE_COLOR": "1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseColor(tt.mode)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Enabled(tt.tty, func(k string) string { return tt.env[k] }); got != tt.want {
				t.Errorf("Enabled = %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := ParseColor("rainbow"); err == nil || err.Error() != `invalid value "rainbow" (auto, always, never)` {
		t.Errorf("rainbow: %v", err)
	}
}

func TestIsTerminal(t *testing.T) {
	if IsTerminal(&bytes.Buffer{}) {
		t.Error("a buffer is no terminal")
	}
}

// record logs msg through h at start+at.
func record(t *testing.T, h slog.Handler, start time.Time, at time.Duration, l slog.Level, msg string, args ...any) {
	t.Helper()
	r := slog.NewRecord(start.Add(at), l, msg, 0)
	r.Add(args...)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestPrettyHandler(t *testing.T) {
	var b bytes.Buffer
	start := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	h := NewPrettyHandler(NewWriter(&b), Options{Level: slog.LevelDebug, Start: start})
	record(t, h, start, 3550*time.Millisecond, slog.LevelWarn, "obsolete PR left open", "branch", "deps/x", "errors", 2)
	record(t, h, start, 0, slog.LevelDebug, "files discovered", "dur", 1234567*time.Nanosecond, "walk", 2345678912*time.Nanosecond)
	record(t, h, start, 125*time.Second, slog.LevelInfo, "actions default host", "source", "platform default", "empty", "", "eq", "a=b")
	record(t, h, start, 1500*time.Second, slog.LevelError, "lookup", "err", errors.New("boom: not found"), "ok", true, "ratio", 0.5)
	g := h.WithAttrs([]slog.Attr{slog.String("repo", "r")}).WithGroup("http").WithAttrs([]slog.Attr{slog.Int("try", 1)})
	record(t, g, start, time.Second, slog.LevelInfo, "request", "status", 200, slog.Group("tls", "v", "1.3"), slog.Group("none"), slog.Attr{})
	record(t, h.WithGroup("unused"), start, time.Second, slog.LevelInfo, "no attrs")
	want := "" +
		"  3.55s WRN obsolete PR left open  branch=deps/x errors=2\n" +
		"  0.00s DBG files discovered  dur=1.2ms walk=2.346s\n" +
		"125.00s INF actions default host  source=\"platform default\" empty=\"\" eq=\"a=b\"\n" +
		"  1500s ERR lookup  err=\"boom: not found\" ok=true ratio=0.5\n" +
		"  1.00s INF request  repo=r http.try=1 http.status=200 http.tls.v=1.3\n" +
		"  1.00s INF no attrs\n"
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if h.Enabled(context.Background(), slog.LevelDebug-1) {
		t.Error("below the level is enabled")
	}
}

func TestPrettyHandlerColor(t *testing.T) {
	var b bytes.Buffer
	start := time.Now()
	h := NewPrettyHandler(NewWriter(&b), Options{Color: true, Start: start})
	record(t, h, start, time.Second, slog.LevelWarn, "slow", "err", errors.New("x"), "n", 1)
	want := "\x1b[2m  1.00s\x1b[0m \x1b[33mWRN\x1b[0m slow  \x1b[2merr=\x1b[0m\x1b[31mx\x1b[0m \x1b[2mn=1\x1b[0m\n"
	if got := b.String(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("default level is info")
	}
}

// TestPrettyHandlerConcurrent logs from many goroutines; every record must be one whole line.
func TestPrettyHandlerConcurrent(t *testing.T) {
	var b bytes.Buffer
	l := slog.New(NewPrettyHandler(NewWriter(&b), Options{}))
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			for j := range 50 {
				l.Info("work", "worker", i, "step", j, "pad", strings.Repeat("x", 64))
			}
		})
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 1000 {
		t.Fatalf("%d lines, want 1000", len(lines))
	}
	for _, ln := range lines {
		if !strings.Contains(ln, " INF work  worker=") || !strings.HasSuffix(ln, strings.Repeat("x", 64)) {
			t.Fatalf("torn line %q", ln)
		}
	}
}

// overlay is a status line that logs its calls.
type overlay struct{ text string }

func (o *overlay) Clear(w io.Writer) { fmt.Fprint(w, "[clear]") }
func (o *overlay) Draw(w io.Writer)  { fmt.Fprintf(w, "[%s]", o.text) }

func TestWriterOverlay(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	fmt.Fprint(w, "a\n")
	o := &overlay{text: "1/3"}
	w.SetOverlay(o)
	fmt.Fprint(w, "b\n")
	o.text = "2/3"
	w.Redraw()
	w.SetOverlay(nil)
	w.Redraw()
	fmt.Fprint(w, "c\n")
	want := "a\n[1/3][clear]b\n[1/3][clear][2/3][clear]c\n"
	if got := b.String(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestStyle(t *testing.T) {
	if got := NewStyle(false).Paint(Red, "x"); got != "x" {
		t.Errorf("off: %q", got)
	}
	on := NewStyle(true)
	if got := on.Paint(BoldRed, "x"); got != "\x1b[1;31mx\x1b[0m" {
		t.Errorf("on: %q", got)
	}
	if on.Paint("", "x") != "x" || on.Paint(Red, "") != "" || !on.Color() {
		t.Error("no color or no text must stay plain")
	}
}

func TestRoundDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		1234567 * time.Nanosecond:    "1.2ms",
		987 * time.Nanosecond:        "0s",
		1234567891 * time.Nanosecond: "1.235s",
		-1234567 * time.Nanosecond:   "-1.2ms",
	} {
		if got := RoundDuration(d).String(); got != want {
			t.Errorf("%v: %s, want %s", d, got, want)
		}
	}
}

func TestActionsHandler(t *testing.T) {
	var b bytes.Buffer
	h := NewHandler(Actions, NewWriter(&b), Options{Level: slog.LevelDebug})
	slog.New(h).Debug("m", "k", 1, "d", 1500*time.Microsecond)
	if got, want := b.String(), "DBG m  k=1 d=1.5ms\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
