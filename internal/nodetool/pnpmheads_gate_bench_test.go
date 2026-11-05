package nodetool

import (
	"bufio"
	"bytes"
	"fmt"
	"testing"
)

func BenchmarkPnpmHeadsGate(b *testing.B) {
	var buf bytes.Buffer
	buf.WriteString("lockfileVersion: '9.0'\n\nimporters:\n  .:\n    dependencies: {}\n\npackages:\n\n")
	for i := range 20000 {
		fmt.Fprintf(&buf, "  pkg-%d@1.0.%d:\n    resolution: {integrity: sha512-abcdef%d}\n    engines: {node: '>=14'}\n\n", i, i, i)
	}
	data := buf.Bytes()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := pnpmHeads(bufio.NewReader(bytes.NewReader(data))); err != nil {
			b.Fatal(err)
		}
	}
}
