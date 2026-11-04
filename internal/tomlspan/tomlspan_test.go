package tomlspan

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const doc = `# comment
title = "TOML" # trailing
"quoted key" = 'literal \n'
a.b.c = 1
[project]
name = "x"
dependencies = [
  "requests>=2.31.0",  # why
  'click==8.1.7',
  "esc\"aped",
  1,
]
dt = 1979-05-27 07:32:00Z
inline = { version = "^1.0", extras = ["a", "b",], nested = { x = true } }

[tool.poetry.dependencies]
python = "^3.9"
requests = { version = "^2.31" }

[[tool.poetry.source]]
name = "one"
[[tool.poetry.source]]
name = "two"
[tool.poetry.source.sub]
k = "v"

[ dotted . "quoted.part" ]
ml = """
first
second"""
mll = '''
raw \n'''
cont = """a \
       b"""
quotes = """x"" """
q2 = """x"""""
empty = ""
unicode = "\u00e9\U0001F600\x41\e\b\t\n\f\r\\"
`

func mustParse(t *testing.T, src string) *Value {
	t.Helper()
	v, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return v
}

func TestParse(t *testing.T) {
	root := mustParse(t, doc)
	if !slices.Equal(root.Keys, []string{"title", "quoted key", "a", "project", "tool", "dotted"}) {
		t.Errorf("root keys = %v", root.Keys)
	}
	str := func(path ...string) *Value {
		t.Helper()
		v := root.Get(path...)
		if v == nil {
			t.Fatalf("%v missing", path)
		}
		return v
	}
	for _, c := range []struct {
		path  []string
		kind  Kind
		str   string
		exact bool
		line  int
	}{
		{[]string{"title"}, String, "TOML", true, 2},
		{[]string{"quoted key"}, String, `literal \n`, true, 3},
		{[]string{"a", "b", "c"}, Scalar, "1", true, 4},
		{[]string{"project", "name"}, String, "x", true, 6},
		{[]string{"project", "dt"}, Scalar, "1979-05-27 07:32:00Z", true, 13},
		{[]string{"project", "inline", "version"}, String, "^1.0", true, 14},
		{[]string{"project", "inline", "nested", "x"}, Scalar, "true", true, 14},
		{[]string{"tool", "poetry", "dependencies", "python"}, String, "^3.9", true, 17},
		{[]string{"tool", "poetry", "dependencies", "requests", "version"}, String, "^2.31", true, 18},
		{[]string{"dotted", "quoted.part", "ml"}, String, "first\nsecond", true, 28},
		{[]string{"dotted", "quoted.part", "mll"}, String, `raw \n`, true, 31},
		{[]string{"dotted", "quoted.part", "cont"}, String, "a b", false, 33},
		{[]string{"dotted", "quoted.part", "quotes"}, String, `x"" `, true, 35},
		{[]string{"dotted", "quoted.part", "q2"}, String, `x""`, true, 36},
		{[]string{"dotted", "quoted.part", "empty"}, String, "", true, 37},
		{[]string{"dotted", "quoted.part", "unicode"}, String, "é😀A\x1b\b\t\n\f\r\\", false, 38},
	} {
		v := str(c.path...)
		if v.Kind != c.kind || v.Str != c.str || v.Exact != c.exact || v.Line != c.line {
			t.Errorf("%v = {%d %q exact=%v line=%d}, want {%d %q %v %d}", c.path, v.Kind, v.Str, v.Exact, v.Line, c.kind, c.str, c.exact, c.line)
		}
		if v.Exact && doc[v.Start:v.End] != v.Str {
			t.Errorf("%v: span %q != %q", c.path, doc[v.Start:v.End], v.Str)
		}
	}
	deps := str("project", "dependencies")
	if deps.Kind != Array || len(deps.Items) != 4 {
		t.Fatalf("dependencies = %+v", deps)
	}
	var got []string
	for _, s := range deps.Strings() {
		got = append(got, s.Str)
	}
	if !slices.Equal(got, []string{"requests>=2.31.0", "click==8.1.7", `esc"aped`}) {
		t.Errorf("Strings = %q", got)
	}
	if deps.Items[2].Exact || deps.Items[0].Line != 8 || deps.Items[1].Line != 9 {
		t.Errorf("items = %+v", deps.Items)
	}
	if extras := str("project", "inline", "extras").Strings(); len(extras) != 2 {
		t.Errorf("extras = %v", extras)
	}
	src := str("tool", "poetry", "source")
	if src.Kind != Array || len(src.Items) != 2 || src.Items[1].Get("name").Str != "two" || src.Items[1].Get("sub", "k").Str != "v" {
		t.Errorf("source = %+v", src)
	}
	if root.Get("project", "name", "x") != nil || root.Get("nope") != nil || (*Value)(nil).Get("x") != nil {
		t.Error("Get on non-table")
	}
	if (*Value)(nil).Strings() != nil || str("title").Strings() != nil {
		t.Error("Strings on non-array")
	}
}

