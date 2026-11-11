package yamlspan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

func keepAll([]byte) bool { return true }

// nodeLines returns one line per node of the documents n, with everything managers read from a
// node: its position in the file, kind, tag, value and anchor.
func nodeLines(n *yaml.Node, out []string) []string {
	out = append(out, fmt.Sprintf("%d:%d k=%d %s %q &%s", n.Line, n.Column, n.Kind, n.Tag, n.Value, n.Anchor))
	for _, c := range n.Content {
		out = nodeLines(c, out)
	}
	return out
}

// wholeFile returns the nodes of the documents of src as one decoder over all of it yields
// them, the oracle of the parity tests; ok is false when src does not parse. Empty documents
// are dropped: a document no manager keeps yields none at all.
func wholeFile(src []byte) (out []string, ok bool) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out, true
		}
		if err != nil {
			return nil, false
		}
		if len(doc.Content) > 0 {
			out = nodeLines(&doc, out)
		}
	}
}

// checkDocumentsParity checks that Documents yields the nodes of a whole-file decode, with
// the lines and columns of src, so that Locate against src works for every document.
func checkDocumentsParity(t *testing.T, src []byte) {
	t.Helper()
	want, ok := wholeFile(src)
	if !ok {
		return
	}
	var got []string
	if err := (&Parsed{}).Documents(src, keepAll, func(doc *yaml.Node) {
		if len(doc.Content) > 0 {
			got = nodeLines(doc, got)
		}
	}); err != nil {
		t.Fatalf("%q: Documents = %v, want the nodes of the whole file", src, err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%q:\nDocuments  = %v\nwhole file = %v", src, got, want)
	}
}

var documentSeeds = []string{
	"a: 1\n---\nb: two\n---\nc: [3, 4]\n",
	"# head\n---\nkind: A\n...\n---\nkind: B\n",
	"a: &x 1\nb: *x\n---\nc: &x 2\nd: *x\n",
	"%YAML 1.2\n---\na: 1\n...\n---\nb: 2\n",
	"%TAG !e! tag:example.com,2000:app/\n---\n!e!foo\na: 1\n---\nb: 2\n",
	"a: |\n  ---\n  not a document\n---\nb: 2\n",
	"--- |\n  text\n--- x\nb: 2\n",
	"\ufeffa: 1\n---\nb: 2\n",
	"a: 1\r\n---\r\nb: 2\r\n",
	"---\n---\n---\nlast: 1\n",
	"",
}

func TestDocumentsParity(t *testing.T) {
	for _, s := range documentSeeds {
		checkDocumentsParity(t, []byte(s))
	}
}

func FuzzDocuments(f *testing.F) {
	for _, s := range documentSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		checkDocumentsParity(t, []byte(src))
	})
}

// TestDocumentsDecodesOnce checks the point of the per-file cache: a document no predicate
// keeps is never parsed, and one two managers keep is parsed once, for both.
func TestDocumentsDecodesOnce(t *testing.T) {
	src := []byte("kind: skipped\n---\nkind: kept\nimage: a:1\n---\nkind: other\n")
	p := &Parsed{}
	var first []*yaml.Node
	if err := p.Documents(src, func(doc []byte) bool { return bytes.Contains(doc, []byte("kept")) },
		func(doc *yaml.Node) { first = append(first, doc) }); err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("documents = %d, want 1", len(first))
	}
	if p.docs.chunks[0].done || p.docs.chunks[2].done {
		t.Error("a document no predicate keeps was parsed")
	}
	var second []*yaml.Node
	if err := p.Documents(src, keepAll, func(doc *yaml.Node) { second = append(second, doc) }); err != nil {
		t.Fatal(err)
	}
	if len(second) != 3 {
		t.Fatalf("documents = %d, want 3", len(second))
	}
	if second[1] != first[0] {
		t.Error("a document kept by two managers was parsed twice")
	}
}

