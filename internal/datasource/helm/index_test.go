package helmds

import (
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func scanVersions(t *testing.T, src string, wanted ...string) map[string][]string {
	t.Helper()
	w := map[string]bool{}
	for _, n := range wanted {
		w[n] = true
	}
	got, err := scanIndex(strings.NewReader(src), w)
	if err != nil {
		t.Fatalf("scanIndex: %v", err)
	}
	out := map[string][]string{}
	for name, rels := range got {
		for _, r := range rels {
			out[name] = append(out[name], r.Version)
		}
	}
	return out
}

func TestScanIndex(t *testing.T) {
	got := scanVersions(t, testIndex, "cert-manager", "quoted", "absent")
	if len(got) != 2 || !slices.Equal(got["cert-manager"], []string{"v1.15.0", "v1.14.0"}) || !slices.Equal(got["quoted"], []string{"2.0.0"}) {
		t.Errorf("got %v", got)
	}
	// Indented version lists, CRLF, single quotes, and a chart at the end of input.
	src := "entries:\r\n  a:\r\n    - version: 1.0.0\r\n      urls: [x]\r\n  'b':\r\n    - version: 2.0.0\r\n      urls: [x]"
	if got := scanVersions(t, src, "a", "b"); !slices.Equal(got["a"], []string{"1.0.0"}) || !slices.Equal(got["b"], []string{"2.0.0"}) {
		t.Errorf("indented: %v", got)
	}
	// Lines longer than the read buffer.
	long := strings.Repeat("x", 200<<10)
	src = "entries:\n  a:\n  - description: " + long + "\n    version: 1.0.0\n    urls: [x]\n  b:\n  - description: " + long + "\n    version: 3.0.0\n    urls: [x]\n"
	if got := scanVersions(t, src, "b"); !slices.Equal(got["b"], []string{"3.0.0"}) || len(got) != 1 {
		t.Errorf("long lines: %v", got)
	}
	// JSON indexes are decoded whole.
	src = `  {"apiVersion": "v1", "entries": {"a": [{"version": "1.0.0", "urls": ["x"]}], "b": [{"version": "2.0.0", "urls": ["x"]}]}}`
	if got := scanVersions(t, src, "a"); !slices.Equal(got["a"], []string{"1.0.0"}) || len(got) != 1 {
		t.Errorf("json: %v", got)
	}
	for _, src := range []string{"", "\n\n", "apiVersion: v1\nentries: {}\n", "apiVersion: v1\n"} {
		if got := scanVersions(t, src, "a"); len(got) != 0 {
			t.Errorf("%q: %v", src, got)
		}
	}
	// One byte at a time exercises the buffered reads.
	if got, err := scanIndex(iotest.OneByteReader(strings.NewReader(testIndex)), map[string]bool{"quoted": true}); err != nil || len(got["quoted"]) != 1 {
		t.Errorf("one byte reader: %v, %v", got, err)
	}
}

func TestScanIndexErrors(t *testing.T) {
	for name, src := range map[string]string{
		"flow entries":  "entries: {a: []}\n",
		"sequence":      "entries:\n  - a\n",
		"misindented":   "entries:\n    a:\n    - version: 1.0.0\n  b:\n",
		"broken chart":  "entries:\n  a:\n  - version: [\n",
		"broken json":   "{\"entries\": [",
		"reader errors": "",
	} {
		r := strings.NewReader(src)
		var err error
		if name == "reader errors" {
			_, err = scanIndex(iotest.ErrReader(iotest.ErrTimeout), map[string]bool{"a": true})
		} else {
			_, err = scanIndex(r, map[string]bool{"a": true})
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := scanIndex(iotest.TimeoutReader(strings.NewReader("entries:\n  a:\n")), map[string]bool{"a": true}); err == nil {
		t.Error("read error mid-stream: no error")
	}
}