func TestParseCRLF(t *testing.T) {
	src := "[a]\r\nb = \"1\"\r\nc = \"\"\"\r\nx\r\ny\"\"\"\r\nd = 2\r\n"
	root := mustParse(t, src)
	if b := root.Get("a", "b"); b.Str != "1" || b.Line != 2 {
		t.Errorf("b = %+v", b)
	}
	if c := root.Get("a", "c"); c.Str != "x\r\ny" || c.Line != 3 {
		t.Errorf("c = %+v", c)
	}
	if d := root.Get("a", "d"); d.Line != 6 {
		t.Errorf("d line = %d", d.Line)
	}
}

func TestParseImplicitTables(t *testing.T) {
	root := mustParse(t, "[a.b]\nx = 1\n[a]\ny = 2\n[[c]]\n[c.d]\nz = 3\n[[c]]\n")
	if root.Get("a", "b", "x") == nil || root.Get("a", "y") == nil {
		t.Error("implicit table a")
	}
	c := root.Get("c")
	if len(c.Items) != 2 || c.Items[0].Get("d", "z") == nil {
		t.Errorf("c = %+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"a = 1\na = 2\n", "line 2: duplicate key a"},
		{"[a]\n[a]\n", "table a defined twice"},
		{"a = 1\n[a]\n", "a is not a table"},
		{"a = 1\n[a.b]\n", "key a is not a table"},
		{"a = 1\na.b = 2\n", "key a is not a table"},
		{"a = {x = 1}\n[a]\n", "a is not a table"},
		{"[a]\n[[a]]\n", "a is not an array of tables"},
		{"[a\n", "unterminated table header"},
		{"[[a]\n", "unterminated table header"},
		{"a = \"x\n", "newline in string"},
		{"a = \"x", "unterminated string"},
		{"a = \"\"\"x", "unterminated string"},
		{"a = \"\\q\"", `invalid escape \q`},
		{"a = \"\\u12\"", `short \u escape`},
		{"a = \"\\uZZZZ\"", `invalid \u escape`},
		{"a = \"\\", "unterminated string"},
		{"a = \"x \\\n\"", "invalid escape"},
		{"a = \"\"\"x \\ y\"\"\"", "invalid escape"},
		{"a = \"\"\"x\ry\"\"\"", "bare carriage return in string"},
		{"a = [1, 2", "unterminated array"},
		{"a = [1 2]", `invalid value "1 2"`}, {"a = [\"x\" 1]", "expected , or ] in array"},
		{"a = [", "unterminated array"},
		{"a = [1", "unterminated array"},
		{"a = {x = 1 y = 2}", `invalid value "1 y = 2"`}, {"a = {x = \"1\" y = 2}", "expected , or } in inline table"},
		{"a = {x = 1", "unterminated inline table"},
		{"a = 1 2\n", `invalid value "1 2"`},
		{"a = \n", "expected value"},
		{"a =", "expected value"},
		{"a = ]", "expected value"},
		{"a 1\n", "expected = after key a"},
		{"= 1\n", "invalid key character '='"},
		{"a. = 1\n", "invalid key character '='"},
		{"\"\"\"a\"\"\" = 1\n", "multi-line string as key"},
		{"a = 1 b = 2\n", `invalid value "1 b = 2"`},
		{"a = \"x\" b\n", "expected end of line, found 'b'"},
		{"[a] x\n", "expected end of line"},
		{"a.", "expected key"},
	} {
		_, err := Parse([]byte(c.src))
		var e *Error
		if err == nil || !errors.As(err, &e) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%q) = %v, want %q", c.src, err, c.want)
		}
	}
}
