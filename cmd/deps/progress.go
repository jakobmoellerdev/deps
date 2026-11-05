package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
)

// statusTick is the redraw interval of the status line.
const statusTick = 100 * time.Millisecond

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// phaseLabels name the phases on the status line.
var phaseLabels = map[engine.Phase]string{
	engine.PhaseDiscover: "discovering files",
	engine.PhaseExtract:  "extracting",
	engine.PhaseResolve:  "resolving",
	engine.PhasePRs:      "PRs",
}

// statusLine is the live status line of a terminal: an engine.Progress that shows the
// running phase as the logx.Writer overlay, below every record, and redraws it every
// statusTick until the phase ends; then the line is removed. Only the phase goroutine calls
// Start and End; Done and RateLimited may come from any goroutine.
type statusLine struct {
	w        *logx.Writer
	style    logx.Style
	inFlight func() []httpx.PoolUse // nil = not shown
	width    func() int             // terminal columns
	now      func() time.Time
	tick     time.Duration // 0 = no ticker (tests redraw by hand)

	done, errs atomic.Int64

	mu           sync.Mutex // guards the fields below; taken inside the Writer's lock by Draw
	phase        engine.Phase
	total        int
	start        time.Time
	limitedHost  string
	limitedUntil time.Time

	stop    chan struct{}
	stopped chan struct{}
}

// newStatusLine returns the status line of the terminal behind w, out.
func newStatusLine(w *logx.Writer, out io.Writer, style logx.Style, limits httpx.Limits) *statusLine {
	return &statusLine{w: w, style: style, inFlight: limits.InFlight, width: termWidth(out), now: time.Now, tick: statusTick}
}

// termWidth returns the column count of the terminal out, 80 when unknown.
func termWidth(out io.Writer) func() int {
	return func() int {
		if f, ok := out.(interface{ Fd() uintptr }); ok {
			if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 { //nolint:gosec // file descriptors fit an int
				return w
			}
		}
		return 80
	}
}

func (s *statusLine) Start(p engine.Phase, total int) {
	s.mu.Lock()
	s.phase, s.total, s.start = p, total, s.now()
	s.limitedHost, s.limitedUntil = "", time.Time{}
	s.mu.Unlock()
	s.done.Store(0)
	s.errs.Store(0)
	s.w.SetOverlay(s)
	if s.tick > 0 {
		s.stop, s.stopped = make(chan struct{}), make(chan struct{})
		go s.ticker(s.stop, s.stopped)
	}
}

func (s *statusLine) ticker(stop, stopped chan struct{}) {
	defer close(stopped)
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.w.Redraw()
		case <-stop:
			return
		}
	}
}

func (s *statusLine) Done(_ engine.Phase, err error) {
	s.done.Add(1)
	if err != nil {
		s.errs.Add(1)
	}
}

func (s *statusLine) End(engine.Phase) {
	if s.stop != nil {
		close(s.stop)
		<-s.stopped
		s.stop = nil
	}
	s.w.SetOverlay(nil)
}

func (s *statusLine) RateLimited(host string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until.After(s.limitedUntil) {
		s.limitedHost, s.limitedUntil = host, until
	}
}

// Clear erases the status line; the cursor is at its start afterwards.
func (s *statusLine) Clear(w io.Writer) { _, _ = io.WriteString(w, "\r\x1b[2K") }

// Draw writes the status line without a newline, cut to the terminal width so it never
// wraps (Clear only erases one line).
func (s *statusLine) Draw(w io.Writer) {
	_, _ = io.WriteString(w, s.render())
}

// render returns the status line: `⠹ resolving 212/308 · npm 18 in flight · 2.1s`.
func (s *statusLine) render() string {
	s.mu.Lock()
	now := s.now()
	elapsed := now.Sub(s.start)
	var b strings.Builder
	b.WriteString(phaseLabels[s.phase])
	if s.total > 0 {
		fmt.Fprintf(&b, " %d/%d", s.done.Load(), s.total)
	}
	if s.limitedHost != "" && now.Before(s.limitedUntil) {
		fmt.Fprintf(&b, " · rate limited by %s, %ds", s.limitedHost, int(s.limitedUntil.Sub(now).Round(time.Second)/time.Second))
	}
	s.mu.Unlock()
	if s.inFlight != nil {
		if pools := s.inFlight(); len(pools) > 0 {
			b.WriteString(" · ")
			for i, p := range pools[:min(len(pools), 3)] {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(p.Name + " " + strconv.Itoa(p.N))
			}
			b.WriteString(" in flight")
		}
	}
	if n := s.errs.Load(); n > 0 {
		fmt.Fprintf(&b, " · %d error", n)
		if n > 1 {
			b.WriteByte('s')
		}
	}
	fmt.Fprintf(&b, " · %.1fs", elapsed.Seconds())
	frame := spinner[int(max(elapsed, 0)/statusTick)%len(spinner)]
	text := truncate(b.String(), s.width()-3) // spinner, space and one free column
	return s.style.Paint(logx.Cyan, frame) + " " + s.style.Paint(logx.Dim, text)
}

// truncate cuts s to n runes, ending in "…" when cut.
func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
