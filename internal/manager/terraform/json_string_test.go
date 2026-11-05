package terraform

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// decodeStringDecoder is the former decodeString: a decoder per string value.
func decodeStringDecoder(v jsontext.Value) (string, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))
	tok, err := dec.ReadToken()
	if err != nil {
		return "", err
	}
	return tok.String(), nil
}

// checkDecodeString compares decodeString with the decoder for a validated string value.
func checkDecodeString(t *testing.T, v jsontext.Value) {
	t.Helper()
	want, err := decodeStringDecoder(v)
	if err != nil {
		t.Fatalf("%s: %v", v, err)
	}
	got, err := decodeString(v, string(v[1:len(v)-1]))
	if err != nil || got != want {
		t.Fatalf("decodeString(%s) = %q, %v; want %q", v, got, err, want)
	}
}

func TestDecodeStringFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/*/*.tf.json")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	extra := `["plain", "a\"b", "\\", "\u00e9\n\t", "\ud83d\ude00", "", "x\/y", "caf\u00e9", "é"]`
	srcs := [][]byte{[]byte(extra)}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, src)
	}
	n := 0
	for _, src := range srcs {
		dec := jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))
		for {
			kind := dec.PeekKind()
			if kind == 0 {
				break
			}
			if kind != '"' {
				if _, err := dec.ReadToken(); err != nil {
					t.Fatal(err)
				}
				continue
			}
			v, err := dec.ReadValue()
			if err != nil {
				t.Fatal(err)
			}
			checkDecodeString(t, v.Clone())
			n++
		}
		if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	}
	if n < 100 {
		t.Fatalf("only %d strings", n)
	}
}

func FuzzDecodeString(f *testing.F) {
	for _, s := range []string{`"plain"`, `"a\"b"`, `"\u00e9\n"`, `"\ud83d\ude00"`, `""`, `"\/"`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		v := jsontext.Value(b)
		if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' || !v.IsValid() {
			return
		}
		checkDecodeString(t, v)
	})
}
