package yamlspan

import (
	"strings"
	"testing"
)

// BenchmarkLineStartsManifest finds the line starts of a 40k-line Kubernetes manifest.
func BenchmarkLineStartsManifest(b *testing.B) {
	src := []byte(strings.Repeat("      containers:\n        - name: app\n          image: nginx:1.25\n\n", 10000))
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		if n := len(LineStarts(src)); n != 40001 {
			b.Fatal(n)
		}
	}
}
