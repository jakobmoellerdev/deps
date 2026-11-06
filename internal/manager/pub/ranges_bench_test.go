package pub

import (
	"strconv"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func BenchmarkPubNewRange(b *testing.B) {
	var vs []string
	for major := 1; major < 4; major++ {
		for minor := range 10 {
			for patch := range 10 {
				vs = append(vs, strconv.Itoa(major)+"."+strconv.Itoa(minor)+"."+strconv.Itoa(patch))
			}
		}
	}
	for _, strategy := range []string{"", dep.RangeWiden} {
		b.Run("strategy="+strategy, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, v := range vs {
					newRange("^1.2.3", v, strategy)
				}
			}
		})
	}
}
