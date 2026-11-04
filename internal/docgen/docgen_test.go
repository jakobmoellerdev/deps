package docgen

import (
	"strings"
	"testing"
)

func TestAnchor(t *testing.T) {
	for in, want := range map[string]string{
		"`rules[].allow`":                  "rulesallow",
		"`customDatasources.<name>.url`":   "customdatasourcesnameurl",
		"deps run":                         "deps-run",
		"Exit codes":                       "exit-codes",
		"Skip reasons of every manager":    "skip-reasons-of-every-manager",
		"`custom[].checksum.file`":         "customchecksumfile",
		"Security advisories (OSV)":        "security-advisories-osv",
		"`separate_major` x":               "separate_major-x",
		"Ünïcode heading":                  "ünïcode-heading",
		"`actions.defaultHost` (embedded)": "actionsdefaulthost-embedded",
	} {
		if got := Anchor(in); got != want {
			t.Errorf("Anchor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlainToMarkdown(t *testing.T) {
	for in, want := range map[string]string{
		"as 'Name <email>' (git identity)":         "as `Name <email>` (git identity)",
		"e.g. '3 days', '12h' or '0'.":             "e.g. `3 days`, `12h` or `0`.",
		"('*' stays, '**' crosses)":                "(`*` stays, `**` crosses)",
		"the bot's own and the manager's":          "the bot's own and the manager's",
		"custom.<name> of customDatasources":       `custom.\<name> of customDatasources`,
		"globs like **/x":                          `globs like \*\*/x`,
		"keeps `code <spans>` and 'q'":             "keeps `code <spans>` and `q`",
		"'a' 'b'":                                  "`a` `b`",
		"regex '/^\\d+$/'; '!/<RE2>/' rejects":     "regex `/^\\d+$/`; `!/<RE2>/` rejects",
		"[x 'y']":                                  "[x `y`]",
		"empty means 'it''s' unchanged":            "empty means 'it''s' unchanged",
		"no quotes, no specials":                   "no quotes, no specials",
		"(default: <dir>/.deps.yaml)":              `(default: \<dir>/.deps.yaml)`,
		"an 'unterminated quote stays":             "an 'unterminated quote stays",
		"inner '`tick`' quote":                     "inner '`tick`' quote",
		"pattern '^[a-z]*$' end":                   "pattern `^[a-z]*$` end",
		"quote at end 'x'":                         "quote at end `x`",
		"'x' at start":                             "`x` at start",
		"a value with a backtick inside 'a`b' end": "a value with a backtick inside 'a`b' end",
	} {
		if got := PlainToMarkdown(in); got != want {
			t.Errorf("PlainToMarkdown(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestCellAndCodeSpan(t *testing.T) {
	if got := cell(" a | b \\| c\nd "); got != `a \| b \| c d` {
		t.Errorf("cell = %q", got)
	}
	if got := codeSpan("a`b"); got != "`` a`b ``" {
		t.Errorf("codeSpan = %q", got)
	}
}

func TestDuplicateHeadingIsAnError(t *testing.T) {
	d := newDoc("T")
	d.heading(2, "`a.b`")
	d.heading(2, "`ab`")
	if _, err := d.bytes(); err == nil || !strings.Contains(err.Error(), "#ab") {
		t.Errorf("err = %v, want duplicate anchor #ab", err)
	}
}
