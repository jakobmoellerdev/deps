package npm

import (
	"fmt"
	"sync"
	"testing"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// uncachedNewRange is NewRange before compiled ranges: the range is parsed and every
// constraint compiled from its string on each call. It is the oracle of
// TestNewRangeCompiledParity.
func uncachedNewRange(rng, v, strategy string) (string, bool) {
	r, ok := parseRange(rng)
	if !ok {
		return "", false
	}
	admitsStr := func(rng, v string) bool {
		c, err := semver.NewConstraint(rng)
		if err != nil {
			return false
		}
		sv, err := semver.NewVersion(v)
		return err == nil && c.Check(sv)
	}
	out := func() string {
		nv, err := semver.NewVersion(v)
		if err != nil {
			return r.raw
		}
		if strategy == dep.RangePin {
			return v
		}
		if admitsStr(r.raw, v) {
			for _, a := range r.alts {
				if admitsStr(r.raw[a.start:a.end], v) {
					return r.raw[:a.start] + r.render(a, nv, modeBump) + r.raw[a.end:]
				}
			}
			return r.raw
		}
		last := r.alts[len(r.alts)-1]
		if strategy == dep.RangeWiden && !r.exact() {
			if w := r.raw[:last.start] + r.render(last, nv, modeWiden) + r.raw[last.end:]; w != r.raw && admitsStr(w, v) {
				return w
			}
			sep := " || "
			if len(r.alts) > 1 {
				sep = r.raw[r.alts[len(r.alts)-2].end:last.start]
			}
			return r.raw[:last.end] + sep + r.render(last, nv, modeReplace) + r.raw[last.end:]
		}
		return r.render(last, nv, modeReplace)
	}()
	if out == rng || !admitsStr(out, v) {
		return "", false
	}
	return out, true
}

// TestNewRangeCompiledParity checks that NewRange, whose range parse and constraints are
// compiled once per range string, rewrites like the uncached oracle for every range, version
// and strategy, on the first (compiling) and later (cached) calls, concurrently as the
// engine resolves dependencies.
func TestNewRangeCompiledParity(t *testing.T) {
	ranges := []string{
		"^1.2.3", "~1.2.3", ">=1.2.3 <2", ">1.2.3 <2", "<=2.0.0", "<0.5", "1.x", "1.2.x", "^1.2",
		"v1.2.3", "=1.2.3", "1.2.3", "^1.0.0-beta.1", "1.x || 2.x", "^1.0.0 ||  ^2.0.0",
		">=1.2.3 <2.0.0 || 3.x", "1.2.3 - 2.3.4", "1 - 2", "1.0 - 2.1", "~>1.2.3", "^0.0.3",
		"1.2.3-01", // parsed, but Masterminds rejects its constraint
		"*", "latest", "1.2.3 || ", "",
	}
	versions := []string{
		"0.0.3", "0.0.4", "0.4.9", "0.7.1", "1.0.0", "1.2.3", "1.2.4", "1.4.0", "1.9.9", "2.0.0",
		"2.3.4", "2.9.0", "3.1.0", "1.0.0-beta.2", "2.0.0-rc.1", "v1.5.0", "1.3.0+build.1",
		"not-a-version",
	}
	strategies := []string{"", "bump", dep.RangeWiden, dep.RangePin}
	type result struct {
		out string
		ok  bool
	}
	want := map[string]result{}
	for _, rng := range ranges {
		for _, v := range versions {
			for _, s := range strategies {
				out, ok := uncachedNewRange(rng, v, s)
				want[rng+"\x00"+v+"\x00"+s] = result{out, ok}
			}
		}
	}
	var wg sync.WaitGroup
	errs := make(chan string, len(want)*4)
	for range 4 {
		wg.Go(func() {
			for _, rng := range ranges {
				for _, v := range versions {
					for _, s := range strategies {
						out, ok := npmManager{}.NewRange(dep.Dep{Range: rng}, v, s)
						if w := want[rng+"\x00"+v+"\x00"+s]; (result{out, ok}) != w {
							errs <- fmt.Sprintf("NewRange(%q, %s, %q) = %q, %v; want %q, %v", rng, v, s, out, ok, w.out, w.ok)
						}
					}
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestEditsRangeNotAVersion checks that a new version Masterminds cannot parse leaves the
// range as written.
func TestEditsRangeNotAVersion(t *testing.T) {
	edits, err := New().Edits(dep.Update{Dep: dep.Dep{Range: "^1.2.3"}, NewVersion: "not-a-version"})
	if err != nil || len(edits) != 1 || edits[0].New != "^1.2.3" {
		t.Errorf("edits = %+v, %v; want the range unchanged", edits, err)
	}
}

// TestAdmitsCacheCap checks that the constraints admits caches are dropped once maxGenerated
// distinct ranges were checked, and that a cleared cache still answers.
func TestAdmitsCacheCap(t *testing.T) {
	generated.Clear()
	generatedN.Store(0)
	t.Cleanup(func() { generated.Clear(); generatedN.Store(0) })
	v := semver.MustParse("1.5.0")
	for i := range maxGenerated - 1 {
		if !admits(fmt.Sprintf(">=1.0.0 <%d.0.0", i+2), v) {
			t.Fatalf("<%d.0.0 rejects 1.5.0", i+2)
		}
	}
	if n := generatedN.Load(); n != maxGenerated-1 {
		t.Fatalf("cached %d ranges, want %d", n, maxGenerated-1)
	}
	if admits("<1.0.0", v) || generatedN.Load() != 0 {
		t.Errorf("cache not cleared at %d entries: %d", maxGenerated, generatedN.Load())
	}
	if _, hit := generated.Load(">=1.0.0 <2.0.0"); hit {
		t.Error("old entry survived the clear")
	}
	if !admits(">=1.0.0 <2.0.0", v) || admits("not a range", v) {
		t.Error("admits after clear")
	}
}
