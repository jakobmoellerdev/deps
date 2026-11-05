package toolrun

import "bytes"

// TailBuffer is an io.Writer that keeps only the last Max bytes written to it, so a chatty
// tool cannot grow memory while its output is kept just for an error message.
type TailBuffer struct {
	Max int
	b   []byte
}

func (t *TailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.Max {
		t.b = append(t.b[:0], p[len(p)-t.Max:]...)
		return n, nil
	}
	if over := len(t.b) + len(p) - t.Max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	t.b = append(t.b, p...)
	return n, nil
}

// Bytes returns the kept bytes; they alias the buffer until the next Write.
func (t *TailBuffer) Bytes() []byte { return t.b }

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
