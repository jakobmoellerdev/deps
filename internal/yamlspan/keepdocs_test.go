package yamlspan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

// keepMarked keeps the documents containing "keep".
func keepMarked(doc []byte) bool { return bytes.Contains(doc, []byte("keep")) }

func TestKeepDocuments(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"single kept", "a: keep\n", "a: keep\n"},
		{"single dropped", "a: 1\nb: 2\n", "\n\n"},
		{"first implicit dropped", "a: 1\n---\nb: keep\n", "\n---\nb: keep\n"},
		{"middle dropped, marker content cut", "a: keep\n--- # x\nb: 1\n---\nc: keep\n", "a: keep\n---\n\n---\nc: keep\n"},
		{"last dropped without newline", "a: keep\n---\nb: 1", "a: keep\n---\n"},
		{"marker at end", "a: keep\n---", "a: keep\n---"},
		{"end marker stays", "a: 1\n... # x\nb: 2\n---\nc: keep\n", "\n...\n\n---\nc: keep\n"},
		{"directive document kept", "a: 1\n...\n%YAML 1.2\n---\nb: keep\n", "a: 1\n...\n%YAML 1.2\n---\nb: keep\n"},
		{"empty", "", ""},
		{"crlf", "a: 1\r\n---\r\nb: keep\r\n", "\n---\r\nb: keep\r\n"},
		{"lone CR: nothing blanked", "a: 1\rb: 2\n---\nc: keep\n", "a: 1\rb: 2\n---\nc: keep\n"},
		{"LS break: nothing blanked", "a: 1\u2028b: 2\n", "a: 1\u2028b: 2\n"},
		{"UTF-16: nothing blanked", "\xff\xfea\x00:\x00 \x001\x00\n\x00", "\xff\xfea\x00:\x00 \x001\x00\n\x00"},
		{"document-level block scalar swallows later markers", "a: 1\n--- |\n  t\n--- x\n---\nb: 2\n", "\n---\n\n\n\n\n"},
		{"block scalar behind a tag, kept with the rest", "a: 1\n--- !!str >-\n--- x\n---\nb: keep\n", "\n--- !!str >-\n--- x\n---\nb: keep\n"},
		{"block scalar at column 0", "|\n--- keep\n", "|\n--- keep\n"},
		{"no split at ----, ---x or indented ---", "a: |\n  ---\n----\n---x\n--- keep\n", "\n\n\n\n--- keep\n"},
	} {
		if got := string(KeepDocuments([]byte(c.src), keepMarked)); got != c.want {
			t.Errorf("%s: KeepDocuments(%q) = %q, want %q", c.name, c.src, got, c.want)
		}
	}
	src := []byte("a: keep\n---\nb: keep\n")
	if got := KeepDocuments(src, keepMarked); &got[0] != &src[0] {
		t.Error("all documents kept: want src itself, got a copy")
	}
}

// FuzzKeepDocuments checks, for valid YAML, that KeepDocuments' output parses, keeps the line
// count, and yields exactly the nodes (value, kind, tag, anchor, line, column) the original
// has on the lines of kept documents; blanked documents contribute at most empty nulls.
func FuzzKeepDocuments(f *testing.F) {
	for _, s := range []string{
		"a: keep\n---\nb: [1, 2]\n---\nc: &x keep\nd: *x\n",
		"# c\nkind: A\n---\nx: {y: z}\n---\nimage: keep\n",
		"a: 1\n...\n---\nb: keep\n",
		"%YAML 1.2\n---\nkeep: |\n  multi\n  line\n---\nx: 'y'\n",
		"--- keep\n--- other\n",
		"a:\n  - keep\n  - {b: c}\r\n---\r\nz: \"q\"\r\n",
		"a: keep\n--- |\n  text\n---\n- ü: keep\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		orig, err := nodes([]byte(src))
		if err != nil {
			return // invalid input: the parser may split documents differently
		}
		out := KeepDocuments([]byte(src), keepMarked)
		if unsplittable([]byte(src)) {
			if string(out) != src {
				t.Fatalf("unsplittable %q: want src unchanged, got %q", src, out)
			}
			return
		}
		if yamlBreaks(string(out)) != yamlBreaks(src) {
			t.Fatalf("line count changed: %q -> %q", src, out)
		}
		got, err := nodes(out)
		if err != nil {
			t.Fatalf("output of valid %q does not parse: %v\n%q", src, err, out)
		}
		kept := keptLines(src)
		want := map[string]int{}
		for k, n := range orig {
			if kept[lineOf(k)] {
				want[k] = n
			}
		}
		// empty nulls are positioned at the next token, which may lie in another document
		for _, m := range []map[string]int{got, want} {
			maps.DeleteFunc(m, func(k string, _ int) bool { return strings.HasPrefix(k, "!!null|||") })
		}
		if !maps.Equal(got, want) {
			t.Fatalf("nodes differ for %q -> %q:\ngot  %v\nwant %v", src, out, got, want)
		}
	})
}

// keptLines returns the 1-based line numbers of the documents KeepDocuments keeps with
// keepMarked: the marked ones, those with a line starting with "%", and from a document-level
// block scalar on, everything as one document.
func keptLines(src string) map[int]bool {
	kept := map[int]bool{}
	flush := func(start, end int) {
		if doc := src[start:end]; !keepMarked([]byte(doc)) && !strings.HasPrefix(doc, "%") && !strings.Contains(doc, "\n%") {
			return
		}
		last := 1 + yamlBreaks(src[:end])
		if end < len(src) {
			last-- // the line after the final break starts the next document
		}
		for l := 1 + yamlBreaks(src[:start]); l <= last; l++ {
			kept[l] = true
		}
	}
	start, atRoot := 0, true
	for i := 0; i < len(src); {
		end := len(src)
		if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
			end = i + j + 1
		}
		line := []byte(src[i:end])
		marker := isMarker(line, "---")
		if i > start && marker {
			flush(start, i)
			start, atRoot = i, true
		}
		if isMarker(line, "...") {
			atRoot = true
		} else if root, ok := rootToken(line, marker); atRoot && ok {
			if root == '|' || root == '>' {
				break
			}
			atRoot = false
		}
		i = end
	}
	flush(start, len(src))
	return kept
}

// yamlBreaks counts the line breaks of s as the YAML scanner does: CRLF, LF, CR, NEL, LS, PS.
func yamlBreaks(s string) int {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	n := 0
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\u0085' || r == '\u2028' || r == '\u2029' {
			n++
		}
	}
	return n
}

// nodes counts every node below the document nodes of src by its rendering.
func nodes(src []byte) (map[string]int, error) {
	seen := map[string]int{}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind != yaml.DocumentNode {
			seen[fmt.Sprintf("%s|%s|%s|%d@%d:%d", n.Tag, n.Value, n.Anchor, n.Kind, n.Line, n.Column)]++
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return seen, nil
		}
		if err != nil {
			return nil, err
		}
		walk(&doc)
	}
}

func lineOf(key string) int {
	var line, col int
	_, _ = fmt.Sscanf(key[strings.LastIndexByte(key, '@')+1:], "%d:%d", &line, &col)
	return line
}
