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

const (
	statusTick     = 100 * time.Millisecond // redraw interval of the status line
	heartbeatEvery = 30 * time.Second       // interval of the Actions heartbeat lines
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// phaseLabels name the phases on the status line and in heartbeats.
var phaseLabels = map[engine.Phase]string{
	engine.PhaseDiscover: "discovering files",
	engine.PhaseExtract:  "extracting",
	engine.PhaseResolve:  "resolving",
	engine.PhasePRs:      "PRs",
}

// progress is the engine.Progress of the command's stderr. On a terminal (line) it is the
// live status line: the logx.Writer overlay, below every record, redrawn every tick until the
// phase ends and then removed. With the actions format (groups) every phase gets a
// collapsible ::group:: (discover and extract share one) that ends with the phase's final
// counts, and off a terminal a heartbeat line every heartbeat shows that a long phase still
// moves. Only the phase goroutine calls Start and End; Done and RateLimited may come from
// any goroutine.
type progress struct {
	w         *logx.Writer
	style     logx.Style
	inFlight  func() []httpx.PoolUse // nil = not shown
	width     func() int             // terminal columns
	now       func() time.Time
	line      bool          // the status line is on
	tick      time.Duration // status line redraw interval; 0 = no ticker (tests redraw by hand)
	groups    *annotator    // non-nil: a log group per phase
	heartbeat time.Duration // 0 = no heartbeat lines

	done, errs atomic.Int64

	mu           sync.Mutex // guards the fields below; taken inside the Writer's lock by Draw
	phase        engine.Phase
	total        int
	start        time.Time
	limitedHost  string
	limitedUntil time.Time

	grouped bool // a phase group is open; phase goroutine only
	stop    chan struct{}
	stopped chan struct{}
}

// newProgress returns a progress writing to stderr w (out is the file behind it) with every
// output off; the caller turns on the status line and the Actions groups and heartbeat.
func newProgress(w *logx.Writer, out io.Writer, style logx.Style, limits httpx.Limits) *progress {
	return &progress{w: w, style: style, inFlight: limits.InFlight, width: termWidth(out), now: time.Now}
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

func (p *progress) Start(ph engine.Phase, total int) {
	p.mu.Lock()
	p.phase, p.total, p.start = ph, total, p.now()
	p.limitedHost, p.limitedUntil = "", time.Time{}
	p.mu.Unlock()
	p.done.Store(0)
	p.errs.Store(0)
	if p.groups != nil && (ph != engine.PhaseExtract || !p.grouped) {
		p.endGroup()
		p.groups.group(groupTitle(ph, total))
		p.grouped = true
	}
	if p.line {
		p.w.SetOverlay(p)
	}
	var redraw, beat *time.Ticker
	if p.line && p.tick > 0 {
		redraw = time.NewTicker(p.tick)
	}
	if p.heartbeat > 0 {
		beat = time.NewTicker(p.heartbeat)
	}
	if redraw != nil || beat != nil {
		p.stop, p.stopped = make(chan struct{}), make(chan struct{})
		go p.loop(redraw, beat, p.stop, p.stopped)
	}
}

// loop redraws the status line on redraw and writes a heartbeat line on beat (either may be
// nil) until stop is closed.
func (p *progress) loop(redraw, beat *time.Ticker, stop, stopped chan struct{}) {
	defer close(stopped)
	var redrawC, beatC <-chan time.Time
	if redraw != nil {
		defer redraw.Stop()
		redrawC = redraw.C
	}
	if beat != nil {
		defer beat.Stop()
		beatC = beat.C
	}
	for {
		select {
		case <-redrawC:
			p.w.Redraw()
		case <-beatC:
			_, _ = io.WriteString(p.w, "deps: "+p.text(true)+"\n")
		case <-stop:
			return
		}
	}
}

func (p *progress) Done(_ engine.Phase, err error) {
	p.done.Add(1)
	if err != nil {
		p.errs.Add(1)
	}
}

func (p *progress) End(ph engine.Phase) {
	if p.stop != nil {
		close(p.stop)
		<-p.stopped
		p.stop = nil
	}
	if p.line {
		p.w.SetOverlay(nil)
	}
	if p.groups != nil && ph != engine.PhaseDiscover {
		_, _ = io.WriteString(p.w, "deps: "+p.text(false)+"\n")
		p.endGroup()
	}
}

// endGroup ends the open phase group, if any: also the discover group of a walk that
// failed before extraction, when the command ends (session.close).
func (p *progress) endGroup() {
	if p.grouped {
		p.groups.endGroup()
		p.grouped = false
	}
}

func (p *progress) RateLimited(host string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until.After(p.limitedUntil) {
		p.limitedHost, p.limitedUntil = host, until
	}
}

// groupTitle is the log group title of a phase.
func groupTitle(ph engine.Phase, total int) string {
	switch ph {
	case engine.PhaseDiscover, engine.PhaseExtract:
		return "Discovering and extracting dependency files"
	case engine.PhaseResolve:
		return "Resolving " + plural(total, "dependency", "dependencies")
	}
	return "Building and pushing " + plural(total, "PR", "PRs")
}

// Clear erases the status line; the cursor is at its start afterwards.
func (p *progress) Clear(w io.Writer) { _, _ = io.WriteString(w, "\r\x1b[2K") }

// Draw writes the status line without a newline, cut to the terminal width so it never
// wraps (Clear only erases one line).
func (p *progress) Draw(w io.Writer) { _, _ = io.WriteString(w, p.render()) }

// render returns the status line: `⠹ resolving 212/308 · npm 18 in flight · 2.1s`.
func (p *progress) render() string {
	p.mu.Lock()
	elapsed := p.now().Sub(p.start)
	p.mu.Unlock()
	frame := spinner[int(max(elapsed, 0)/statusTick)%len(spinner)]
	text := truncate(p.text(true), p.width()-3) // spinner, space and one free column
	return p.style.Paint(logx.Cyan, frame) + " " + p.style.Paint(logx.Dim, text)
}

// text describes the running phase: `resolving 212/308 · npm 18 in flight · 2.1s`; the
// requests in flight only with inFlight (not in the final line of a phase).
func (p *progress) text(inFlight bool) string {
	p.mu.Lock()
	now := p.now()
	elapsed := now.Sub(p.start)
	var b strings.Builder
	b.WriteString(phaseLabels[p.phase])
	if p.total > 0 {
		fmt.Fprintf(&b, " %d/%d", p.done.Load(), p.total)
	}
	if p.limitedHost != "" && now.Before(p.limitedUntil) {
		fmt.Fprintf(&b, " · rate limited by %s, %ds", p.limitedHost, int(p.limitedUntil.Sub(now).Round(time.Second)/time.Second))
	}
	p.mu.Unlock()
	if inFlight && p.inFlight != nil {
		if pools := p.inFlight(); len(pools) > 0 {
			b.WriteString(" · ")
			for i, u := range pools[:min(len(pools), 3)] {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(u.Name + " " + strconv.Itoa(u.N))
			}
			b.WriteString(" in flight")
		}
	}
	if n := p.errs.Load(); n > 0 {
		fmt.Fprintf(&b, " · %d error", n)
		if n > 1 {
			b.WriteByte('s')
		}
	}
	fmt.Fprintf(&b, " · %.1fs", elapsed.Seconds())
	return b.String()
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
