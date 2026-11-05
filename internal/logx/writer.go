package logx

import (
	"io"
	"sync"
)

// Overlay is output kept below everything else a Writer writes, such as a live status line.
// The Writer calls Clear before and Draw after each write, under its lock, with the
// underlying writer; so a log record or result line never lands inside the overlay.
type Overlay interface {
	Clear(w io.Writer)
	Draw(w io.Writer)
}

// Writer is the shared stderr of a command: log records and result lines go through it. Each
// Write reaches the underlying writer whole, under one lock, so concurrent records do not
// interleave and an Overlay can clear and redraw around them.
type Writer struct {
	mu      sync.Mutex
	w       io.Writer
	overlay Overlay
}

// NewWriter returns a Writer writing to w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overlay == nil {
		return w.w.Write(p)
	}
	w.overlay.Clear(w.w)
	n, err := w.w.Write(p)
	w.overlay.Draw(w.w)
	return n, err
}

// SetOverlay replaces the overlay (nil removes it): the old one is cleared, the new one drawn.
func (w *Writer) SetOverlay(o Overlay) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overlay != nil {
		w.overlay.Clear(w.w)
	}
	w.overlay = o
	if o != nil {
		o.Draw(w.w)
	}
}

// Redraw clears and draws the overlay, for when its content changed between writes.
func (w *Writer) Redraw() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.overlay != nil {
		w.overlay.Clear(w.w)
		w.overlay.Draw(w.w)
	}
}
