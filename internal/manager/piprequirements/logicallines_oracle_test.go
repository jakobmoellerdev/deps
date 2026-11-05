package piprequirements

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// logicalLinesOracle is the previous string-copying logicalLines, kept as the
// reference implementation for parity.
func logicalLinesOracle(src []byte) []line {
	var out []line
	var cur line
	var b strings.Builder
	for pos := 0; pos < len(src); {
		end := pos
		for end < len(src) && src[end] != '\n' {
			end++
		}
		next := end + 1
		raw := strings.TrimSuffix(string(src[pos:end]), "\r")
		cont := strings.HasSuffix(raw, "\\")
		raw = strings.TrimSuffix(raw, "\\")
		cur.segs = append(cur.segs, seg{at: b.Len(), file: pos})
		b.WriteString(raw)
		if !cont || next >= len(src) {
			cur.text = b.String()
			out = append(out, cur.finish())
			cur, b = line{}, strings.Builder{}
		}
		pos = next
	}
	return out
}

func checkLogicalLinesParity(t *testing.T, src []byte) {
	t.Helper()
	got, want := logicalLines(src), logicalLinesOracle(src)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("logicalLines(%q)\n got %+v\nwant %+v", src, got, want)
	}
	for _, l := range got {
		for i := range len(l.text) {
			if o := l.offset(i); o < 0 || o >= len(src) || src[o] != l.text[i] {
				t.Fatalf("logicalLines(%q): line %q offset(%d) = %d does not map to %q", src, l.text, i, o, l.text[i])
			}
		}
	}
}

var logicalLinesSeeds = []string{
	"",
	"\n",
	"\\",
	"a\\",
	"a\\\n",
	"a==1 \\\n    --hash=sha256:x \\\n    --hash=sha256:y\n",
	"a==1 \\\r\n  b # c \\\r\nd\r\n",
	"# only comment \\\nstill comment\n",
	"  x \\\n  # comment after continuation\ny==2\n",
	"a\\\\\nb\n",
	"no newline at end",
}

func TestLogicalLinesOracleParity(t *testing.T) {
	for _, s := range logicalLinesSeeds {
		checkLogicalLinesParity(t, []byte(s))
	}
	for _, dir := range []string{"testdata", "../../pytool/testdata"} {
		files, err := filepath.Glob(filepath.Join(dir, "**", "*.txt"))
		if err != nil {
			t.Fatal(err)
		}
		top, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
		for _, f := range append(top, files...) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			checkLogicalLinesParity(t, src)
		}
	}
}

func FuzzLogicalLinesOracleParity(f *testing.F) {
	for _, s := range logicalLinesSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(checkLogicalLinesParity)
}
