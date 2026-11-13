package versioning

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// sortVersions is a few hundred release names as a registry lists them: plain releases,
// "v" prefixes, two- and four-part versions, prereleases, build metadata, epochs,
// revisions and a handful of strings no scheme parses, so the order of valid and invalid
// versions is covered for every scheme.
func sortVersions() []string {
	forms := []string{
		"%d.%d.%d", "v%d.%d.%d", "%d.%d", "%[1]d", "%d.%d.%d.%d", "%d.%d.%d-rc%[4]d",
		"%d.%d.%d-alpha.%[4]d", "%d.%d.%d.Final", "%d.%d.%d+build%[4]d", "%d.%d.%d-r%[4]d",
		"%d.%d.%d_alpha%[4]d", "%d!%d.%d.%d", "%d.%d.%d-SNAPSHOT", "%d.%d.%dpost%[4]d",
		"%d.%d.%d-%[4]d.el8", "%d.%d.%d~rc%[4]d",
	}
	vs := make([]string, 0, 4*len(forms)*len(forms))
	for i := range 24 {
		maj, mnr, pat, n := i/8, i/3%5, i%7, i%4
		for _, f := range forms {
			vs = append(vs, fmt.Sprintf(f, maj, mnr, pat, n))
		}
	}
	return append(vs, "", "latest", "stable", "main", "sha-3a22eba", "1.2.3.4.5.6.7.8.9",
		"....", "v", "1.2.3-", "2:1.2.3-4", "1.2.3 ", "VERSION", "1.2.3/4")
}

// TestSortMatchesCompare pins Sort, SortDesc and SortBy to the order they replace: the
// stable sort by Compare every caller used before, ties included.
func TestSortMatchesCompare(t *testing.T) {
	t.Parallel()
	names := append(slices.Clone(Names), RegexPrefix+`^(?<major>\d+)\.(?<minor>\d+)(\.(?<patch>\d+))?`)
	for _, name := range names {
		s, err := Parse(name)
		if err != nil {
			t.Fatalf("Parse(%q): %v", name, err)
		}
		corpora := [][]string{sortVersions()}
		for _, c := range benchCorpora() {
			if c.scheme == name {
				corpora = append(corpora, c.cands[:600])
			}
		}
		for i, vs := range corpora {
			t.Run(fmt.Sprintf("%s/%d", name, i), func(t *testing.T) {
				t.Parallel()
				want := slices.Clone(vs)
				slices.SortStableFunc(want, s.Compare)
				got := slices.Clone(vs)
				s.Sort(got)
				if !slices.Equal(got, want) {
					t.Errorf("Sort differs from a stable sort by Compare:\n got %q\nwant %q", first(got, want), first(want, got))
				}
				wantDesc := slices.Clone(vs)
				slices.SortStableFunc(wantDesc, func(a, b string) int { return s.Compare(b, a) })
				gotDesc := slices.Clone(vs)
				s.SortDesc(gotDesc)
				if !slices.Equal(gotDesc, wantDesc) {
					t.Errorf("SortDesc differs from a stable sort by reversed Compare:\n got %q\nwant %q", first(gotDesc, wantDesc), first(wantDesc, gotDesc))
				}
			})
		}
	}
}

// first returns got around the first index where it differs from want, for a readable failure.
func first(got, want []string) []string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return got[max(0, i-2):min(len(got), i+3)]
		}
	}
	return got[:min(len(got), 5)]
}

// TestSortByMatchesCompare pins SortBy, with and without a tiebreak, to the stable sorts
// the galaxy and apk datasources ran by Compare.
func TestSortByMatchesCompare(t *testing.T) {
	t.Parallel()
	type rel struct {
		version string
		n       int // input position, to see which of two equal versions came first
	}
	for _, name := range []string{"semver", "apk", "maven", "loose", "rpm"} {
		s, err := Parse(name)
		if err != nil {
			t.Fatalf("Parse(%q): %v", name, err)
		}
		vs := sortVersions()
		rels := make([]rel, len(vs))
		for i, v := range vs {
			rels[i] = rel{version: v, n: i}
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := slices.Clone(rels)
			slices.SortStableFunc(want, func(a, b rel) int { return s.Compare(a.version, b.version) })
			got := slices.Clone(rels)
			SortBy(s, got, func(r rel) string { return r.version }, nil)
			if !slices.Equal(got, want) {
				t.Errorf("SortBy differs from a stable sort by Compare:\n got %v\nwant %v", got, want)
			}
			wantTie := slices.Clone(rels)
			slices.SortStableFunc(wantTie, func(a, b rel) int {
				if c := s.Compare(a.version, b.version); c != 0 {
					return c
				}
				return strings.Compare(a.version, b.version)
			})
			gotTie := slices.Clone(rels)
			SortBy(s, gotTie, func(r rel) string { return r.version },
				func(a, b rel) int { return strings.Compare(a.version, b.version) })
			if !slices.Equal(gotTie, wantTie) {
				t.Errorf("SortBy with a tiebreak differs from the same stable sort:\n got %v\nwant %v", gotTie, wantTie)
			}
		})
	}
}

// TestOrderMatchesCompare checks the comparator Order returns against Compare pair by pair.
func TestOrderMatchesCompare(t *testing.T) {
	t.Parallel()
	vs := sortVersions()
	for _, name := range Names {
		s, err := Parse(name)
		if err != nil {
			t.Fatalf("Parse(%q): %v", name, err)
		}
		c := s.Order(vs)
		for i := range vs {
			for j := range vs {
				if got, want := sign(c.Compare(i, j)), sign(s.Compare(vs[i], vs[j])); got != want {
					t.Fatalf("%s: Order.Compare(%q, %q) = %d, Compare = %d", name, vs[i], vs[j], got, want)
				}
			}
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// BenchmarkSort sorts a registry-sized release list per scheme, as the datasources and the
// go.mod resolver do: impl=compare is the stable sort by Compare that parses both operands
// of every comparison, impl=sort the parse-once Sort.
func BenchmarkSort(b *testing.B) {
	corpora := benchCorpora()
	apk := make([]string, 0, 3000)
	for i := range 3000 {
		apk = append(apk, fmt.Sprintf("%d.%d.%d-r%d", i/600, i/30%20, i%30, i%4))
	}
	corpora = append(corpora, benchCorpus{scheme: "apk", cur: "1.2.3-r0", cands: apk})
	seen := map[string]int{}
	for _, c := range corpora {
		s, _ := Get(c.scheme)
		vs := c.cands[:min(len(c.cands), 600)]
		name := c.scheme
		seen[c.scheme]++
		if n := seen[c.scheme]; n > 1 {
			name = fmt.Sprintf("%s%d", c.scheme, n)
		}
		buf := make([]string, len(vs))
		b.Run(name+"/impl=compare", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				copy(buf, vs)
				slices.SortStableFunc(buf, s.Compare)
			}
		})
		b.Run(name+"/impl=sort", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				copy(buf, vs)
				s.Sort(buf)
			}
		})
	}
}
