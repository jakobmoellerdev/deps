// Package diff renders line-based unified diffs (diff -u, git apply and patch compatible).
package diff

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// context is the number of unchanged lines around each change.
const context = 3

type kind byte

const (
	equal  kind = ' '
	del    kind = '-'
	insert kind = '+'
)

// op is one line of the edit script: its kind, text (with its newline, if any) and the
// 0-based indexes of the next old and new line before it.
type op struct {
	kind   kind
	line   string
	oldIdx int
	newIdx int
}

// Unified returns the unified diff from old to updated with headers oldName and newName, or ""
// when they are equal. A last line without a newline is marked "\ No newline at end of file".
func Unified(oldName, newName, old, updated string) string {
	if old == updated {
		return ""
	}
	ops := script(lines(old), lines(updated))
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldName, newName)
	for start := 0; start < len(ops); {
		if ops[start].kind == equal {
			start++
			continue
		}
		last := start // last change of the hunk
		for i := start + 1; i < len(ops); i++ {
			if ops[i].kind != equal {
				if i-last-1 > 2*context {
					break
				}
				last = i
			}
		}
		from, to := max(0, start-context), min(len(ops), last+1+context)
		writeHunk(&b, ops[from:to])
		start = to
	}
	return b.String()
}

func writeHunk(b *strings.Builder, ops []op) {
	var oldN, newN int
	for _, o := range ops {
		if o.kind != insert {
			oldN++
		}
		if o.kind != del {
			newN++
		}
	}
	fmt.Fprintf(b, "@@ -%s +%s @@\n", rangeOf(ops[0].oldIdx, oldN), rangeOf(ops[0].newIdx, newN))
	for _, o := range ops {
		b.WriteByte(byte(o.kind))
		b.WriteString(o.line)
		if !strings.HasSuffix(o.line, "\n") {
			b.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

// rangeOf formats a hunk range starting at 0-based line idx: "l" for one line, "l,n"
// otherwise; an empty range names the line before it, as diff -u does.
func rangeOf(idx, n int) string {
	switch n {
	case 0:
		return fmt.Sprintf("%d,0", idx)
	case 1:
		return strconv.Itoa(idx + 1)
	}
	return fmt.Sprintf("%d,%d", idx+1, n)
}

// lines splits s after each newline; a last line without one is kept as is.
func lines(s string) []string {
	l := strings.SplitAfter(s, "\n")
	if l[len(l)-1] == "" {
		l = l[:len(l)-1]
	}
	return l
}

// script returns a shortest edit script from a to b: the common prefix and suffix are
// trimmed, the rest is diffed with Myers' O(ND) algorithm.
func script(a, b []string) []op {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ops := make([]op, 0, len(a)+len(b)-pre-suf)
	for i := range pre {
		ops = append(ops, op{equal, a[i], i, i})
	}
	for _, o := range myers(a[pre:len(a)-suf], b[pre:len(b)-suf]) {
		o.oldIdx += pre
		o.newIdx += pre
		ops = append(ops, o)
	}
	for i := range suf {
		ai, bi := len(a)-suf+i, len(b)-suf+i
		ops = append(ops, op{equal, a[ai], ai, bi})
	}
	return ops
}

// myers returns a shortest edit script from a to b, deletions before insertions within a
// change.
//
// The greedy search keeps its whole trace, which is O(D^2) ints for D edits and reaches
// tens of GiB once lock file maintenance rewrites all 30k lines of a lockfile. It
// therefore gives up at traceBudget and hands over to the linear-space search, which
// splits the problem at the middle snake instead of remembering how it found a path.
func myers(a, b []string) []op {
	if ops, ok := greedy(a, b); ok {
		return ops
	}
	return split(a, b)
}

// traceBudget is how many ints the greedy search may keep: 16 MiB, reached at around 1400
// edits, which is far more than any change short of a rewrite of the whole file.
const traceBudget = 2 << 20

// greedy returns a shortest edit script from a to b with Myers' O(ND) algorithm, or false
// when its trace would outgrow traceBudget.
func greedy(a, b []string) ([]op, bool) {
	n, m := len(a), len(b)
	off := n + m + 1
	v := make([]int, 2*off+1) // v[off+k]: furthest x on diagonal k
	// trace[d] holds v[off-d-1 : off+d+2] before round d: the diagonals -d-1..d+1 that
	// round reads, so trace[d][k+d+1] is v[off+k].
	var trace [][]int
	kept := 0
	for d := 0; ; d++ {
		if kept += 2*d + 3; kept > traceBudget {
			return nil, false
		}
		trace = append(trace, slices.Clone(v[off-d-1:off+d+2]))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1] // down: insertion
			} else {
				x = v[off+k-1] + 1 // right: deletion
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(a, b, trace), true
			}
		}
	}
}

