package cargo

import (
	"fmt"
	"strings"
	"testing"
)

// render describes the items of src, one per line: kind, key and value (strings with their
// span content, or "-" when the span is zero).
func render(t *testing.T, src string) string {
	t.Helper()
	var b strings.Builder
	err := scanTOML([]byte(src), func(it item) {
		fmt.Fprintf(&b, "%c %s", it.kind, strings.Join(it.key, "|"))
		switch it.kind {
		case 's':
			span := "-"
			if it.span.End > 0 {
				span = src[it.span.Start:it.span.End]
			}
			fmt.Fprintf(&b, " %q %q", it.str, span)
		case 'a':
			fmt.Fprintf(&b, " %q", it.strs)
		case 'v':
			fmt.Fprintf(&b, " %s", it.str)
		}
		b.WriteByte('\n')
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return b.String()
}

func TestScanTOML(t *testing.T) {
	src := "# comment\r\n" +
		"top = 1 # trailing\r\n" +
		"[a . \"b.c\" . 'd']\n" +
		"s = \"x\\ty\" \n" +
		"lit = 'C:\\path'\n" +
		"ml = \"\"\"\nline \\\"\"\" \n\"\"\"\n" +
		"mll = '''\nraw ''''\n" +
		"arr = [ \"one\", 2, [\"nested\"], { k = \"in array\" }, 'two', ] # done\n" +
		"multi = [\n  \"x\", # c\n  \"y\"\n]\n" +
		"tbl = { v = \"1\", n.d = true, deep = { e = \"2\" }, list = [\"z\"] }\n" +
		"wrapped = {\n  v = \"3\", # TOML 1.1\n}\n" +
		"u = \"\\u00e9\\U0001F600\\b\\n\\f\\r\\e\\\"\\\\\"\n" +
		"[[bin]]\nname = \"rg\"\n" +
		"date = 1979-05-27T07:32:00Z\n"
	want := "v top 1\n" +
		"h a|b.c|d\n" +
		"s a|b.c|d|s \"x\\ty\" \"-\"\n" +
		"s a|b.c|d|lit \"C:\\\\path\" \"C:\\\\path\"\n" +
		"s a|b.c|d|ml \"\\nline \\\\\\\"\\\"\\\" \\n\" \"-\"\n" +
		"s a|b.c|d|mll \"\\nraw '\" \"-\"\n" +
		"a a|b.c|d|arr [\"one\" \"two\"]\n" +
		"a a|b.c|d|multi [\"x\" \"y\"]\n" +
		"s a|b.c|d|tbl|v \"1\" \"1\"\n" +
		"v a|b.c|d|tbl|n|d true\n" +
		"s a|b.c|d|tbl|deep|e \"2\" \"2\"\n" +
		"a a|b.c|d|tbl|list [\"z\"]\n" +
		"s a|b.c|d|wrapped|v \"3\" \"3\"\n" +
		"s a|b.c|d|u \"é😀\\b\\n\\f\\r\\x1b\\\"\\\\\" \"-\"\n" +
		"H bin\n" +
		"s bin|name \"rg\" \"rg\"\n" +
		"v bin|date 1979-05-27T07:32:00Z\n"
	if got := render(t, src); got != want {
		t.Errorf("items:\n%s\nwant:\n%s", got, want)
	}
}

func TestScanTOMLErrors(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"a = \"x\nb = 1\n", "line 1: unterminated string"},
		{"a = \"x", "line 1: unterminated string"},
		{"a = 'x", "line 1: unterminated string"},
		{"\n\na = \"\\q\"\n", "line 3: invalid escape \\q"},
		{"a = \"\\u12\"", "line 1: short unicode escape"},
		{"a = \"\\uZZZZ\"", "line 1: invalid unicode escape"},
		{"a = \"\\", "line 1: unterminated escape"},
		{"a = \"\"\"never closed", "line 1: unterminated multi-line string"},
		{"[a\n", "line 1: unterminated table header"},
		{"[[a]\n", "line 1: unterminated table header"},
		{"[]\n", "line 1: expected a key"},
		{"a\n", "line 1: expected '=' after key"},
		{"a =\n", "line 1: expected a value"},
		{"a = 1 b = 2\n", "line 1: unexpected 'b'"},
		{"[a] x\n", "line 1: unexpected 'x'"},
		{"a = [1 2]\n", "line 1: expected ',' or ']' in array"},
		{"a = [\"x\n", "line 1: unterminated string"},
		{"a = {b = 1 c = 2}\n", "line 1: expected ',' or '}' in inline table"},
		{"a = {b}\n", "line 1: expected '=' after key"},
		{"\"\"\"k\"\"\" = 1\n", "line 1: multi-line key"},
		{"'k = 1\n", "line 1: unterminated string"},
	} {
		err := scanTOML([]byte(tc.src), func(item) {})
		if err == nil || err.Error() != tc.want {
			t.Errorf("%q: err = %v, want %s", tc.src, err, tc.want)
		}
	}
}
