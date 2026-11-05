package gradle

import (
	"regexp"
	"slices"
	"testing"
)

// lineForms pairs each regexp run by inLines with the candidate lines it is scanned on.
var lineForms = []struct {
	name  string
	re    *regexp.Regexp
	lines func(code []byte) []int
}{
	{"declRe", declRe, quotedLines},
	{"extAssignRe", extAssignRe, quotedLines},
	{"byExtraRe", byExtraRe, func(c []byte) []int { return litLines(c, "extra(") }},
	{"extIndexRe", extIndexRe, quotedLines},
	{"extSetRe", extSetRe, func(c []byte) []int { return litLines(c, "set(") }},
	{"blockAssignRe", blockAssignRe, quotedLines},
	{"mapDeclRe", mapDeclRe, func(c []byte) []int { return valueLines(c, listValue) }},
	{"kotlinMapRe", kotlinMapRe, func(c []byte) []int { return valueLines(c, mapOfValue) }},
	{"mapAssignRe", mapAssignRe, quotedLines},
	{"coordRe", coordRe, func(c []byte) []int { return wordLines(c, configWords) }},
	{"mapRe", mapRe, func(c []byte) []int { return wordLines(c, configWords) }},
	{"pluginRe", pluginRe, func(c []byte) []int { return litLines(c, "version") }},
}

func quotedLines(c []byte) []int { return valueLines(c, quotedValue) }

// checkInLines reports where scanning only the candidate lines of code differs from
// scanning all of it.
func checkInLines(t *testing.T, code []byte) {
	t.Helper()
	for _, f := range lineForms {
		want := f.re.FindAllSubmatchIndex(code, -1)
		got := slices.Collect(inLines(f.re, code, f.lines(code)))
		if !slices.EqualFunc(got, want, slices.Equal[[]int]) {
			t.Errorf("%s on %q:\n got %v\nwant %v", f.name, code, got, want)
		}
	}
}

// TestInLines checks that the candidate lines of every regexp hold all of its matches in
// the script fixtures.
func TestInLines(t *testing.T) {
	for _, f := range scriptFixtures(t) {
		t.Run(f.file, func(t *testing.T) {
			checkInLines(t, f.src)
			checkInLines(t, scan(f.src, true).code)
			checkInLines(t, scan(f.src, false).code)
		})
	}
}

func FuzzInLines(f *testing.F) {
	for _, s := range scriptFixtures(f) {
		f.Add(s.src)
	}
	for _, s := range []string{
		"ext {\n  a = '1'\n}\n", "ext {\r\n\tset('a', \"1\")\r\n}", "a ==\n'1'", "val x by\nextra(\"1\")",
		"versions = [\nk: '1']", "deps['x'] =\t[", "val m: Map<String, String> = mapOf(", "é = '1'\n\x82x = '1'",
		"def x = '1'; val y = \"2\"\nString z = '3'", "=", "\n=\n'", "x =",
		"implementationgroup: 'g'", "api'g:n:v'\nxapi 'g:n:v'", "compileOnlyApi(group = \"g\", name = \"n\")",
		"id 'x' version 'y'", "kotlin(\"jvm\").version(\"1\")", "id(\"x\")\nversion \"1\"", "implementation\n'g:n:v'",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		checkInLines(t, src)
		checkInLines(t, scan(src, true).code)
	})
}
