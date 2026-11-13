package logx

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// processStart approximates when the program started: when this package was initialized.
var processStart = time.Now()

// PrettyHandler writes compact records for terminals, one line each:
//
//	3.55s WRN message  key=value group.key=value
//
// The elapsed time since Options.Start is right-aligned, the level has three letters, durations
// are rounded (RoundDuration) and, with Options.Color, the level is colored and the attributes
// are dim (errors red).
type PrettyHandler struct {
	w      *Writer
	level  slog.Leveler
	color  bool
	start  time.Time
	noTime bool   // the Actions format: no elapsed column (the runner timestamps each line)
	prefix string // the open groups, "a.b."
	pre    []byte // attributes of WithAttrs, formatted, each with a leading space
}

// NewPrettyHandler returns a PrettyHandler writing to w.
func NewPrettyHandler(w *Writer, o Options) *PrettyHandler {
	h := &PrettyHandler{w: w, level: o.Level, color: o.Color, start: o.Start}
	if h.level == nil {
		h.level = slog.LevelInfo
	}
	if h.start.IsZero() {
		h.start = processStart
	}
	return h
}

func (h *PrettyHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, 0, 256); return &b }}

// Handle formats r and writes it with a single Write.
func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	bp := bufPool.Get().(*[]byte)
	b := (*bp)[:0]
	var d time.Duration
	if !r.Time.IsZero() && !h.noTime {
		d = max(r.Time.Sub(h.start), 0)
	}
	if !h.noTime {
		b = appendPaint(b, h.color, Dim, elapsed(d))
		b = append(b, ' ')
	}
	lvl, sgr := levelLabel(r.Level)
	b = appendPaint(b, h.color, sgr, lvl)
	b = append(b, ' ')
	b = appendMessage(b, r.Message)
	mark := len(b)
	b = append(b, h.pre...)
	r.Attrs(func(a slog.Attr) bool {
		b = h.appendAttr(b, h.prefix, a)
		return true
	})
	if len(b) > mark {
		b = slices.Insert(b, mark, ' ') // two spaces between message and attributes
	}
	b = append(b, '\n')
	_, err := h.w.Write(b)
	*bp = b
	bufPool.Put(bp)
	return err
}

func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := *h
	c.pre = slices.Clip(h.pre)
	for _, a := range attrs {
		c.pre = h.appendAttr(c.pre, h.prefix, a)
	}
	return &c
}

func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.prefix = h.prefix + name + "."
	return &c
}

// elapsed renders d right-aligned in seconds: "  3.55s", "123.40s", "1234s".
func elapsed(d time.Duration) string {
	s := d.Seconds()
	var b []byte
	if s < 1000 {
		b = strconv.AppendFloat(make([]byte, 0, 8), s, 'f', 2, 64)
	} else {
		b = strconv.AppendFloat(make([]byte, 0, 8), s, 'f', 0, 64)
	}
	for len(b) < 6 {
		b = slices.Insert(b, 0, ' ')
	}
	return string(append(b, 's'))
}

// levelLabel returns the three-letter label of l and its color.
func levelLabel(l slog.Level) (string, string) {
	switch {
	case l < slog.LevelInfo:
		return "DBG", Dim
	case l < slog.LevelWarn:
		return "INF", Cyan
	case l < slog.LevelError:
		return "WRN", Yellow
	default:
		return "ERR", Red
	}
}

// appendAttr appends " key=value", groups flattened to "group.key=value"; empty attributes
// and empty groups are left out, as by the slog handlers.
func (h *PrettyHandler) appendAttr(b []byte, prefix string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return b
	}
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			b = h.appendAttr(b, prefix, ga)
		}
		return b
	}
	b = append(b, ' ')
	if h.color {
		b = append(b, "\x1b["+Dim+"m"...)
	}
	b = append(b, prefix...)
	b = append(b, a.Key...)
	b = append(b, '=')
	isErr := false
	if a.Value.Kind() == slog.KindAny {
		_, isErr = a.Value.Any().(error)
	}
	if h.color && isErr {
		b = append(b, "\x1b[0m\x1b["+Red+"m"...) // the key stays dim, the error is red
	}
	b = appendValue(b, a.Value)
	if h.color {
		b = append(b, "\x1b[0m"...)
	}
	return b
}

func appendValue(b []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		return appendString(b, v.String())
	case slog.KindInt64:
		return strconv.AppendInt(b, v.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(b, v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.AppendFloat(b, v.Float64(), 'g', -1, 64)
	case slog.KindBool:
		return strconv.AppendBool(b, v.Bool())
	case slog.KindDuration:
		return append(b, RoundDuration(v.Duration()).String()...)
	case slog.KindTime:
		return v.Time().AppendFormat(b, time.RFC3339)
	}
	if err, ok := v.Any().(error); ok {
		return appendString(b, err.Error())
	}
	return appendString(b, fmt.Sprint(v.Any()))
}

// appendString appends s, quoted when it is empty or has spaces, '=', '"' or unprintable runes.
func appendString(b []byte, s string) []byte {
	if needsQuoting(s) {
		return strconv.AppendQuote(b, s)
	}
	return append(b, s...)
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		if r <= ' ' || r == '=' || r == '"' || r == utf8.RuneError || !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}

// appendMessage appends the message of a record, which may hold spaces, quoted when it holds
// a control or other unprintable rune: a newline would break the record across lines, and a
// continuation line starting with "::" would reach a GitHub Actions runner as a workflow
// command.
func appendMessage(b []byte, s string) []byte {
	for _, r := range s {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return strconv.AppendQuote(b, s)
		}
	}
	return append(b, s...)
}
