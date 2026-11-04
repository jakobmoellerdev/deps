package properties

import (
	"maps"
	"testing"
)

func TestParse(t *testing.T) {
	src := "# comment\n! bang\n  a=1\nb = two  \r\nc: x\\:y\nd value\ne\n\\ k=v\na=last\n"
	props := Parse([]byte(src))
	for key, want := range map[string]struct {
		value string
		line  int
	}{
		"a": {"last", 9}, "b": {"two", 4}, "c": {`x\:y`, 5}, "d": {"value", 6}, "e": {"", 7}, `\ k`: {"v", 8},
	} {
		p, ok := props[key]
		if !ok || p.Value != want.value || p.Line != want.line || src[p.Start:p.End] != want.value {
			t.Errorf("%q = %+v, want %+v", key, p, want)
		}
	}
	if len(props) != 6 {
		t.Errorf("props = %v", props)
	}
	got := Values([]byte(src))
	// The escaped `\ k` key unescapes to a leading space.
	if escaped := Unescape(`\ k`); got[escaped] != "v" {
		t.Errorf("Values[%q] = %q", escaped, got[escaped])
	}
	delete(got, Unescape(`\ k`))
	if !maps.Equal(got, map[string]string{"a": "last", "b": "two", "c": "x:y", "d": "value", "e": ""}) {
		t.Errorf("Values = %v", got)
	}
	if got := Unescape(`https\://x\\y\`); got != `https://x\y\` {
		t.Errorf("Unescape = %q", got)
	}
}
