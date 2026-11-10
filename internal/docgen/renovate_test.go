package docgen

import "testing"

func TestRenovateOptions(t *testing.T) {
	b, err := RenovateOptions("Intro.", []RenovateRow{{"`groupName`", "`rules[].group`"}, {"`a|b`", "`x \\| y`"}})
	if err != nil {
		t.Fatal(err)
	}
	want := Header + "\n# Renovate options\n\nIntro.\n\n| Renovate | deps |\n|---|---|\n" +
		"| `groupName` | `rules[].group` |\n| `a\\|b` | `x \\| y` |\n"
	if got := string(b); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
