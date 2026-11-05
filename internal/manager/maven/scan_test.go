package maven

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dumpTree renders n and its descendants, one element per line.
func dumpTree(n *node) string {
	var b strings.Builder
	var walk func(n *node, depth int)
	walk = func(n *node, depth int) {
		fmt.Fprintf(&b, "%s<%s> line %d value %q span %v pieces %d\n", strings.Repeat("  ", depth), n.name, n.line, n.value, n.span, n.pieces)
		for _, k := range n.kids {
			if k.parent != n {
				fmt.Fprintf(&b, "%s  bad parent of <%s>\n", strings.Repeat("  ", depth), k.name)
			}
			walk(k, depth+1)
		}
	}
	walk(n, 0)
	return b.String()
}

// checkScan fails t if scan accepts src but decode rejects it or builds another tree.
func checkScan(t *testing.T, src []byte) bool {
	t.Helper()
	got, ok := scan(src)
	if !ok {
		return false
	}
	want, err := decode(src)
	if err != nil {
		t.Fatalf("scan accepts %q, decode: %v", src, err)
	}
	if g, w := dumpTree(got), dumpTree(want); g != w {
		t.Fatalf("%q: scan\n%s\ndecode\n%s", src, g, w)
	}
	return true
}

// pomFixtures returns the XML files of testdata.
func pomFixtures(tb testing.TB) map[string][]byte {
	tb.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".xml" {
			return err
		}
		b, err := os.ReadFile(p)
		out[p] = b
		return err
	})
	if err != nil {
		tb.Fatal(err)
	}
	if len(out) == 0 {
		tb.Fatal("no XML fixtures")
	}
	return out
}

func TestScanFixtures(t *testing.T) {
	for name, src := range pomFixtures(t) {
		if !checkScan(t, src) {
			t.Errorf("%s: scan falls back", name)
		}
		crlf := []byte(strings.ReplaceAll(string(src), "\n", "\r\n"))
		checkScan(t, crlf)
	}
}

func TestScan(t *testing.T) {
	for _, tc := range []struct {
		src  string
		scan bool // scan builds the tree, else decode
	}{
		{`<?xml version="1.0" encoding="UTF-8"?><project><version>1</version></project>`, true},
		{`<?xml version='1.0' encoding='ISO-8859-1'?><project/>`, true},
		{`<?xml version="1.1"?><project/>`, false},
		{`<?xml?><project/>`, true},
		{"<project\n  xmlns=\"http://maven.apache.org/POM/4.0.0\"\txmlns:xsi='x' xsi:schemaLocation=\"a b\">\n</project>", true},
		{`<project a="1"b="2"/>`, true},
		{`<project a=1/>`, false},
		{`<project a/>`, false},
		{`<project a="<"/>`, false},
		{`<project a="&amp;"/>`, false},
		{`<project a="x/>`, false},
		{`<p:project><p:version>1</p:version></p:project>`, true},
		{`<p:project></q:project>`, false},
		{`<a:b:c/>`, false},
		{`<:a/>`, false},
		{`<a:/>`, false},
		{`<1a/>`, false},
		{`<-a/>`, false},
		{`<é/>`, false},
		{`<aé/>`, false},
		{`< a/>`, false},
		{`<a></b>`, false},
		{`<a>`, false},
		{`</a>`, false},
		{`<a></a >`, true},
		{`<a></a x>`, false},
		{`<a/ >`, false},
		{`<a><!-- c --></a>`, true},
		{`<a><!---- --></a>`, false},
		{`<a><!-- a--b --></a>`, false},
		{`<a><!--></a>`, false},
		{`<a><!- x --></a>`, false},
		{`<a><![CDATA[x]]></a>`, true},
		{`<!DOCTYPE a><a/>`, false},
		{`<a><![CDATA[ ]]></a>`, true},
		{`<a>x<![CDATA[<y>&amp;]]>z</a>`, true},
		{`<a><![CDATA[x</a>`, false},
		{`<a><![CDAT[x]]></a>`, false},
		{`<a>&nbsp;&lt;&#x41;</a>`, true},
		{`<a>&bogus;</a>`, false},
		{`<a>&amp</a>`, false},
		{`<a>&#xZZ;</a>`, false},
		{`<a>x &amp; y</a>`, true},
		{`<a>x &#65; y</a>`, true},
		{`<a>x ]]> y</a>`, false},
		{`<a>x ]> y ] ></a>`, true},
		{"<a> 1\r\n</a>", true},
		{"<a>1\r2</a>", true},
		{"<a>1\r\n2</a>", true},
		{"<a>\x00</a>", false},
		{"<a>\x7f</a>", true},
		{"<a>\xff</a>", false},
		{"<a>\uFFFE</a>", false},
		{"\uFEFF<a>Jörg</a>", true},
		{"<a>1<!-- x -->2</a>", true},
		{"<a> 1 <b/> 2 </a>", true},
		{"<a><?pi x?>1</a>", true},
		{"<a><?pi x</a>", false},
		{"<a><? x?></a>", false},
		{"text<a/>tail", true},
		{"<a/><b/>", true},
		{"", true},
		{"<", false},
		{"<a", false},
		{"<a x", false},
		{"<a x=", false},
		{"<a/", false},
	} {
		if got := checkScan(t, []byte(tc.src)); got != tc.scan {
			t.Errorf("%q: scan %v, want %v", tc.src, got, tc.scan)
		}
	}
}

func FuzzScan(f *testing.F) {
	for _, src := range pomFixtures(f) {
		f.Add(src)
	}
	for _, s := range []string{
		`<?xml version="1.0"?><a b='c'><d>1</d><!-- e --><f/></a>`,
		"<a>\r\n x\r\n</a>", "<p:a xmlns:p='u'><p:b>1</p:b></p:a>", "<a>]]></a>", "<a>&lt;</a>",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		checkScan(t, src)
	})
}
