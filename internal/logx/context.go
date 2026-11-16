package logx

import (
	"context"
	"log/slog"
	"slices"
)

type attrsKey struct{}

// WithAttrs returns ctx carrying attrs: every record logged with it, or with a context
// derived from it, gets them appended by a ContextHandler. It is how one unit of concurrent
// work labels its records (the repository and run of a `deps serve` cycle) without threading
// a logger through every call: deps logs with the default logger and the call's context
// everywhere. Attrs of a derived context follow those of its parent.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	if old, ok := ctx.Value(attrsKey{}).([]slog.Attr); ok {
		attrs = append(slices.Clip(old), attrs...)
	}
	return context.WithValue(ctx, attrsKey{}, attrs)
}

// ContextAttrs returns the attributes WithAttrs put in ctx, oldest first; nil when it carries
// none.
func ContextAttrs(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(attrsKey{}).([]slog.Attr)
	return attrs
}

// ContextHandler appends the attributes of a record's context (WithAttrs) to the record and
// passes it to Next. It is installed outermost, so the attributes are part of the record
// wherever it goes next, including a Buffer that renders it later.
type ContextHandler struct{ Next slog.Handler }

func (h ContextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.Next.Enabled(ctx, l)
}

func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs := ContextAttrs(ctx); len(attrs) > 0 {
		r = r.Clone()
		r.AddAttrs(attrs...)
	}
	return h.Next.Handle(ctx, r)
}

func (h ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ContextHandler{Next: h.Next.WithAttrs(attrs)}
}

func (h ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{Next: h.Next.WithGroup(name)}
}
