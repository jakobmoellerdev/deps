package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
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
// live status block: the logx.Writer overlay, below every record, redrawn every tick until the
// phase ends and then removed. With the actions format (groups) every phase gets a
// collapsible ::group:: (discover and extract share one; PRs are groups of their own) that
// lists the files or deps of the phase (engine.Lister) and ends with the phase's final
// counts, and off a terminal a heartbeat line every heartbeat shows that a long phase still
// moves. Only the phase goroutine calls Start, End and the List methods; Done and
// RateLimited may come from any goroutine.
type progress struct {
	w         *logx.Writer
	style     logx.Style
	hosts     func() []httpx.HostUse // requests per host (httpx.HostCounter.Hosts); nil = not shown
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

	// The status block on screen; under the Writer's lock (Clear, Draw, Update), reset by
	// Start. Its layout only changes slowly, so lines do not jump between redraws.
	drawn        int        // lines
	slots        []hostSlot // the host lines, in the order the hosts appeared
	col, textCol int        // widest name and text so far; they never shrink
	moreUntil    time.Time  // the "+ n more hosts" line stays until then
}

// hostSlot is a host line of the status block.
type hostSlot struct {
	host string
	idle time.Time // since when the host has no open request; zero while it has
}

// newProgress returns a progress writing to stderr w (out is the file behind it) with every
// output off; the caller turns on the status line and the Actions groups and heartbeat.
func newProgress(w *logx.Writer, out io.Writer, style logx.Style, hosts *httpx.HostCounter) *progress {
	return &progress{w: w, style: style, hosts: hosts.Hosts, width: termWidth(out), now: time.Now}
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
	p.drawn, p.slots, p.col, p.textCol, p.moreUntil = 0, nil, 0, 0, time.Time{}
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

// ListFiles lists the discovered files in the discovery group, one line each with the
// managers matching it and its dep counts. Only with the actions format: the group is
// collapsed, so the listing costs no screen space.
func (p *progress) ListFiles(files []engine.FileReport) {
	if p.groups == nil || len(files) == 0 {
		return
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, f := range files {
		var outcome []string
		if f.Deps > 0 || f.Err == nil {
			outcome = append(outcome, plural(f.Deps, "dep", "deps"))
		}
		if f.Skipped > 0 {
			outcome = append(outcome, strconv.Itoa(f.Skipped)+" skipped")
		}
		if f.Err != nil {
			outcome = append(outcome, "error: "+shortError(f.Err))
		}
		if f.Deps == 0 && f.Err == nil {
			outcome[0] = "no deps"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", f.File, strings.Join(f.Managers, ", "), strings.Join(outcome, ", "))
	}
	_ = tw.Flush()
	_, _ = io.WriteString(p.w, b.String())
}

// ListDeps lists the looked-up deps in the resolve group, one line each with its updates, or
// up to date, skipped or failed. Only with the actions format, as ListFiles.
func (p *progress) ListDeps(deps []engine.DepReport) {
	if p.groups == nil || len(deps) == 0 {
		return
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, d := range deps {
		var outcome string
		switch {
		case d.Err != nil:
			outcome = "error: " + shortError(d.Err)
		case d.Skip != "":
			outcome = "skipped: " + d.Skip
		case len(d.Updates) > 0:
			ups := make([]string, len(d.Updates))
			for i, u := range d.Updates {
				from, to := u.FromTo()
				ups[i] = fmt.Sprintf("%s -> %s (%s)", from, to, updateType(u, plain))
			}
			outcome = strings.Join(ups, "; ")
		case d.Pending != nil:
			outcome = "up to date (" + d.Pending.Version + " held back: " + d.Pending.Reason + ")"
		default:
			outcome = "up to date"
		}
		fmt.Fprintf(tw, "  %s:%d\t%s\t%s\n", d.File, d.Line, d.Name, outcome)
	}
	_ = tw.Flush()
	_, _ = io.WriteString(p.w, b.String())
}

// shortError is the first line of err, at most maxErrorText runes, without the location a
// DepError adds (the listing line has it).
func shortError(err error) string {
	if de, ok := errors.AsType[*engine.DepError](err); ok {
		err = de.Err
	}
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return truncate(msg, maxErrorText)
}

// maxErrorText is the length of an error in a listing line.
const maxErrorText = 120

// groupTitle is the log group title of the discover, extract or resolve phase.
func groupTitle(ph engine.Phase, total int) string {
	if ph == engine.PhaseResolve {
		return "Resolving " + plural(total, "dependency", "dependencies")
	}
	return "Discovering and extracting dependency files"
}

// Clear erases the status block; the cursor is at the start of its first line afterwards.
func (p *progress) Clear(w io.Writer) {
	_, _ = io.WriteString(w, "\r"+cursorUp(p.drawn-1)+"\x1b[J")
	p.drawn = 0
}

// Draw writes the status block without a trailing newline, every line cut to the terminal
// width so none wraps (Clear erases the lines it drew).
func (p *progress) Draw(w io.Writer) {
	lines := p.render()
	p.drawn = len(lines)
	_, _ = io.WriteString(w, strings.Join(lines, "\n"))
}

// Update redraws the status block over the drawn one: every line is overwritten and its
// rest erased, then whatever is below the new block, so no line is ever blank in between.
func (p *progress) Update(w io.Writer) {
	lines := p.render()
	var b strings.Builder
	b.WriteString("\r" + cursorUp(p.drawn-1))
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l + "\x1b[K")
	}
	b.WriteString("\x1b[J")
	p.drawn = len(lines)
	_, _ = io.WriteString(w, b.String())
}

// cursorUp moves the cursor n lines up; "" for n < 1.
func cursorUp(n int) string {
	if n < 1 {
		return ""
	}
	return "\x1b[" + strconv.Itoa(n) + "A"
}

// Status block limits.
const (
	barWidth     = 20 // cells of every bar
	hostLines    = 5
	hostColumn   = 28                      // widest host name shown; longer ones are cut
	minBarLength = 5                       // narrower bars are left out, on every line
	hostLinger   = 1500 * time.Millisecond // a host line stays this long after its last request
)

// render returns the lines of the status block: the phase with a progress bar of its items
// and one line per host (layout), its bar the share of the requests sent to it that are
// done. All bars have the same width and start in the same column, which only moves right
// during a phase (a longer name), so a redraw only changes what changed:
//
//	⠹ resolving           ━━━━━━━━━━━━━───────  212/308 · 1 error · 2.1s
//	  registry.npmjs.org  ━━━━━━━━━━━━━━━━────  412/476 · 64 open
//	  github.com          ━━━━━━━━━━━━━━━━━━━━  62/62 · done
//	  + 2 more hosts · 3 open
func (p *progress) render() []string {
	width := p.width() - 1 // one free column
	p.mu.Lock()
	now := p.now()
	elapsed := now.Sub(p.start)
	total := p.total
	p.mu.Unlock()
	type row struct {
		prefix, name, text string
		sep                string // between name and text without bars
		done, total        int    // total 0: no bar
	}
	frame := spinner[int(max(elapsed, 0)/statusTick)%len(spinner)]
	head := row{prefix: p.style.Paint(logx.Cyan, frame) + " ", name: phaseLabels[p.phase], text: strings.TrimPrefix(p.details(), " · "), sep: " · "}
	if total > 0 {
		done := int(p.done.Load())
		head.done, head.total, head.text, head.sep = done, total, fmt.Sprintf("%d/%d · %s", done, total, head.text), " "
	}
	shown, more := p.layout(now)
	rows := make([]row, 1, 1+len(shown))
	rows[0] = head
	for _, u := range shown {
		text := fmt.Sprintf("%d/%d · %d open", u.Done, u.Done+u.Open, u.Open)
		if u.Open == 0 {
			text = fmt.Sprintf("%d/%d · done", u.Done, u.Done)
		}
		rows = append(rows, row{prefix: "  ", name: truncate(u.Host, hostColumn), text: text, sep: "  ", done: u.Done, total: u.Done + u.Open})
	}
	// The name column fits the widest name; the bar gets what the longest text leaves.
	for _, r := range rows {
		p.col = max(p.col, 2+utf8.RuneCountInString(r.name)) // the prefix is two columns
		p.textCol = max(p.textCol, utf8.RuneCountInString(r.text))
	}
	bar := min(barWidth, width-p.col-4-p.textCol)
	lines := make([]string, 0, len(rows)+1)
	for _, r := range rows {
		if bar < minBarLength || r.total == 0 && len(rows) == 1 {
			lines = append(lines, r.prefix+p.style.Paint(logx.Dim, truncate(r.name+r.sep+r.text, width-2)))
			continue
		}
		name := r.name + strings.Repeat(" ", p.col-2-utf8.RuneCountInString(r.name))
		b := strings.Repeat(" ", bar)
		if r.total > 0 {
			b = p.bar(r.done, r.total, bar)
		}
		lines = append(lines, r.prefix+p.style.Paint(logx.Dim, name)+"  "+b+"  "+p.style.Paint(logx.Dim, r.text))
	}
	if len(more) > 0 {
		p.moreUntil = now.Add(hostLinger)
		open := 0
		for _, u := range more {
			open += u.Open
		}
		lines = append(lines, "  "+p.style.Paint(logx.Dim, truncate(fmt.Sprintf("+ %s · %d open", plural(len(more), "more host", "more hosts"), open), width-2)))
	} else if now.Before(p.moreUntil) {
		lines = append(lines, "") // keeps the height while the other hosts pause
	}
	return lines
}

// layout returns the hosts of the host lines and the other hosts with open requests. A host
// keeps its line, in the order the hosts appeared, until it had no open request for
// hostLinger; a host with open requests takes a free line, busiest first. Lines thus neither
// reorder nor blink when a host's requests come in bursts.
func (p *progress) layout(now time.Time) (shown, more []httpx.HostUse) {
	if p.hosts == nil {
		return nil, nil
	}
	hosts := p.hosts() // open ones first, busiest first
	byHost := make(map[string]httpx.HostUse, len(hosts))
	for _, u := range hosts {
		byHost[u.Host] = u
	}
	kept := p.slots[:0]
	for _, s := range p.slots {
		switch {
		case byHost[s.host].Open > 0:
			s.idle = time.Time{}
		case s.idle.IsZero():
			s.idle = now
		case now.Sub(s.idle) >= hostLinger:
			continue
		}
		kept = append(kept, s)
	}
	p.slots = kept
	for _, u := range hosts {
		if u.Open == 0 || slices.ContainsFunc(p.slots, func(s hostSlot) bool { return s.host == u.Host }) {
			continue
		}
		if len(p.slots) < hostLines {
			p.slots = append(p.slots, hostSlot{host: u.Host})
		} else {
			more = append(more, u)
		}
	}
	shown = make([]httpx.HostUse, len(p.slots))
	for i, s := range p.slots {
		shown[i] = cmp.Or(byHost[s.host], httpx.HostUse{Host: s.host})
	}
	return shown, more
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
	if inFlight && p.hosts != nil {
		var open []httpx.HostUse
		total := 0
		for _, u := range p.hosts() {
			if u.Open > 0 {
				open = append(open, u)
				total += u.Open
			}
		}
		if len(open) > 0 {
			fmt.Fprintf(&b, " · %d open request", total)
			if total > 1 {
				b.WriteByte('s')
			}
			b.WriteString(": ")
			for i, u := range open[:min(len(open), 3)] {
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