func backtrack(a, b []string, trace [][]int) []op {
	x, y := len(a), len(b)
	var rev []op
	for d := len(trace) - 1; d > 0; d-- {
		v, k := trace[d], x-y
		prevK := k - 1
		if k == -d || (k != d && v[k+d] < v[k+d+2]) {
			prevK = k + 1
		}
		prevX := v[prevK+d+1]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x, y = x-1, y-1
			rev = append(rev, op{equal, a[x], x, y})
		}
		if x == prevX {
			y--
			rev = append(rev, op{insert, b[y], x, y})
		} else {
			x--
			rev = append(rev, op{del, a[x], x, y})
		}
	}
	for x > 0 {
		x, y = x-1, y-1
		rev = append(rev, op{equal, a[x], x, y})
	}
	slices.Reverse(rev)
	return rev
}

// split returns a shortest edit script from a to b with the linear-space refinement of
// Myers' algorithm: it looks for the middle snake of a shortest path from both ends at
// once and recurses into the two halves, so it only ever holds the two furthest-reaching
// vectors, O(len(a)+len(b)) ints.
func split(a, b []string) []op {
	e := &edit{a: a, b: b, off: (len(a)+len(b))/2 + 2, ops: make([]op, 0, len(a)+len(b))}
	e.ia, e.ib = intern(a, b)
	e.vf = make([]int, 2*e.off+1)
	e.vb = make([]int, 2*e.off+1)
	e.diff(0, len(a), 0, len(b))
	normalize(a, b, e.ops)
	return e.ops
}

// edit holds one linear-space search. Every recursion level shares the two vectors, which
// is safe because a level writes a diagonal before any level reads it again.
type edit struct {
	a, b   []string
	ia, ib []int32 // a and b as line ids, so the inner loops compare ints
	vf, vb []int   // furthest x per diagonal, forwards and backwards; vf[off+k] is diagonal k
	off    int
	ops    []op
}

// intern numbers the distinct lines of a. A line of b that does not occur in a never
// equals a line of a, so all of those share one id that matches nothing.
func intern(a, b []string) (ia, ib []int32) {
	ids := make(map[string]int32, len(a))
	buf := make([]int32, len(a)+len(b))
	ia, ib = buf[:len(a):len(a)], buf[len(a):]
	for i, l := range a {
		id, ok := ids[l]
		if !ok {
			id = int32(len(ids))
			ids[l] = id
		}
		ia[i] = id
	}
	for i, l := range b {
		id, ok := ids[l]
		if !ok {
			id = -1
		}
		ib[i] = id
	}
	return ia, ib
}

// diff appends the edit script from a[a0:a1] to b[b0:b1].
func (e *edit) diff(a0, a1, b0, b1 int) {
	for a0 < a1 && b0 < b1 && e.ia[a0] == e.ib[b0] {
		e.ops = append(e.ops, op{equal, e.a[a0], a0, b0})
		a0, b0 = a0+1, b0+1
	}
	suf := 0
	for a1-suf > a0 && b1-suf > b0 && e.ia[a1-1-suf] == e.ib[b1-1-suf] {
		suf++
	}
	a1, b1 = a1-suf, b1-suf
	switch {
	case a0 == a1:
		for y := b0; y < b1; y++ {
			e.ops = append(e.ops, op{insert, e.b[y], a0, y})
		}
	case b0 == b1:
		for x := a0; x < a1; x++ {
			e.ops = append(e.ops, op{del, e.a[x], x, b0})
		}
	default:
		// The middle snake lies on a shortest path, so both halves are smaller problems.
		x0, y0, x1, y1 := e.middleSnake(a0, a1, b0, b1)
		e.diff(a0, x0, b0, y0)
		for ; x0 < x1; x0, y0 = x0+1, y0+1 {
			e.ops = append(e.ops, op{equal, e.a[x0], x0, y0})
		}
		e.diff(x1, a1, y1, b1)
	}
	for i := range suf {
		e.ops = append(e.ops, op{equal, e.a[a1+i], a1 + i, b1 + i})
	}
}

