package versioning

import (
	"fmt"
	"testing"
)

// BenchmarkNuGetMatchRange tests 500 candidate releases against an interval range.
func BenchmarkNuGetMatchRange(b *testing.B) {
	var cands []string
	for i := range 500 {
		cands = append(cands, fmt.Sprintf("%d.%d.%d", i/100, i/10%10, i%10))
	}
	s, _ := Get("nuget")
	m := s.(RangeMatcher)
	b.ReportAllocs()
	for b.Loop() {
		match, err := m.MatchRange("[1.0,3.0)")
		if err != nil {
			b.Fatal(err)
		}
		for _, v := range cands {
			match(v)
		}
	}
}
