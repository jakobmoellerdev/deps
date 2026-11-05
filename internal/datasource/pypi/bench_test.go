package pypi

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func BenchmarkDecodeHTMLFiles(b *testing.B) {
	var sb strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&sb, "<a href=\"../../packages/ab/cd/%064x/pkg-1.%d.tar.gz#sha256=%064x\" data-requires-python=\"&gt;=3.8\">pkg-1.%d.tar.gz</a><br/>\n", i, i, i, i)
	}
	body := sb.String()
	base, _ := url.Parse("https://pypi.org/simple/pkg/")
	b.ReportAllocs()
	for b.Loop() {
		n := 0
		if err := decodeHTMLFiles(strings.NewReader(body), base, func(simpleFile) { n++ }); err != nil {
			b.Fatal(err)
		}
	}
}
