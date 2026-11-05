package versioning

import (
	"cmp"
	"slices"
	"testing"
)

// checkRankOrder checks that rank orders like a stable sort by version, best first: equal
// versions keep their candidate order.
func checkRankOrder[T any](t *testing.T, name string, s impl[T], cur string, cands []string) {
	t.Helper()
	r := s.rank(cur, cands, nil, nil)
	if len(r.kept) == 0 {
		t.Fatalf("%s: nothing ranked", name)
	}
	want := slices.Clone(r.kept)
	slices.SortFunc(want, func(a, b rankedVersion[T]) int { return cmp.Compare(a.i, b.i) })
	slices.SortStableFunc(want, func(a, b rankedVersion[T]) int { return s.cmp(b.v, a.v) })
	for i := range want {
		if r.kept[i].i != want[i].i {
			t.Fatalf("%s: rank[%d] = %s, want %s", name, i, cands[r.kept[i].i], cands[want[i].i])
		}
	}
}

func TestRankOrderIsStable(t *testing.T) {
	for _, c := range benchCorpora() {
		switch s := schemes[c.scheme].(type) {
		case semverScheme:
			checkRankOrder(t, c.scheme, s.impl, c.cur, c.cands)
		case pep440Scheme:
			checkRankOrder(t, c.scheme, s.impl, c.cur, c.cands)
		case hashicorpScheme:
			checkRankOrder(t, c.scheme, s.impl, c.cur, c.cands)
		case impl[cargoVersion]:
			checkRankOrder(t, c.scheme, s, c.cur, c.cands)
		case impl[goVersion]:
			checkRankOrder(t, c.scheme, s, c.cur, c.cands)
		case impl[looseVersion]:
			checkRankOrder(t, c.scheme, s, c.cur, c.cands)
		case impl[mavenVersion]:
			checkRankOrder(t, c.scheme, s, c.cur, c.cands)
		default:
			t.Fatalf("%s: unexpected scheme type %T", c.scheme, s)
		}
	}
	// Equal versions in a short list, shuffled around other versions.
	maven := schemes["maven"].(impl[mavenVersion])
	checkRankOrder(t, "maven ties", maven, "1", []string{"2.0", "3", "2", "2.0.0", "3.0", "2-ga"})
	r := maven.rank("1", []string{"2.0", "3", "2", "2.0.0", "3.0", "2-ga"}, nil, nil)
	var got []string
	for i := range r.Len() {
		got = append(got, r.At(i).Version)
	}
	if want := []string{"3", "3.0", "2.0", "2", "2.0.0", "2-ga"}; !slices.Equal(got, want) {
		t.Errorf("maven Rank = %v, want %v", got, want)
	}
}
