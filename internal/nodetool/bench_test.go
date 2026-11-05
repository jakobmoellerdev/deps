package nodetool

import (
	"bufio"
	"bytes"
	"fmt"
	"testing"
)

// benchLock returns a package-lock.json (lockfileVersion 3) shaped like a website's: n
// hoisted packages with resolved URL, integrity, license and dependencies, every tenth also
// nested below another; keys are 30 of the hoisted names.
func benchLock(n int) ([]byte, map[string]bool) {
	var b bytes.Buffer
	b.WriteString(`{"name":"site","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"site","dependencies":{"p0":"^1.0.0"}}`)
	for i := range n {
		fmt.Fprintf(&b, `,"node_modules/p%d":{"version":"1.%d.0","resolved":"https://registry.npmjs.org/p%d/-/p%d-1.%d.0.tgz","integrity":"sha512-%064d","license":"MIT","dependencies":{"p%d":"^1.0.0","p%d":"~2.1.0"},"engines":{"node":">=18"}}`,
			i, i%40, i, i, i%40, i, (i+1)%n, (i+7)%n)
		if i%10 == 0 {
			fmt.Fprintf(&b, `,"node_modules/p%d/node_modules/p%d":{"version":"0.%d.1","dev":true}`, i, (i+3)%n, i%9)
		}
	}
	b.WriteString(`}}`)
	keys := map[string]bool{}
	for i := range 30 {
		keys[fmt.Sprintf("node_modules/p%d", i*n/30)] = true
	}
	return b.Bytes(), keys
}

// BenchmarkLockedVersions reads 30 wanted versions from a 2000-package lockfile.
func BenchmarkLockedVersions(b *testing.B) {
	src, keys := benchLock(2000)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		if _, v, err := lockedVersions(bufio.NewReader(bytes.NewReader(src)), keys); err != nil || len(v) != 30 {
			b.Fatal(err, len(v))
		}
	}
}
