package versioning

import (
	"strconv"
	"testing"
)

// pubBenchVersions is a pub.dev-sized release history: 0.x, 1.x and 2.x releases, every
// fifth one with a prerelease before it.
func pubBenchVersions() []string {
	var vs []string
	for major := range 3 {
		for minor := range 10 {
			for patch := range 10 {
				v := strconv.Itoa(major) + "." + strconv.Itoa(minor) + "." + strconv.Itoa(patch)
				if patch%5 == 0 {
					vs = append(vs, v+"-dev.1")
				}
				vs = append(vs, v)
			}
		}
	}
	return vs
}

func BenchmarkPubMatchRange(b *testing.B) {
	vs := pubBenchVersions()
	for _, rng := range []string{"^1.2.3", ">=1.2.3 <2.0.0"} {
		b.Run(rng, func(b *testing.B) {
			m, err := schemes["pub"].(RangeMatcher).MatchRange(rng)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				for _, v := range vs {
					m(v)
				}
			}
		})
	}
}
