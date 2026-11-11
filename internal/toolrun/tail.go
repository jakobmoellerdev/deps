package toolrun

import "bytes"

// TailBuffer is an io.Writer that keeps only the last Max bytes written to it, so a chatty
// tool cannot grow memory while its output is kept just for an error message. Up to 2*Max
// bytes are buffered: dropping what ran over the cap on every write would move the whole
// buffer per write, so it is compacted once per Max bytes written instead.
type TailBuffer struct {
	Max int
	b   []byte
}

func (t *TailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= t.Max {
		t.b = append(t.b[:0], p[n-t.Max:]...)
		return n, nil
	}
	t.b = append(t.b, p...)
	if len(t.b) > 2*t.Max {
		t.b = append(t.b[:0], t.b[len(t.b)-t.Max:]...)
	}
	return n, nil
}

// Bytes returns the kept bytes; they alias the buffer until the next Write.
func (t *TailBuffer) Bytes() []byte {
	if len(t.b) > t.Max {
		return t.b[len(t.b)-t.Max:]
	}
	return t.b
}

// Tail returns the last n lines of b, trimmed.
func Tail(b []byte, n int) string {
	if n <= 0 {
		return ""
	}
	b = bytes.TrimSpace(b)
	i := len(b)
	for ; n > 0; n-- {
		j := bytes.LastIndexByte(b[:i], '\n')
		if j < 0 {
			return string(b)
		}
		i = j
	}
	return string(b[i+1:]) // skip the newline
}
