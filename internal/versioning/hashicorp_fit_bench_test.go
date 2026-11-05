package versioning

import (
	"fmt"
	"testing"
)

// BenchmarkHashicorpFitPartial ranks provider releases against a partial `~> 5.0` style
// current value, where most candidates collapse to an already seen 5.x.
func BenchmarkHashicorpFitPartial(b *testing.B) {
	var cands []string
	for i := range 3000 {
		cands = append(cands, fmt.Sprintf("%d.%d.%d", 5+i/1000, i/30%40, i%30))
	}
	s, _ := Get("hashicorp")
	b.ReportAllocs()
	for b.Loop() {
		r := s.Rank("5.0", cands, nil, nil)
		for i := range min(r.Len(), 5) {
			_ = r.At(i)
		}
	}
}
