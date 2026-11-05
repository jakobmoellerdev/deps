package npm

import (
	"fmt"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// BenchmarkClassifyScanExtract extracts a package.json with 100 dependencies in the
// common plain/caret/tilde/>= spellings.
func BenchmarkClassifyScanExtract(b *testing.B) {
	ops := []string{"^", "~", ">=", "", "="}
	var sb strings.Builder
	sb.WriteString("{\n  \"name\": \"bench\",\n  \"dependencies\": {\n")
	for i := range 100 {
		if i > 0 {
			sb.WriteString(",\n")
		}
		pre := ""
		if i%10 == 0 {
			pre = "-beta.1"
		}
		fmt.Fprintf(&sb, "    \"pkg-%d\": \"%s%d.%d.%d%s\"", i, ops[i%len(ops)], i%7+1, i%13, i%31, pre)
	}
	sb.WriteString("\n  }\n}\n")
	src := []byte(sb.String())
	m := New()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m.Extract("package.json", src, manager.Env{}); err != nil {
			b.Fatal(err)
		}
	}
}
