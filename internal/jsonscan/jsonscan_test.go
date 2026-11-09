package jsonscan

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// skipAll skips every top-level value of in.
func skipAll(r io.Reader) error {
	s := New(r)
	for {
		if _, err := s.Peek(); err != nil {
			if err == io.EOF { //nolint:errorlint // Peek returns io.EOF itself
				return nil
			}
			return err
		}
		if err := s.Skip(); err != nil {
			return err
		}
	}
}

// validAll reports whether jsontext reads every top-level value of in.
func validAll(in []byte) error {
	dec := jsontext.NewDecoder(bytes.NewReader(in), jsontext.AllowDuplicateNames(true))
	for {
		if _, err := dec.ReadValue(); err != nil {
			if err == io.EOF { //nolint:errorlint // ReadValue returns io.EOF itself
				return nil
			}
			return err
		}
	}
}

// checkSkip checks that Skip accepts exactly what jsontext accepts, read whole and byte by byte.
func checkSkip(t *testing.T, in []byte) {
	t.Helper()
	want := validAll(in)
	for _, r := range []io.Reader{bytes.NewReader(in), iotest.OneByteReader(bytes.NewReader(in))} {
		err := skipAll(r)
		if (err == nil) != (want == nil) {
			t.Fatalf("Skip(%.200q) = %v; jsontext %v", in, err, want)
		}
		if err != nil && !errors.As(err, new(*SyntaxError)) {
			t.Fatalf("Skip(%.200q) = %v (%T), want a *SyntaxError", in, err, err)
		}
	}
}

var skipInputs = []string{
	`{"a":[1,-2.5e+10,0,true,false,null,{},[],"x"],"b":{"c":{"d":"é\u00e9\ud83d\ude00\n\"\\\/\b\f\r\t"}}}`,
	` [ 1 , 2 ] {} "x" 1 2.0 -0 true`,
	"\t\r\n{ \"a\" : 1 }\n",
	strings.Repeat(`{"key":"`+strings.Repeat("v", 37)+`",`, 3) + `"z":1}}}`,
	`{"a":1,}`, `{"a" 1}`, `{"a":1 "b":2}`, `{1:2}`, `{"a"}`, `[1,]`, `[,1]`, `[1 2]`, `]`, `}`, `,`, `:`,
	`01`, `-`, `1.`, `1e`, `1e+`, `.5`, `+1`, `1x`, `tru`, `nul`, `fals`, `trUe`, `x`,
	`"\x"`, `"\u12"`, `"\u12g4"`, `"\ud800"`, `"\ud800\u0041"`, `"\udc00\udc00"`, `"\ud800\udc00"`, `"\uD83D\uDE00"`,
	"\"\x01\"", "\"\x7f\"", "\"\xff\"", "\"\xed\xa0\x80\"", "\"\xc3\"", "\"\xe6\x97\xa5\"", "\"\xef\xbf\xbd\"",
	`"abc`, `{"a":`, `[`, `{`, `{"a":"b`, `[1`, `"abcdefghijklmnop\`, `"\u`,
	strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth),
	strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1),
	strings.Repeat(`{"":`, MaxDepth+1) + `0` + strings.Repeat("}", MaxDepth+1),
	``, ` `,
}

func TestSkip(t *testing.T) {
	for _, in := range skipInputs {
		checkSkip(t, []byte(in))
	}
	// Every byte value inside strings, as the SWAR scan sees it at every offset of a word.
	for c := range 256 {
		for pad := range 9 {
			checkSkip(t, []byte(`["`+strings.Repeat("a", pad)+string(rune(c))[:1]+`bcdefghij"]`))
			checkSkip(t, append([]byte(`["`+strings.Repeat("a", pad)), byte(c), 'b', '"', ']'))
		}
	}
}

func FuzzSkip(f *testing.F) {
	for _, in := range skipInputs {
		f.Add([]byte(in))
	}
	f.Fuzz(checkSkip)
}

func TestWalk(t *testing.T) {
	const doc = ` {"name" : "x", "es\u0063" : "a\nb", "n": null, "list": [true, false, {"deep": [1]}], "skip": {"a": [1, "b"]}, "last": "y"} 7`
	s := New(iotest.OneByteReader(strings.NewReader(doc)))
	if err := s.Open('{'); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for {
		more, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		name := s.Name()
		switch {
		case s.NameIs("name"), name == "last":
			v, err := s.StrBytes()
			if err != nil {
				t.Fatal(err)
			}
			got[name] = string(v)
		case s.NameIs("esc"):
			if got[name], err = s.Str(); err != nil {
				t.Fatal(err)
			}
		case name == "n":
			if err := s.Null(); err != nil {
				t.Fatal(err)
			}
			got[name] = nil
		case name == "list":
			if err := s.Open('['); err != nil {
				t.Fatal(err)
			}
			var bools []bool
			for {
				more, err := s.Next()
				if err != nil {
					t.Fatal(err)
				}
				if !more {
					break
				}
				if c, _ := s.Peek(); c == '{' {
					if err := s.Skip(); err != nil {
						t.Fatal(err)
					}
					continue
				}
				b, err := s.Bool()
				if err != nil {
					t.Fatal(err)
				}
				bools = append(bools, b)
			}
			got[name] = bools
		default:
			s.Mark()
			if err := s.Skip(); err != nil {
				t.Fatal(err)
			}
			got[name] = strings.TrimSpace(string(s.Marked()))
			s.Unmark()
		}
	}
	want := map[string]any{"name": "x", "esc": "a\nb", "n": nil, "list": []bool{true, false}, "skip": `{"a": [1, "b"]}`, "last": "y"}
	if !equalMaps(got, want) {
		t.Fatalf("walked %v, want %v", got, want)
	}
	if err := s.Skip(); err != nil {
		t.Fatal(err)
	}
	if s.Offset() != int64(len(doc)) {
		t.Fatalf("Offset() = %d, want %d", s.Offset(), len(doc))
	}
	if _, err := s.Peek(); err != io.EOF { //nolint:errorlint // io.EOF itself
		t.Fatalf("Peek() at the end = %v, want io.EOF", err)
	}
}

