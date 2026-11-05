package logx

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
)

// Buffer holds the log records of one unit of concurrent work, such as one PR, so they come
// out as one block (Flush) instead of interleaved with the records of the other units. Records
// reach it through a BufferHandler when they are logged with a context carrying it
// (WithBuffer). It is safe for concurrent use.
type Buffer struct {
	mu      sync.Mutex
	entries []bufferEntry
	flushed bool
}

type bufferEntry struct {
	h *BufferHandler // the handler (with its attributes and groups) that got the record
	r slog.Record
}

type bufferKey struct{}

// WithBuffer returns ctx carrying b: records logged with it, or a context derived from it,
// are held in b until b.Flush.
func WithBuffer(ctx context.Context, b *Buffer) context.Context {
	return context.WithValue(ctx, bufferKey{}, b)
}

// hold adds r to b unless b was flushed already, reporting whether it did.
func (b *Buffer) hold(h *BufferHandler, r slog.Record) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.flushed {
		return false
	}
	b.entries = append(b.entries, bufferEntry{h, r.Clone()})
	return true
}

// Flush renders the held records, in the order they were logged, with the handlers they
// were logged to and writes them to w in one Write, between header and footer; nothing at all
// when no record was held. Records logged after Flush pass through unbuffered. Flushing
// twice writes nothing the second time.
func (b *Buffer) Flush(w io.Writer, header, footer string) error {
	b.mu.Lock()
	entries := b.entries
	b.entries, b.flushed = nil, true
	b.mu.Unlock()
	if len(entries) == 0 {
		return nil
	}
	var out bytes.Buffer
	out.WriteString(header)
	chains := map[*BufferHandler]slog.Handler{}
	for _, e := range entries {
		h, ok := chains[e.h]
		if !ok {
			h = e.h.chain(&out)
			chains[e.h] = h
		}
		if err := h.Handle(context.Background(), e.r); err != nil {
			return err
		}
	}
	out.WriteString(footer)
	_, err := w.Write(out.Bytes())
	return err
}

// BufferHandler passes records to the next handler, except records whose context carries
// an unflushed Buffer: those are held in the Buffer, which renders them on Flush with a copy of
// the next handler (built by build) writing to the block.
type BufferHandler struct {
	next  slog.Handler
	build func(w io.Writer) slog.Handler // a handler like the root next, writing to w
	ops   []bufferOp                     // WithAttrs and WithGroup calls since the root
}

// bufferOp is a WithAttrs (attrs) or WithGroup (group) call.
type bufferOp struct {
	attrs []slog.Attr
	group string
}

// NewBufferHandler returns a BufferHandler in front of next; build returns a handler
// formatting like next that writes to the given writer.
func NewBufferHandler(next slog.Handler, build func(w io.Writer) slog.Handler) *BufferHandler {
	return &BufferHandler{next: next, build: build}
}

func (h *BufferHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *BufferHandler) Handle(ctx context.Context, r slog.Record) error {
	if b, ok := ctx.Value(bufferKey{}).(*Buffer); ok && b.hold(h, r) {
		return nil
	}
	return h.next.Handle(ctx, r)
}

func (h *BufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &BufferHandler{next: h.next.WithAttrs(attrs), build: h.build, ops: append(slices.Clip(h.ops), bufferOp{attrs: attrs})}
}

func (h *BufferHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &BufferHandler{next: h.next.WithGroup(name), build: h.build, ops: append(slices.Clip(h.ops), bufferOp{group: name})}
}

// chain builds the handler of h writing to w: the root handler with h's attributes and
// groups.
func (h *BufferHandler) chain(w io.Writer) slog.Handler {
	c := h.build(w)
	for _, op := range h.ops {
		if op.group != "" {
			c = c.WithGroup(op.group)
		} else {
			c = c.WithAttrs(op.attrs)
		}
	}
	return c
}