// middleSnake searches forwards from (a0,b0) and backwards from (a1,b1) until the two meet
// and returns the ends of the snake they meet on. It needs a[a0] != b[b0] and a[a1-1] !=
// b[b1-1], which make the shortest path at least two edits long and both halves therefore
// smaller than the problem itself.
func (e *edit) middleSnake(a0, a1, b0, b1 int) (int, int, int, int) {
	n, m := a1-a0, b1-b0
	delta := n - m
	odd := delta%2 != 0
	off := e.off
	e.vf[off+1], e.vb[off+1] = 0, 0
	for d := 0; ; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && e.vf[off+k-1] < e.vf[off+k+1]) {
				x = e.vf[off+k+1] // down: insertion
			} else {
				x = e.vf[off+k-1] + 1 // right: deletion
			}
			y := x - k
			sx, sy := x, y
			for x < n && y < m && e.ia[a0+x] == e.ib[b0+y] {
				x, y = x+1, y+1
			}
			e.vf[off+k] = x
			// With delta odd the two searches can only meet on a forward step, on a
			// diagonal the backward search reached in its previous round.
			if odd && k-delta > -d && k-delta < d && n-e.vb[off+delta-k] <= x {
				return a0 + sx, b0 + sy, a0 + x, b0 + y
			}
		}
		// The backward search runs on the reversed input, where its diagonal k is the
		// forward search's diagonal delta-k.
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && e.vb[off+k-1] < e.vb[off+k+1]) {
				x = e.vb[off+k+1]
			} else {
				x = e.vb[off+k-1] + 1
			}
			y := x - k
			sx, sy := x, y
			for x < n && y < m && e.ia[a1-1-x] == e.ib[b1-1-y] {
				x, y = x+1, y+1
			}
			e.vb[off+k] = x
			if !odd && delta-k >= -d && delta-k <= d && n-x <= e.vf[off+delta-k] {
				return a1 - x, b1 - y, a1 - sx, b1 - sy
			}
		}
	}
}

// normalize rewrites a script the way the greedy search writes it: deletions before
// insertions, and every match on the earliest line that can carry it. Which of the equally
// short scripts a search returns is otherwise an artifact of where it split the input, and
// the two searches would render the same change differently.
func normalize(a, b []string, ops []op) {
	for i := 0; i < len(ops); {
		if ops[i].kind == equal {
			i++
			continue
		}
		x, y := 0, 0 // first old and new line of the change
		if i > 0 {
			x, y = ops[i-1].oldIdx+1, ops[i-1].newIdx+1
		}
		p, q, j := 0, 0, i
		for ; j < len(ops) && ops[j].kind != equal; j++ {
			if ops[j].kind == del {
				p++
			} else {
				q++
			}
		}
		// The match after the change moves in front of a deleted or inserted line when
		// that line repeats it, which leaves the script just as short.
		ox, oy := x+p, y+q // the matched pair before moving it
		mx, my := ox, oy
		if j < len(ops) {
			for my > y && b[my-1] == b[my] {
				my--
			}
			if my == y {
				for mx > x && a[mx-1] == a[mx] {
					mx--
				}
			}
		}
		at := i
		for t := x; t < mx; t++ {
			ops[at] = op{del, a[t], t, y}
			at++
		}
		for t := y; t < my; t++ {
			ops[at] = op{insert, b[t], mx, t}
			at++
		}
		if j == len(ops) {
			i = at
			continue
		}
		ops[at] = op{equal, a[mx], mx, my}
		at++
		i = at
		for t := mx + 1; t <= ox; t++ {
			ops[at] = op{del, a[t], t, my + 1}
			at++
		}
		for t := my + 1; t <= oy; t++ {
			ops[at] = op{insert, b[t], ox + 1, t}
			at++
		}
	}
}
