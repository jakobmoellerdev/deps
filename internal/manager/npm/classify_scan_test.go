package npm

import (
	"regexp"
	"testing"
)

// classifyOracleRe is the regexp scanRange replaced; it is the oracle for parity.
var classifyOracleRe = regexp.MustCompile(`^(\^|~|>=|=)?(\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)$`)

var classifyScanSeeds = []string{
	"", "1.2.3", "^1.2.3", "~1.2.3", ">=1.2.3", "=1.2.3", "^0.0.0", "10.20.30",
	"1.2.3-beta", "1.2.3-beta.1", "1.2.3--", "1.2.3-a-b.c-d.0", "^1.2.3-rc.1",
	"1.2", "1", "1.2.3.4", "1.2.3-", "1.2.3-a.", "1.2.3-a..b", "1.2.3+build",
	"1.2.3-a+b", ">1.2.3", "<=1.2.3", "^^1.2.3", "~>1.2.3", ">= 1.2.3", " 1.2.3",
	"1.2.3 ", "1.2.3\n", "v1.2.3", "1.x", "*", "latest", "^1.2.3 || ^2.0.0",
	"1.2.3 - 2.0.0", "1..3", ".1.2", "1.2.a", "1.2.3-é", "١.٢.٣", "=", ">=", "^",
}

func classifyScanCheck(t *testing.T, v string) {
	t.Helper()
	m := classifyOracleRe.FindStringSubmatchIndex(v)
	off, ok := scanRange(v)
	if ok != (m != nil) {
		t.Fatalf("scanRange(%q) ok = %v, oracle %v", v, ok, m != nil)
	}
	if ok && (off != m[4] || m[5] != len(v)) {
		t.Fatalf("scanRange(%q) off = %d, oracle [%d:%d]", v, off, m[4], m[5])
	}
}

func TestClassifyScanMatchesRegexp(t *testing.T) {
	for _, v := range classifyScanSeeds {
		classifyScanCheck(t, v)
	}
}

func FuzzClassifyScan(f *testing.F) {
	for _, v := range classifyScanSeeds {
		f.Add(v)
	}
	f.Fuzz(classifyScanCheck)
}
