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
func testStatusLine(buf *bytes.Buffer, width int, pools ...httpx.PoolUse) (*statusLine, *logx.Writer, *time.Time) {
	w := logx.NewWriter(buf)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	s := &statusLine{w: w, inFlight: func() []httpx.PoolUse { return pools }, width: func() int { return width }, now: func() time.Time { return now }}
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
	s := newStatusLine(w, &buf, logx.NewStyle(false), httpx.NewLimits(1))
	s.tick = time.Millisecond
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

// Piped stderr, or a terminal with TERM=dumb, gets no status line.
func TestProgressOnlyOnTerminals(t *testing.T) {
	w := logx.NewWriter(&bytes.Buffer{})
	o := &rootOpts{stderr: w}
	if o.progress(httpx.NewLimits(1)) != nil {
		t.Error("status line on a pipe")
	}
	o.term.tty = true
	t.Setenv("TERM", "dumb")
	if o.progress(httpx.NewLimits(1)) != nil {
		t.Error("status line with TERM=dumb")
	}
	t.Setenv("TERM", "xterm")
	if o.progress(httpx.NewLimits(1)) == nil {
		t.Error("no status line on a terminal")
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
