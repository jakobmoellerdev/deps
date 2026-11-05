package main

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/logx"
)

// testStatusLine returns a status line on buf without ticker, on a clock tests advance.
func testStatusLine(buf *bytes.Buffer, width int, pools ...httpx.PoolUse) (*progress, *logx.Writer, *time.Time) {
	w := logx.NewWriter(buf)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	s := &progress{w: w, line: true, inFlight: func() []httpx.PoolUse { return pools }, width: func() int { return width }, now: func() time.Time { return now }}
	return s, w, &now
}

const clearLine = "\r\x1b[2K"

// The line is drawn when the phase starts, cleared before and redrawn after every record,
// refreshed by Redraw and removed when the phase ends; afterwards writes pass through.
func TestStatusLineOverlay(t *testing.T) {
	var buf bytes.Buffer
	s, w, now := testStatusLine(&buf, 80, httpx.PoolUse{Name: "npm", N: 18})
	s.Start(engine.PhaseResolve, 308)
	line0 := "⠋ resolving 0/308 · npm 18 in flight · 0.0s"
	if got := buf.String(); got != line0 {
		t.Fatalf("start: %q", got)
	}
	_, _ = w.Write([]byte("record\n"))
	for range 212 {
		s.Done(engine.PhaseResolve, nil)
	}
	s.Done(engine.PhaseResolve, errors.New("boom"))
	*now = now.Add(2100 * time.Millisecond)
	w.Redraw()
	s.End(engine.PhaseResolve)
	_, _ = w.Write([]byte("after\n"))
	line1 := "⠙ resolving 213/308 · npm 18 in flight · 1 error · 2.1s"
	want := line0 + clearLine + "record\n" + line0 + clearLine + line1 + clearLine + "after\n"
	if got := buf.String(); got != want {
		t.Errorf("output:\n%q\nwant\n%q", got, want)
	}
}

// Done from many goroutines counts every item; rate-limit waits show until they end; the
// line never exceeds the terminal width.
func TestStatusLineCounting(t *testing.T) {
	var buf bytes.Buffer
	s, _, now := testStatusLine(&buf, 80)
	s.Start(engine.PhaseExtract, 100)
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			var err error
			if i%10 == 0 {
				err = errors.New("x")
			}
			s.Done(engine.PhaseExtract, err)
		})
	}
	wg.Wait()
	s.RateLimited("api.github.com", now.Add(41*time.Second))
	s.RateLimited("api.github.com", now.Add(5*time.Second)) // an earlier reset does not shorten it
	if got, want := s.render(), "⠋ extracting 100/100 · rate limited by api.github.com, 41s · 10 errors · 0.0s"; got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
	*now = now.Add(42 * time.Second)
	if got := s.render(); strings.Contains(got, "rate limited") {
		t.Errorf("rate limit shown after it ended: %q", got)
	}
	s.End(engine.PhaseExtract)

	s, _, _ = testStatusLine(&buf, 20, httpx.PoolUse{Name: "npm", N: 1}, httpx.PoolUse{Name: "docker", N: 1})
	s.Start(engine.PhaseDiscover, 0)
	if got, want := s.render(), "⠋ discovering file…"; got != want {
		t.Errorf("narrow: %q, want %q", got, want)
	}
	s.End(engine.PhaseDiscover)
	s.style = logx.NewStyle(true)
	if got, want := s.render(), "\x1b[36m⠋\x1b[0m \x1b[2mdiscovering file…\x1b[0m"; got != want {
		t.Errorf("colored: %q, want %q", got, want)
	}
}

// The ticker redraws until the phase ends.
func TestStatusLineTicker(t *testing.T) {
	var buf syncBuffer
	w := logx.NewWriter(&buf)
	s := newProgress(w, &buf, logx.NewStyle(false), httpx.NewLimits(1))
	s.line, s.tick = true, time.Millisecond
	s.Start(engine.PhaseResolve, 1)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(buf.String(), clearLine) < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.End(engine.PhaseResolve)
	n := strings.Count(buf.String(), clearLine)
	if n < 3 {
		t.Fatalf("%d redraws", n)
	}
	time.Sleep(5 * time.Millisecond)
	if got := strings.Count(buf.String(), clearLine); got != n {
		t.Errorf("redrawn after End: %d > %d", got, n)
	}
}

