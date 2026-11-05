package pyreq

import (
	"strings"
	"testing"
)

// normalizeOracle is the builder-only Normalize before the already-normalized fast path.
func normalizeOracle(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	sep := false
	for i := range len(name) {
		c := name[i]
		switch {
		case c == '-' || c == '_' || c == '.':
			sep = true
			continue
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
		}
		if sep && b.Len() > 0 {
			b.WriteByte('-')
		}
		sep = false
		b.WriteByte(c)
	}
	return b.String()
}

func FuzzNormalizeOracle(f *testing.F) {
	for _, s := range []string{"", "-", "a-b", "a--b", "-a", "a-", "A_b.C", "typing-extensions", "x\xff-"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := Normalize(s), normalizeOracle(s); got != want {
			t.Fatalf("Normalize(%q) = %q, oracle %q", s, got, want)
		}
	})
}

func BenchmarkNormalizeFastPath(b *testing.B) {
	names := []string{"requests", "typing-extensions", "django", "zope-interface", "Django", "typing_extensions"}
	b.ReportAllocs()
	for b.Loop() {
		for _, n := range names {
			_ = Normalize(n)
		}
	}
}
