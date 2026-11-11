package diff

import (
	"math/rand/v2"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestUnified(t *testing.T) {
	for name, tc := range map[string]struct{ old, updated, want string }{
		"equal": {"a\n", "a\n", ""},
		"replace": {"a\nb\nc\n", "a\nB\nc\n",
			"--- o\n+++ n\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"},
		"add to empty": {"", "x\ny\n",
			"--- o\n+++ n\n@@ -0,0 +1,2 @@\n+x\n+y\n"},
		"delete all": {"x\n", "",
			"--- o\n+++ n\n@@ -1 +0,0 @@\n-x\n"},
		"insert names the line before": {"1\n2\n3\n4\n5\n6\n7\n8\n", "1\n2\n3\n4\n4a\n5\n6\n7\n8\n",
			"--- o\n+++ n\n@@ -2,6 +2,7 @@\n 2\n 3\n 4\n+4a\n 5\n 6\n 7\n"},
		"pure deletion range": {"1\n2\n3\n4\n5\n6\n7\n8\n9\n", "1\n2\n3\n4\n6\n7\n8\n9\n",
			"--- o\n+++ n\n@@ -2,7 +2,6 @@\n 2\n 3\n 4\n-5\n 6\n 7\n 8\n"},
		"no newline at end": {"a\nb", "a\nc",
			"--- o\n+++ n\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n"},
		"newline added": {"a", "a\n",
			"--- o\n+++ n\n@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+a\n"},
		// Changes 6 lines apart share a hunk; 7 apart split it.
		"merged hunk": {"c1\n1\n2\n3\n4\n5\n6\nc2\n", "C1\n1\n2\n3\n4\n5\n6\nC2\n",
			"--- o\n+++ n\n@@ -1,8 +1,8 @@\n-c1\n+C1\n 1\n 2\n 3\n 4\n 5\n 6\n-c2\n+C2\n"},
		"split hunks": {"c1\n1\n2\n3\n4\n5\n6\n7\nc2\n", "C1\n1\n2\n3\n4\n5\n6\n7\nC2\n",
			"--- o\n+++ n\n@@ -1,4 +1,4 @@\n-c1\n+C1\n 1\n 2\n 3\n@@ -6,4 +6,4 @@\n 5\n 6\n 7\n-c2\n+C2\n"},
	} {
		if got := Unified("o", "n", tc.old, tc.updated); got != tc.want {
			t.Errorf("%s:\ngot\n%s\nwant\n%s", name, got, tc.want)
		}
	}
}

// TestUnifiedApplies applies random diffs back onto the old text.
func TestUnifiedApplies(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	gen := func() string {
		var b strings.Builder
		for range r.IntN(30) {
			b.WriteString(string(rune('a' + r.IntN(4))))
			b.WriteByte('\n')
		}
		if r.IntN(4) == 0 {
			b.WriteString("z")
		}
		return b.String()
	}
	for range 2000 {
		old, updated := gen(), gen()
		d := Unified("o", "n", old, updated)
		if got := apply(t, old, d); got != updated {
			t.Fatalf("apply(%q, diff) = %q, want %q\n%s", old, got, updated, d)
		}
	}
}

// apply is a strict patch: every context and deleted line must match old at the position the
// hunk header names.
func apply(t *testing.T, old, d string) string {
	t.Helper()
	if d == "" {
		return old
	}
	src := lines(old)
	var out []string
	pos := 0
	dl := strings.SplitAfter(d, "\n")[2:] // skip ---/+++
	for i := 0; i < len(dl) && dl[i] != ""; i++ {
		l := dl[i]
		if strings.HasPrefix(l, "@@") {
			hdr, _, _ := strings.Cut(strings.TrimPrefix(l, "@@ -"), " ")
			startStr, countStr, ok := strings.Cut(hdr, ",")
			start, err := strconv.Atoi(startStr)
			if err != nil {
				t.Fatalf("bad header %q", l)
			}
			if ok && countStr == "0" {
				start++ // empty range names the line before
			}
			for ; pos < start-1; pos++ {
				out = append(out, src[pos])
			}
			continue
		}
		text := l[1:]
		if i+1 < len(dl) && strings.HasPrefix(dl[i+1], `\`) {
			text = strings.TrimSuffix(text, "\n")
			i++
		}
		switch l[0] {
		case ' ', '-':
			if pos >= len(src) || src[pos] != text {
				t.Fatalf("line %d: diff has %q, old has %q\n%s", pos+1, text, src[min(pos, len(src)-1)], d)
			}
			if l[0] == ' ' {
				out = append(out, text)
			}
			pos++
		case '+':
			out = append(out, text)
		}
	}
	return strings.Join(append(out, src[pos:]...), "")
}

// TestSplitShortest checks the linear-space search against the greedy one: it has to find
// a script of the same length and that script has to describe the same two texts.
func TestSplitShortest(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	gen := func(alpha int) []string {
		l := make([]string, r.IntN(30))
		for i := range l {
			l[i] = string(rune('a'+r.IntN(alpha))) + "\n"
		}
		return l
	}
	for range 20000 {
		alpha := 2 + r.IntN(4)
		a, b := gen(alpha), gen(alpha)
		got := split(a, b)
		want, ok := greedy(a, b)
		if !ok {
			t.Fatal("greedy gave up on a tiny input")
		}
		replay(t, a, b, got)
		if edits(got) != edits(want) {
			t.Fatalf("split needs %d edits, greedy %d\na = %q\nb = %q", edits(got), edits(want), a, b)
		}
	}
}

// TestRewriteFallsBack pins where the greedy search hands over: a rewritten lockfile is
// past the trace budget, and the script for it is still the shortest one.
func TestRewriteFallsBack(t *testing.T) {
	const n = 3000
	old, updated := lockfile(n, 1)
	a, b := lines(old), lines(updated)
	if _, ok := greedy(a, b); ok {
		t.Fatal("greedy kept the trace for a rewritten file")
	}
	ops := script(a, b)
	replay(t, a, b, ops)
	if got := edits(ops); got != 2*n {
		t.Errorf("%d edits, want %d", got, 2*n)
	}
}

// TestUnifiedBoundedMemory diffs a rewritten file, which the greedy search would have
// walked with a trace of 800 MiB (and of tens of GiB for a regenerated 30k-line lockfile).
func TestUnifiedBoundedMemory(t *testing.T) {
	old, updated := lockfile(5000, 1)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d := Unified("o", "n", old, updated)
	runtime.ReadMemStats(&after)
	if mib := (after.TotalAlloc - before.TotalAlloc) >> 20; mib > 64 {
		t.Errorf("allocated %d MiB, want at most 64", mib)
	}
	if got := apply(t, old, d); got != updated {
		t.Error("the diff does not apply back")
	}
}

func edits(ops []op) int {
	n := 0
	for _, o := range ops {
		if o.kind != equal {
			n++
		}
	}
	return n
}

// replay walks a script with the cursors it claims, which checks its line indexes, its
// line texts and that it covers both inputs.
func replay(t *testing.T, a, b []string, ops []op) {
	t.Helper()
	x, y, prev := 0, 0, equal
	for i, o := range ops {
		if o.oldIdx != x || o.newIdx != y {
			t.Fatalf("op %d %q: at (%d,%d), want (%d,%d)", i, o.kind, o.oldIdx, o.newIdx, x, y)
		}
		if prev == insert && o.kind == del {
			t.Fatalf("op %d: a deletion after an insertion of the same change", i)
		}
		prev = o.kind
		switch o.kind {
		case equal:
			if o.line != a[x] || a[x] != b[y] {
				t.Fatalf("op %d: %q is not %q and %q", i, o.line, a[x], b[y])
			}
			x, y = x+1, y+1
		case del:
			if o.line != a[x] {
				t.Fatalf("op %d: deletes %q, not %q", i, o.line, a[x])
			}
			x++
		case insert:
			if o.line != b[y] {
				t.Fatalf("op %d: inserts %q, not %q", i, o.line, b[y])
			}
			y++
		}
	}
	if x != len(a) || y != len(b) {
		t.Fatalf("script ends at (%d,%d), want (%d,%d)", x, y, len(a), len(b))
	}
}
