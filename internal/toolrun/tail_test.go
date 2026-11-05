package toolrun

import "testing"

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
