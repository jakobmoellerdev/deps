package logx

import (
	"bytes"
	"io"
	"sync"
)

// Overlay is output kept below everything else a Writer writes, such as a live status line
// on a terminal. The Writer calls Clear before and Draw after each write, under its lock; so
// a log record or result line never lands inside the overlay.
type Overlay interface {
	Clear(w io.Writer)
	Draw(w io.Writer)
}

// Updater is an Overlay that can redraw itself over its previous drawing in place, without
// erasing it first; Redraw then uses Update instead of Clear and Draw.
type Updater interface {
	Update(w io.Writer)
}

// Synchronized output (DEC private mode 2026): a terminal that supports it renders what
// comes between begin and end as one frame, so a redraw never shows half drawn; others
// ignore the unknown mode.
const (
	syncBegin = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
)

// Writer is the shared stderr of a command: log records and result lines go through it. Each
// Write reaches the underlying writer whole, under one lock, so concurrent records do not
// interleave and an Overlay can clear and redraw around them. With an overlay, each write
// together with the overlay's clear and redraw is one write to the underlying writer, framed
// as one synchronized update, so the terminal never shows the overlay erased.
type Writer struct {
	mu      sync.Mutex
	w       io.Writer
	overlay Overlay
	buf     bytes.Buffer // one overlay operation, written at once
}

// NewWriter returns a Writer writing to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overlay == nil {
		return w.w.Write(p)
	}
	w.buf.WriteString(syncBegin)
	w.overlay.Clear(&w.buf)
	w.buf.Write(p)
	w.overlay.Draw(&w.buf)
	if err := w.flush(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// flush ends the synchronized update in buf and writes it.
func (w *Writer) flush() error {
	w.buf.WriteString(syncEnd)
	_, err := w.w.Write(w.buf.Bytes())
	w.buf.Reset()
	return err
}

// SetOverlay replaces the overlay (nil removes it): the old one is cleared, the new one drawn.
func (w *Writer) SetOverlay(o Overlay) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overlay == nil && o == nil {
		return
	}
	w.buf.WriteString(syncBegin)
	if w.overlay != nil {
		w.overlay.Clear(&w.buf)
	}
	w.overlay = o
	if o != nil {
		o.Draw(&w.buf)
	}
	_ = w.flush()
}

// Redraw redraws the overlay, for when its content changed between writes: in place with
// Update when it is an Updater, else with Clear and Draw.
func (w *Writer) Redraw() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overlay == nil {
		return
	}
	w.buf.WriteString(syncBegin)
	if u, ok := w.overlay.(Updater); ok {
		u.Update(&w.buf)
	} else {
		w.overlay.Clear(&w.buf)
		w.overlay.Draw(&w.buf)
	}
	_ = w.flush()
}
