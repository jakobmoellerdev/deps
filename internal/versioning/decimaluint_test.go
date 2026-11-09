package versioning

import (
	"strconv"
	"testing"
)

// TestDecimalUint pins decimalUint to strconv.ParseUint(s, 10, 64), whose error the
// prerelease comparators used to tell numeric identifiers from alphanumeric ones.
func TestDecimalUint(t *testing.T) {
	for _, s := range []string{"", "0", "007", "42", "canary", "rc1", "1a", "-1", "+1", "1_0", "18446744073709551615", "18446744073709551616"} {
		want, err := strconv.ParseUint(s, 10, 64)
		got, ok := decimalUint(s)
		if ok != (err == nil) || ok && got != want {
			t.Errorf("decimalUint(%q) = %d, %v; ParseUint = %d, %v", s, got, ok, want, err)
		}
	}
}

func TestDecimalUintNoAlloc(t *testing.T) {
	if n := testing.AllocsPerRun(100, func() { compareSemverPrePart("canary", "rc") }); n != 0 {
		t.Errorf("compareSemverPrePart allocates %v times", n)
	}
}