func equalMaps(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !strings.EqualFold(strings.TrimSpace(sprint(v)), strings.TrimSpace(sprint(w))) {
			return false
		}
	}
	return true
}

func sprint(v any) string {
	switch v := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return "s:" + v
	case []bool:
		var b strings.Builder
		for _, x := range v {
			if x {
				b.WriteString("t")
			} else {
				b.WriteString("f")
			}
		}
		return "b:" + b.String()
	}
	return "?"
}

func TestFind(t *testing.T) {
	const doc = `{"a":{"deprecated":1},"dep\u0072ecated":"x","b":[{"deprecated":2}],"deprecated":true}`
	for _, r := range []io.Reader{strings.NewReader(doc), iotest.OneByteReader(strings.NewReader(doc))} {
		s := New(r)
		if err := s.Open('{'); err != nil {
			t.Fatal(err)
		}
		var got []string
		for {
			found, err := s.Find("deprecated")
			if err != nil {
				t.Fatal(err)
			}
			if !found {
				break
			}
			if !s.NameIs("deprecated") || s.Name() != "deprecated" {
				t.Fatalf("Name() = %q after Find", s.Name())
			}
			c, _ := s.Peek()
			if c == '"' {
				v, _ := s.Str()
				got = append(got, v)
			} else {
				v, _ := s.Bool()
				got = append(got, map[bool]string{true: "true", false: "false"}[v])
			}
		}
		if strings.Join(got, ",") != "x,true" {
			t.Fatalf("found %q", got)
		}
		if _, err := s.Peek(); err != io.EOF { //nolint:errorlint // io.EOF itself
			t.Fatalf("Peek() after the object = %v", err)
		}
	}
	// Find after Next, in an empty object, and on errors.
	s := New(strings.NewReader(`{"a":1,"b":2}`))
	_ = s.Open('{')
	if more, _ := s.Next(); !more || s.Name() != "a" {
		t.Fatal("Next() did not move to a")
	}
	_ = s.Skip()
	if found, err := s.Find("b"); !found || err != nil {
		t.Fatalf("Find(b) = %v, %v", found, err)
	}
	s = New(strings.NewReader(`{}`))
	_ = s.Open('{')
	if found, err := s.Find("b"); found || err != nil {
		t.Fatalf("Find(b) in {} = %v, %v", found, err)
	}
	for _, in := range []string{`{"a" 1}`, `{"a":1 "b"}`, `{"a":[}`, `{1}`, `{"b"`, `{"a":1`} {
		s := New(strings.NewReader(in))
		_ = s.Open('{')
		if _, err := s.Find("b"); err == nil {
			t.Fatalf("Find(b) in %s succeeded", in)
		}
	}
}

