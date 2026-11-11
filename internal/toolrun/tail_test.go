package toolrun

import (
	"bytes"
	"strings"
	"testing"
)

func TestTail(t *testing.T) {
	for _, tt := range []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc"},
		{"a\nb\nc", 5, "a\nb\nc"},
		{"a", 5, "a"},
		{"one", 1, "one"},
		{"\n\nonly\n\n", 1, "only"},
		{"a\r\nb\r\nc\r\n", 2, "b\r\nc"},
		{"a\nb", 0, ""},
		{"", 3, ""},
	} {
		if got := Tail([]byte(tt.in), tt.n); got != tt.want {
			t.Errorf("Tail(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestTailBuffer(t *testing.T) {
	b := &TailBuffer{Max: 8}
	for _, s := range []string{"abc", "defg", "hij"} {
		if n, err := b.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if string(b.Bytes()) != "cdefghij" {
		t.Errorf("after small writes = %q", b.Bytes())
	}
	if _, err := b.Write([]byte("0123456789")); err != nil || string(b.Bytes()) != "23456789" {
		t.Errorf("after a large write = %q", b.Bytes())
	}
}

// TestTailBufferKeepsOnlyMax covers the slack the buffer keeps between compactions: Bytes
// must still report the last Max bytes.
func TestTailBufferKeepsOnlyMax(t *testing.T) {
	b := &TailBuffer{Max: 8}
	var all strings.Builder
	for i := range 20 {
		s := strings.Repeat(string(rune('a'+i%26)), 3)
		all.WriteString(s)
		if _, err := b.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
		if len(b.b) > 2*b.Max {
			t.Fatalf("write %d: buffer grew to %d bytes", i, len(b.b))
		}
	}
	if want := all.String()[all.Len()-8:]; string(b.Bytes()) != want {
		t.Errorf("Bytes = %q, want %q", b.Bytes(), want)
	}
}

// BenchmarkTailBufferWrite writes a tool's chatty output in the small chunks a pipe
// delivers, with the buffer long past its cap.
func BenchmarkTailBufferWrite(b *testing.B) {
	p := bytes.Repeat([]byte("tool output line\n"), 5) // 85 bytes
	t := &TailBuffer{Max: 32 << 10}
	for range (32 << 10) / len(p) {
		_, _ = t.Write(p)
	}
	b.SetBytes(int64(len(p)))
	b.ReportAllocs()
	for b.Loop() {
		_, _ = t.Write(p)
	}
}
