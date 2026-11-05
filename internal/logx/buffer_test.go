package logx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// bufferedLogger returns a logger writing text records without time to out, behind a
// BufferHandler.
func bufferedLogger(out *Writer) *slog.Logger {
	o := &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}
	return slog.New(NewBufferHandler(slog.NewTextHandler(out, o), func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, o) }))
}

// Records of concurrent units come out contiguous per unit, each between its header and
// footer; records without a buffer pass through at once.
func TestBufferContiguous(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	log := bufferedLogger(w)
	log.Info("start")
	if got := out.String(); got != "level=INFO msg=start\n" {
		t.Fatalf("unbuffered record not written at once: %q", got)
	}
	const units, records = 8, 50
	var wg sync.WaitGroup
	for u := range units {
		wg.Go(func() {
			b := &Buffer{}
			ctx := WithBuffer(t.Context(), b)
			l := log.With("unit", u)
			for i := range records {
				l.InfoContext(ctx, "step", "i", i)
				log.Info("free") // unbuffered, interleaves freely
			}
			if err := b.Flush(w, fmt.Sprintf("<%d>\n", u), fmt.Sprintf("</%d>\n", u)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	free := 0
	for i := 0; i < len(lines); i++ {
		if lines[i] == "level=INFO msg=free" || lines[i] == "level=INFO msg=start" {
			free++
			continue
		}
		var u int
		if _, err := fmt.Sscanf(lines[i], "<%d>", &u); err != nil {
			t.Fatalf("line %d: %q is neither a header nor unbuffered", i, lines[i])
		}
		for j := range records {
			if want := fmt.Sprintf("level=INFO msg=step unit=%d i=%d", u, j); lines[i+1+j] != want {
				t.Fatalf("unit %d record %d = %q, want %q", u, j, lines[i+1+j], want)
			}
		}
		if want := fmt.Sprintf("</%d>", u); lines[i+1+records] != want {
			t.Fatalf("footer %q, want %q", lines[i+1+records], want)
		}
		i += records + 1
	}
	if free != 1+units*records {
		t.Errorf("%d unbuffered records, want %d", free, 1+units*records)
	}
}

// Groups and attributes survive the replay; after Flush the buffer passes records through,
// and an empty buffer writes nothing.
func TestBufferFlush(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	log := bufferedLogger(w)
	b := &Buffer{}
	ctx := WithBuffer(context.Background(), b)
	log.WithGroup("g").With("a", 1).InfoContext(ctx, "held", "b", 2)
	if out.Len() != 0 {
		t.Fatalf("held record written: %q", out.String())
	}
	if err := b.Flush(w, "[", "]\n"); err != nil {
		t.Fatal(err)
	}
	log.InfoContext(ctx, "after")
	if err := b.Flush(w, "[", "]\n"); err != nil {
		t.Fatal(err)
	}
	if err := (&Buffer{}).Flush(w, "[", "]\n"); err != nil {
		t.Fatal(err)
	}
	if want := "[level=INFO msg=held g.a=1 g.b=2\n]\nlevel=INFO msg=after\n"; out.String() != want {
		t.Errorf("got %q, want %q", out.String(), want)
	}
	h := NewBufferHandler(slog.DiscardHandler, nil)
	if h.WithAttrs(nil) != h || h.WithGroup("") != h || h.Enabled(ctx, slog.LevelInfo) {
		t.Error("no-op WithAttrs/WithGroup or Enabled")
	}
}