// Piped stderr, or a terminal with TERM=dumb, gets no status line; the actions format gets
// groups, and a heartbeat off a terminal.
func TestProgressSelection(t *testing.T) {
	actionsLog = &annotator{}
	t.Cleanup(func() { actionsLog = &annotator{} })
	w := logx.NewWriter(&bytes.Buffer{})
	o := &rootOpts{stderr: w}
	if o.progress(httpx.NewLimits(1)) != nil {
		t.Error("progress on a pipe")
	}
	o.term.tty = true
	t.Setenv("TERM", "dumb")
	if o.progress(httpx.NewLimits(1)) != nil {
		t.Error("status line with TERM=dumb")
	}
	t.Setenv("TERM", "xterm")
	if p, _ := o.progress(httpx.NewLimits(1)).(*progress); p == nil || !p.line || p.groups != nil || p.heartbeat != 0 {
		t.Errorf("terminal: %+v", p)
	}
	actionsLog = &annotator{on: true}
	if p, _ := o.progress(httpx.NewLimits(1)).(*progress); p == nil || !p.line || p.groups == nil || p.heartbeat != 0 {
		t.Errorf("actions on a terminal: %+v", p)
	}
	o.term.tty = false
	if p, _ := o.progress(httpx.NewLimits(1)).(*progress); p == nil || p.line || p.groups == nil || p.heartbeat != heartbeatEvery {
		t.Errorf("actions piped: %+v", p)
	}
}

// With the actions format each phase is a log group (discover and extract share one) that
// ends with the final counts; records stay inside.
func TestProgressActionsGroups(t *testing.T) {
	var buf syncBuffer
	w := logx.NewWriter(&buf)
	p := newProgress(w, &buf, logx.NewStyle(false), httpx.NewLimits(1))
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	p.groups = &annotator{on: true, w: w}
	p.Start(engine.PhaseDiscover, 0)
	p.End(engine.PhaseDiscover)
	p.Start(engine.PhaseExtract, 2)
	p.Done(engine.PhaseExtract, nil)
	p.Done(engine.PhaseExtract, nil)
	p.End(engine.PhaseExtract)
	p.Start(engine.PhaseResolve, 1)
	_, _ = w.Write([]byte("record\n"))
	p.Done(engine.PhaseResolve, errors.New("x"))
	p.End(engine.PhaseResolve)
	p.Start(engine.PhaseDiscover, 0) // a walk that fails: closed with the session
	p.End(engine.PhaseDiscover)
	p.endGroup()
	p.endGroup()
	want := "::group::Discovering and extracting dependency files\n" +
		"deps: extracting 2/2 · 0.0s\n" +
		"::endgroup::\n" +
		"::group::Resolving 1 dependency\n" +
		"record\n" +
		"deps: resolving 1/1 · 1 error · 0.0s\n" +
		"::endgroup::\n" +
		"::group::Discovering and extracting dependency files\n" +
		"::endgroup::\n"
	if g := buf.String(); g != want {
		t.Errorf("output:\n%s\nwant\n%s", g, want)
	}
}

// A heartbeat line is written while a phase runs, not after it ended.
func TestProgressHeartbeat(t *testing.T) {
	var buf syncBuffer
	w := logx.NewWriter(&buf)
	p := newProgress(w, &buf, logx.NewStyle(false), httpx.NewLimits(1))
	p.heartbeat = time.Millisecond
	p.Start(engine.PhaseResolve, 308)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "deps: resolving 0/308 · ") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p.End(engine.PhaseResolve)
	n := len(buf.String())
	if n == 0 {
		t.Fatal("no heartbeat")
	}
	time.Sleep(5 * time.Millisecond)
	if len(buf.String()) != n {
		t.Errorf("heartbeat after End: %q", buf.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
