package versioning

import (
	"fmt"
	"testing"
)

// benchCorpus is a registry-sized release list per scheme, ascending as registries list them:
// 3000 releases with prereleases, post releases and suffixes in between, and a current
// version near the bottom so most candidates are newer.
type benchCorpus struct {
	scheme, cur string
	cands       []string
}

func benchCorpora() []benchCorpus {
	var semver, loose, pep, maven, hc, cargo, gov []string
	for i := range 3000 {
		maj, mnr, pat := i/600, i/30%20, i%30
		plain := fmt.Sprintf("%d.%d.%d", maj, mnr, pat)
		semver = append(semver, plain)
		cargo = append(cargo, plain)
		hc = append(hc, plain)
		pep = append(pep, plain)
		maven = append(maven, plain)
		gov = append(gov, "v"+plain)
		loose = append(loose, fmt.Sprintf("%s.%d", plain, i%7))
		switch i % 5 {
		case 0:
			semver = append(semver, fmt.Sprintf("%d.%d.%d-rc.%d", maj, mnr, pat+1, i%4))
			cargo = append(cargo, fmt.Sprintf("%d.%d.%d-beta.%d", maj, mnr, pat+1, i%4))
			hc = append(hc, fmt.Sprintf("%d.%d.%d-alpha%d", maj, mnr, pat+1, i%4))
			gov = append(gov, fmt.Sprintf("v%d.%d.%d-rc.%d", maj, mnr, pat+1, i%4))
			pep = append(pep, fmt.Sprintf("%d.%d.%drc%d", maj, mnr, pat+1, i%4))
			maven = append(maven, fmt.Sprintf("%d.%d.%d-RC%d", maj, mnr, pat+1, i%4))
			loose = append(loose, fmt.Sprintf("%d.%d-rc%d", maj, mnr, i%4))
		case 1:
			semver = append(semver, fmt.Sprintf("%s-dev.20240%03d", plain, i))
			pep = append(pep, fmt.Sprintf("%s.post%d", plain, i%3))
			maven = append(maven, plain+"-jre")
			gov = append(gov, fmt.Sprintf("v%d.%d.%d-0.2024%04d120000-abcdef%06d", maj, mnr, pat+1, i, i))
		case 2:
			// x.y equals x.y.0 and plain.0 equals plain: ties.
			pep = append(pep, fmt.Sprintf("%s.dev%d", plain, i%3), fmt.Sprintf("%d.%d", maj, mnr))
			maven = append(maven, plain+"-SNAPSHOT", plain+".0")
			hc = append(hc, plain+"-beta")
		}
	}
	return []benchCorpus{
		{"semver", "1.2.3", semver},
		{"cargo", "1.2.3", cargo},
		{"hashicorp", "1.2.3", hc},
		{"go", "v1.2.3", gov},
		{"loose", "1.2.3.0", loose},
		{"pep440", "1.2.3", pep},
		{"maven", "1.2.3", maven},
	}
}

// BenchmarkRank ranks a registry-sized release list per scheme and renders the first few
// candidates, as the engine does for one dependency.
func BenchmarkRank(b *testing.B) {
	for _, c := range benchCorpora() {
		s, _ := Get(c.scheme)
		b.Run(c.scheme, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				r := s.Rank(c.cur, c.cands, nil, nil)
				for i := range min(r.Len(), 5) {
					_ = r.At(i)
				}
			}
		})
	}
}
