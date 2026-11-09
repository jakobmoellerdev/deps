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
// collapsible ::group:: (discover and extract share one; PRs are groups of their own) that
// ends with the phase's final counts, and off a terminal a heartbeat line every heartbeat shows that a long phase still
// moves. Only the phase goroutine calls Start and End; Done and RateLimited may come from
// any goroutine.
type progress struct {
	w         *logx.Writer
	style     logx.Style
	inFlight  func() []httpx.HostUse // nil = not shown
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

	drawn int // lines of the status block on screen; under the Writer's lock (Clear, Draw)
}

// newProgress returns a progress writing to stderr w (out is the file behind it) with every
// output off; the caller turns on the status line and the Actions groups and heartbeat.
func newProgress(w *logx.Writer, out io.Writer, style logx.Style, hosts *httpx.HostCounter) *progress {
	return &progress{w: w, style: style, inFlight: hosts.InFlight, width: termWidth(out), now: time.Now}
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
	// The PR phase has no group of its own: each PR's records are a group (prLog), and
	// workflow command groups do not nest.
	if p.groups != nil && ph != engine.PhasePRs && (ph != engine.PhaseExtract || !p.grouped) {
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

// groupTitle is the log group title of the discover, extract or resolve phase.
func groupTitle(ph engine.Phase, total int) string {
	if ph == engine.PhaseResolve {
		return "Resolving " + plural(total, "dependency", "dependencies")
	}
	return "Discovering and extracting dependency files"
}

// Clear erases the status block; the cursor is at the start of its first line afterwards.
func (p *progress) Clear(w io.Writer) {
	_, _ = io.WriteString(w, "\r\x1b[2K"+strings.Repeat("\x1b[1A\x1b[2K", max(p.drawn-1, 0)))
	p.drawn = 0
}

// Draw writes the status block without a trailing newline, every line cut to the terminal
// width so none wraps (Clear erases the lines it drew).
func (p *progress) Draw(w io.Writer) {
	lines := p.render()
	p.drawn = len(lines)
	_, _ = io.WriteString(w, strings.Join(lines, "\n"))
}

// Status block limits: bar widths and the host lines below the phase line.
const (
	phaseBar     = 24
	hostBar      = 16
	hostLines    = 5
	hostColumn   = 28 // widest host name shown; longer ones are cut
	minBarLength = 5  // a narrower bar is left out
)

// render returns the lines of the status block: the phase with a progress bar of its items,
//
//	⠹ resolving ━━━━━━━━━━━━━━──────────  212/308 · 1 error · 2.1s
//	  registry.npmjs.org  ━━━━━━━━━━━━────  412/476 · 64 open
//	  github.com          ━━━━━━━━━━━━━━━─   58/62 · 4 open
//	  + 2 more hosts · 3 open
//
// and one line per host with open requests (the busiest hostLines), its bar the share of
// the requests sent to it that are done.
func (p *progress) render() []string {
	width := p.width() - 1 // one free column
	p.mu.Lock()
	elapsed := p.now().Sub(p.start)
	total := p.total
	p.mu.Unlock()
	frame := spinner[int(max(elapsed, 0)/statusTick)%len(spinner)]
	label, rest := phaseLabels[p.phase], p.details()
	var head string
	if total > 0 {
		rest = fmt.Sprintf("%d/%d", p.done.Load(), total) + rest
		if n := min(phaseBar, width-4-utf8.RuneCountInString(label)-utf8.RuneCountInString(rest)); n >= minBarLength {
			head = p.style.Paint(logx.Dim, label) + " " + p.bar(int(p.done.Load()), total, n) + "  " + p.style.Paint(logx.Dim, rest)
		} else {
			rest = " " + rest
		}
	}
	if head == "" {
		head = p.style.Paint(logx.Dim, truncate(label+rest, width-2))
	}
	lines := []string{p.style.Paint(logx.Cyan, frame) + " " + head}
	if p.inFlight == nil {
		return lines
	}
	hosts := p.inFlight()
	shown := hosts[:min(len(hosts), hostLines)]
	col := 0
	for _, u := range shown {
		col = max(col, min(utf8.RuneCountInString(u.Host), hostColumn))
	}
	for _, u := range shown {
		name := truncate(u.Host, col)
		name += strings.Repeat(" ", col-utf8.RuneCountInString(name))
		count := fmt.Sprintf("%d/%d · %d open", u.Done, u.Done+u.Open, u.Open)
		if n := min(hostBar, width-6-col-utf8.RuneCountInString(count)); n >= minBarLength {
			lines = append(lines, "  "+p.style.Paint(logx.Dim, name)+"  "+p.bar(u.Done, u.Done+u.Open, n)+"  "+p.style.Paint(logx.Dim, count))
		} else {
			lines = append(lines, "  "+p.style.Paint(logx.Dim, truncate(u.Host+"  "+count, width-2)))
		}
	}
	if more := hosts[len(shown):]; len(more) > 0 {
		open := 0
		for _, u := range more {
			open += u.Open
		}
		lines = append(lines, "  "+p.style.Paint(logx.Dim, truncate(fmt.Sprintf("+ %s · %d open", plural(len(more), "more host", "more hosts"), open), width-2)))
	}
	return lines
}

// bar is a progress bar of n cells, done of total filled.
func (p *progress) bar(done, total, n int) string {
	filled := 0
	if total > 0 {
		filled = min(done, total) * n / total
	}
	return p.style.Paint(logx.Cyan, strings.Repeat("━", filled)) + p.style.Paint(logx.Dim, strings.Repeat("─", n-filled))
}

// details is the rate-limit wait, error count and elapsed time of the running phase, each
// after " · ".
func (p *progress) details() string {
	p.mu.Lock()
	now := p.now()
	elapsed := now.Sub(p.start)
	var b strings.Builder
	if p.limitedHost != "" && now.Before(p.limitedUntil) {
		fmt.Fprintf(&b, " · rate limited by %s, %ds", p.limitedHost, int(p.limitedUntil.Sub(now).Round(time.Second)/time.Second))
	}
	p.mu.Unlock()
	if n := p.errs.Load(); n > 0 {
		fmt.Fprintf(&b, " · %d error", n)
		if n > 1 {
			b.WriteByte('s')
		}
	}
	fmt.Fprintf(&b, " · %.1fs", elapsed.Seconds())
	return b.String()
}

// text describes the running phase on one line, for heartbeats and the final line of a
// phase group: `resolving 212/308 · 2.1s · 20 open requests: registry.npmjs.org 18,
// api.github.com 2`, the open requests (the three busiest hosts) only with inFlight.
func (p *progress) text(inFlight bool) string {
	var b strings.Builder
	b.WriteString(phaseLabels[p.phase])
	p.mu.Lock()
	total := p.total
	p.mu.Unlock()
	if total > 0 {
		fmt.Fprintf(&b, " %d/%d", p.done.Load(), total)
	}
	b.WriteString(p.details())
	if inFlight && p.inFlight != nil {
		if hosts := p.inFlight(); len(hosts) > 0 {
			total := 0
			for _, u := range hosts {
				total += u.Open
			}
			fmt.Fprintf(&b, " · %d open request", total)
			if total > 1 {
				b.WriteByte('s')
			}
			b.WriteString(": ")
			for i, u := range hosts[:min(len(hosts), 3)] {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(u.Host + " " + strconv.Itoa(u.Open))
			}
		}
	}
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
