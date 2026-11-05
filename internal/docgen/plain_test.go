package docgen

import (
	"strings"
	"testing"
)

func TestMarkdownToPlain(t *testing.T) {
	for _, tt := range []struct {
		in    string
		width int
		want  string
	}{
		{"Use `pin: digest` and **bold**, *as written*.", 100, "Use pin: digest and bold, as written."},
		{"Globs \\*\\*/x and \\<name\\>, ``a ` b``, a ` # <tag>` suffix", 100, "Globs **/x and <name>, a ` b, a ' # <tag>' suffix"},
		{"See [exit codes](cli.md#exit-codes) and [OSV](https://osv.dev).", 100, "See exit codes and OSV."},
		{"5 * 3 stays, `unbalanced", 100, "5 * 3 stays, `unbalanced"},
		{"```yaml\npin: digest\n\n```\nafter", 100, "    pin: digest\n\nafter"},
		{"| a | b |\n|---|:-:|\n| `x` | y |", 5, "x\n    y"},
		{"- one two three four five six", 18, "- one two three\n  four five six"},
		{"* one two three four five six", 18, "* one two three\n  four five six"},
		{"  1. one two three four five", 18, "  1. one two three\n     four five"},
		{"plain words wrap → here and there", 18, "plain words wrap →\nhere and there"}, // runes, not bytes
		{"## Heading\nline one\nline two\n- item\n  continued", 100, "Heading\nline one line two\n- item continued"},
		{"Intro with `code` and **bold** and a [link](https://x.test).\n" +
			"Continued line.\n\n" +
			"| Name | Used by | Meaning |\n|---|---|---|\n" +
			"| `A_VAR` | `run` | one `a\\|b` and `x|y` value that is long enough to wrap onto a second line of output text |\n\n" +
			"```\ndeps plan\n```\n\n" +
			"- a list item that is long enough to wrap onto a second line of the output text, too\n", 80,
			"Intro with code and bold and a link. Continued line.\n" +
				"\n" +
				"A_VAR\n" +
				"    [run] one a|b and x|y value that is long enough to wrap onto a second line\n" +
				"    of output text\n" +
				"\n" +
				"    deps plan\n" +
				"\n" +
				"- a list item that is long enough to wrap onto a second line of the output text,\n" +
				"  too"},
	} {
		if got := MarkdownToPlain(tt.in, tt.width); got != tt.want {
			t.Errorf("MarkdownToPlain(%q)\n got %q\nwant %q", tt.in, got, tt.want)
		}
	}
}

func TestConfigKeysAndFacts(t *testing.T) {
	s := reflectFake(t)
	keys, err := ConfigKeys(s)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	byPath := map[string]ConfigKey{}
	for _, k := range keys {
		paths = append(paths, k.Path)
		byPath[k.Path] = k
	}
	want := "mode limit paths inner inner.host items items[].files items[].kind items[].check items[].check.url sources sources.<name>.url"
	if got := strings.Join(paths, " "); got != want {
		t.Fatalf("paths = %s, want %s", got, want)
	}
	limit := byPath["limit"]
	if limit.Summary() != "Maximum count; 0 is unlimited." || limit.Details() != "Details about `limit`.\n\n```yaml\nlimit: 3\n```" {
		t.Errorf("limit summary %q, details %q", limit.Summary(), limit.Details())
	}
	if byPath["items[].kind"].Depth != 1 || byPath["items[].check.url"].Depth != 2 {
		t.Errorf("depths: %+v", keys)
	}
	if got := Facts(s, byPath["mode"]); len(got) != 3 || got[0] != (Fact{"Type", "string"}) || got[1] != (Fact{"Default", "`fast`"}) || got[2] != (Fact{"Values", "`fast`, `slow`"}) {
		t.Errorf("mode facts = %+v", got)
	}
}
