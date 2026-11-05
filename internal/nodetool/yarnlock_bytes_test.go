package nodetool

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// yarnVersionsOracle is the string-based yarnVersions this byte-based one replaced.
func yarnVersionsOracle(r io.Reader, ws []lockWant) ([]string, error) {
	want := map[string][]int{}
	for i, w := range ws {
		if w.rng == "" {
			continue
		}
		want[w.name+"@"+w.rng] = append(want[w.name+"@"+w.rng], i)
		want[w.name+"@npm:"+w.rng] = append(want[w.name+"@npm:"+w.rng], i)
	}
	out := make([]string, len(ws))
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 16<<20)
	var cur []int // the wants of the entry being read
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "" || line[0] == '#':
			cur = nil
		case line[0] != ' ':
			cur = cur[:0]
			for _, d := range yarnDescriptors(line) {
				cur = append(cur, want[d]...)
			}
		case len(cur) > 0:
			rest, ok := strings.CutPrefix(line, "  version")
			if !ok || rest == "" || (rest[0] != ' ' && rest[0] != ':') {
				continue
			}
			v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(rest, ":")), `"`)
			for _, i := range cur {
				out[i] = v
			}
			cur = cur[:0]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func yarnBytesWants(lock string) []lockWant {
	var ws []lockWant
	for line := range strings.Lines(lock) {
		if line == "" || line[0] == ' ' || line[0] == '#' {
			continue
		}
		for _, d := range yarnDescriptors(strings.TrimRight(line, "\n")) {
			d = strings.Replace(d, "@npm:", "@", 1)
			if i := strings.LastIndex(d, "@"); i > 0 {
				ws = append(ws, lockWant{name: d[:i], rng: d[i+1:]})
			}
		}
	}
	return append(ws, lockWant{name: "absent", rng: "^1"}, lockWant{name: "norange"})
}

func TestYarnVersionsBytesParity(t *testing.T) {
	for _, dir := range []string{"yarn-classic", "yarn-berry"} {
		files, _ := filepath.Glob(filepath.Join("testdata", "locks", dir, "*", "yarn.lock"))
		files2, _ := filepath.Glob(filepath.Join("testdata", "locks", dir, "yarn.lock"))
		files = append(files, files2...)
		if len(files) == 0 {
			t.Fatalf("no fixtures in %s", dir)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, lock := range []string{string(b), strings.ReplaceAll(string(b), "\n", "\r\n")} {
				ws := yarnBytesWants(lock)
				got, err := yarnVersions(strings.NewReader(lock), ws)
				want, err2 := yarnVersionsOracle(strings.NewReader(lock), ws)
				if err != nil || err2 != nil || !slices.Equal(got, want) {
					t.Errorf("%s: got %q, %v; want %q, %v", f, got, err, want, err2)
				}
			}
		}
	}
}

func FuzzYarnVersionsBytes(f *testing.F) {
	f.Add("ms@^2.1.0, \"@s/a@~1.0.0\":\n  version \"2.1.2\"\n\n\"ms@npm:^2.1.1\":\n  version: 2.1.3\r\n# c\n", "ms", "^2.1.0")
	f.Add("__metadata:\n  version: 6\n", "@s/a", "~1.0.0")
	f.Fuzz(func(t *testing.T, lock, name, rng string) {
		ws := append(yarnBytesWants(lock), lockWant{name: name, rng: rng})
		got, err := yarnVersions(strings.NewReader(lock), ws)
		want, err2 := yarnVersionsOracle(strings.NewReader(lock), ws)
		if (err == nil) != (err2 == nil) || !slices.Equal(got, want) {
			t.Fatalf("got %q, %v; want %q, %v", got, err, want, err2)
		}
	})
}

func yarnBytesBenchLock(berry bool) string {
	var b strings.Builder
	b.WriteString("# yarn lockfile v1\n\n")
	for i := range 20000 {
		if berry {
			fmt.Fprintf(&b, "\"pkg%d@npm:^1.%d.0, pkg%d@npm:~1.%d.1\":\n  version: 1.%d.3\n  resolution: \"pkg%d@npm:1.%d.3\"\n  checksum: abcdef\n  languageName: node\n\n", i, i, i, i, i, i, i)
		} else {
			fmt.Fprintf(&b, "pkg%d@^1.%d.0, \"pkg%d@~1.%d.1\":\n  version \"1.%d.3\"\n  resolved \"https://registry.yarnpkg.com/pkg%d/-/pkg%d-1.%d.3.tgz#abc\"\n  integrity sha512-xyz\n\n", i, i, i, i, i, i, i, i)
		}
	}
	return b.String()
}

func BenchmarkYarnVersionsBytes(b *testing.B) {
	var ws []lockWant
	for i := range 200 {
		ws = append(ws, lockWant{name: fmt.Sprintf("pkg%d", i*100), rng: fmt.Sprintf("^1.%d.0", i*100)})
	}
	for _, kind := range []string{"classic", "berry"} {
		lock := yarnBytesBenchLock(kind == "berry")
		b.Run(kind, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := yarnVersions(strings.NewReader(lock), ws); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