// TestDocumentsCacheBound checks that a file with more document bytes than maxCached does not
// keep all of their nodes: the documents beyond the bound are handed out and dropped, and the
// next manager gets them again.
func TestDocumentsCacheBound(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() <= maxCached; i++ {
		fmt.Fprintf(&b, "---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d%d\nspec:\n  template:\n    spec:\n"+
			"      containers:\n      - name: app\n        image: quay.io/example/app:v1.%d.0\n", i, i)
	}
	src := []byte(b.String())
	p := &Parsed{}
	var first int
	if err := p.Documents(src, keepAll, func(*yaml.Node) { first++ }); err != nil {
		t.Fatal(err)
	}
	if last := p.docs.chunks[len(p.docs.chunks)-1]; last.done || last.docs != nil {
		t.Error("beyond the bound: want the documents dropped")
	}
	if p.docs.cached > maxCached {
		t.Errorf("cached = %d bytes, want at most %d", p.docs.cached, maxCached)
	}
	var second int
	if err := p.Documents(src, keepAll, func(*yaml.Node) { second++ }); err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("second manager: %d documents, want the %d of the first", second, first)
	}
}

// TestDocumentsErrors checks that a parse error stays in the document it is in: it reaches
// only the managers keeping that document, after the documents before it, and is kept for
// the next manager instead of parsing the document again (a Helm template, which the docker
// and the helm manager both keep and neither can parse).
func TestDocumentsErrors(t *testing.T) {
	src := []byte("image: a:1\n---\nimage: {{ .Values.image\n")
	p := &Parsed{}
	var kept int
	if err := p.Documents(src, func(doc []byte) bool { return !bytes.Contains(doc, []byte("{{")) },
		func(*yaml.Node) { kept++ }); err != nil || kept != 1 {
		t.Errorf("without the templated document: %d documents, %v", kept, err)
	}
	kept = 0
	err := p.Documents(src, keepAll, func(*yaml.Node) { kept++ })
	if err == nil || kept != 1 {
		t.Fatalf("with it: %d documents, %v; want the first document and an error", kept, err)
	}
	// The second manager gets the error of the first parse, not the error of another one.
	if again := p.Documents(src, keepAll, func(*yaml.Node) {}); !errors.Is(again, err) {
		t.Errorf("second manager: err = %v, want the kept %v", again, err)
	}
}

// TestDocumentsChunks checks the document splitting: a directive takes the document it
// applies to in, and bytes whose lines a parser reads differently are one document.
func TestDocumentsChunks(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      []int // the line of each chunk's first byte, 0-based
	}{
		{"documents", "a: 1\n---\nb: 2\n---\nc: 3\n", []int{0, 1, 3}},
		{"directive", "%TAG !e! tag:example.com,2000:app/\n---\na: 1\n...\n---\nb: 2\n", []int{0, 4}},
		{"block scalar root", "a: 1\n--- |\n  ---\n---\nb: 2\n", []int{0, 1}},
		{"unsplittable", "a: 1\r---\nb: 2\n", []int{0}},
	} {
		p := &Parsed{}
		if err := p.Documents([]byte(c.src), keepAll, func(*yaml.Node) {}); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		got := make([]int, len(p.docs.chunks))
		for i, ch := range p.docs.chunks {
			got[i] = ch.line
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: chunk lines = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestDocumentsWithoutParsed checks that a nil *Parsed parses anew, like Document's.
func TestDocumentsWithoutParsed(t *testing.T) {
	var p *Parsed
	var lines []int
	if err := p.Documents([]byte("a: 1\n---\nb: 2\n"), keepAll, func(doc *yaml.Node) {
		lines = append(lines, doc.Content[0].Line)
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(lines) != "[1 3]" {
		t.Errorf("lines = %v, want [1 3]", lines)
	}
}

// TestDocumentsRefreshes checks that other bytes than the previous call's are split and
// parsed anew.
func TestDocumentsRefreshes(t *testing.T) {
	p := &Parsed{}
	var values []string
	collect := func(doc *yaml.Node) { values = append(values, doc.Content[0].Content[1].Value) }
	if err := p.Documents([]byte("a: 1\n"), keepAll, collect); err != nil {
		t.Fatal(err)
	}
	if err := p.Documents([]byte("a: 2\n"), keepAll, collect); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(values) != "[1 2]" {
		t.Errorf("values = %v, want [1 2]", values)
	}
}