func TestErrors(t *testing.T) {
	broken := errors.New("connection reset")
	for _, tc := range []struct {
		in   string
		read func(*Scanner) error
		want string
	}{
		{`[1]`, func(s *Scanner) error { return s.Open('{') }, `jsonscan: invalid character '[' (expecting '{') at offset 0`},
		{``, func(s *Scanner) error { return s.Open('{') }, `jsonscan: unexpected EOF at offset 0`},
		{`{"a":1 x`, func(s *Scanner) error { _ = s.Open('{'); _, _ = s.Next(); _ = s.Skip(); _, err := s.Next(); return err }, `jsonscan: invalid character 'x' after value (expecting ',' or '}') at offset 7`},
		{`{"a" 1}`, func(s *Scanner) error { _ = s.Open('{'); _, err := s.Next(); return err }, `jsonscan: invalid character '1' after name (expecting ':') at offset 5`},
		{`{1:2}`, func(s *Scanner) error { _ = s.Open('{'); _, err := s.Next(); return err }, `jsonscan: invalid character '1' at start of name (expecting '"') at offset 1`},
		{`{"a`, func(s *Scanner) error { _ = s.Open('{'); _, err := s.Next(); return err }, `jsonscan: unexpected EOF at offset 3`},
		{`{`, func(s *Scanner) error { _ = s.Open('{'); _, err := s.Next(); return err }, `jsonscan: unexpected EOF at offset 1`},
		{`1`, func(s *Scanner) error { _, err := s.Str(); return err }, `jsonscan: invalid character '1' (expecting a string) at offset 0`},
		{`1`, func(s *Scanner) error { _, err := s.Bool(); return err }, `jsonscan: invalid character '1' (expecting a boolean) at offset 0`},
		{`1`, func(s *Scanner) error { return s.Null() }, `jsonscan: invalid character '1' (expecting null) at offset 0`},
		{``, func(s *Scanner) error { _, err := s.Str(); return err }, `jsonscan: unexpected EOF at offset 0`},
		{``, func(s *Scanner) error { _, err := s.Bool(); return err }, `jsonscan: unexpected EOF at offset 0`},
		{``, func(s *Scanner) error { return s.Null() }, `jsonscan: unexpected EOF at offset 0`},
		{`nul`, func(s *Scanner) error { return s.Null() }, `jsonscan: unexpected EOF at offset 0`},
		{`nux`, func(s *Scanner) error { return s.Null() }, `jsonscan: invalid character 'x' in literal null at offset 2`},
		{`"\q"`, func(s *Scanner) error { return s.Skip() }, `jsonscan: invalid character 'q' in string escape at offset 2`},
		{`"\u00zz"`, func(s *Scanner) error { return s.Skip() }, `jsonscan: invalid escape sequence at offset 1`},
		{`"\ud800x"`, func(s *Scanner) error { return s.Skip() }, `jsonscan: unexpected EOF at offset 7`},
		{`"\ud800\u0041"`, func(s *Scanner) error { return s.Skip() }, `jsonscan: invalid surrogate pair at offset 1`},
		{"\"\x01\"", func(s *Scanner) error { return s.Skip() }, `jsonscan: invalid character '\x01' in string (expecting non-control character) at offset 1`},
		{"\"\xff\"", func(s *Scanner) error { return s.Skip() }, `jsonscan: invalid UTF-8 in string at offset 1`},
		{`-x`, func(s *Scanner) error { return s.Skip() }, `jsonscan: invalid character 'x' at start of value at offset 1`},
		{`1.x`, func(s *Scanner) error { return s.Skip() }, `jsonscan: invalid character 'x' in number (expecting digit) at offset 2`},
		{strings.Repeat("[", MaxDepth+1), func(s *Scanner) error { return s.Skip() }, `jsonscan: exceeded max depth at offset 10000`},
		{strings.Repeat("[", MaxDepth+1), func(s *Scanner) error {
			for range MaxDepth + 1 {
				if err := s.Open('['); err != nil {
					return err
				}
			}
			return nil
		}, `jsonscan: exceeded max depth at offset 10000`},
	} {
		if err := tc.read(New(strings.NewReader(tc.in))); err == nil || err.Error() != tc.want {
			t.Errorf("%.40q: %v, want %s", tc.in, err, tc.want)
		}
	}
	// Truncation wraps io.ErrUnexpectedEOF; read errors are returned as they are.
	if err := New(strings.NewReader(`[1`)).Skip(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated: %v", err)
	}
	r := io.MultiReader(strings.NewReader(`["abc`), iotest.ErrReader(broken))
	if err := New(r).Skip(); !errors.Is(err, broken) || err.Error() != broken.Error() {
		t.Errorf("read error: %v", err)
	}
}

func TestMarkAcrossRefills(t *testing.T) {
	value := `{"a":"` + strings.Repeat("x", 3*bufferSize) + `"}`
	in := `[1,` + value + `,2]`
	s := New(iotest.HalfReader(strings.NewReader(in)))
	_ = s.Open('[')
	_, _ = s.Next()
	_ = s.Skip()
	_, _ = s.Next()
	s.Mark()
	if err := s.Skip(); err != nil {
		t.Fatal(err)
	}
	if string(s.Marked()) != value {
		t.Fatalf("Marked() = %d bytes, want the %d of the value", len(s.Marked()), len(value))
	}
	if b := string(s.Buffered()); len(b) > 3 || b != ",2]"[:len(b)] { // a prefix: the rest is unread
		t.Fatalf("Buffered() = %q", b)
	}
}

func BenchmarkSkip(b *testing.B) {
	var doc strings.Builder
	doc.WriteString(`{"versions":{`)
	for i := range 2000 {
		if i > 0 {
			doc.WriteByte(',')
		}
		doc.WriteString(`"1.0.` + strings.Repeat("1", i%5+1) + `":{"name":"pkg","dependencies":{"left-pad":"^1.3.0","@scope/thing":"~2.0.1"},` +
			`"dist":{"integrity":"sha512-6c1mN2YIWbCWJSk7hONgiuMhOS1tyBJdg6tdfN04kH2IcIyrVwUJeVzbYQRiItCYXXy7ry0u7wlgmBLBu7+1pw==","fileCount":12,"unpackedSize":34567},"_hasShrinkwrap":false}`)
	}
	doc.WriteString(`}}`)
	data := []byte(doc.String())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if err := New(bytes.NewReader(data)).Skip(); err != nil {
			b.Fatal(err)
		}
	}
}
