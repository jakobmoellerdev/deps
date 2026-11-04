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
func myers(a, b []string) []op {
	n, m := len(a), len(b)
	off := n + m + 1
	v := make([]int, 2*off+1) // v[off+k]: furthest x on diagonal k
	var trace [][]int         // v before each round d
	for d := 0; ; d++ {
		trace = append(trace, slices.Clone(v))
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
				return backtrack(a, b, trace, off)
			}
		}
	}
}

func backtrack(a, b []string, trace [][]int, off int) []op {
	x, y := len(a), len(b)
	var rev []op
	for d := len(trace) - 1; d > 0; d-- {
		v, k := trace[d], x-y
		prevK := k - 1
		if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
			prevK = k + 1
		}
		prevX := v[off+prevK]
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
