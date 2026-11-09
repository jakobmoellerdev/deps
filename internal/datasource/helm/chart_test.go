package helmds

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

// indexFiles returns the trimmed real index fixtures, plus the index.yaml files in
// $HELM_INDEX_DIR (whole downloaded indexes, too large to commit).
func indexFiles(t testing.TB) []string {
	t.Helper()
	files, err := filepath.Glob("testdata/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("fixtures: %v", err)
	}
	if dir := os.Getenv("HELM_INDEX_DIR"); dir != "" {
		more, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		files = append(files, more...)
	}
	return files
}

// chartBlocks splits a `helm repo index` written index (chart keys at column 2) into the
// dedented chart blocks scanIndex captures.
func chartBlocks(data []byte) map[string][]byte {
	blocks := map[string][]byte{}
	var chart string
	inEntries := false
	for line := range bytes.Lines(data) {
		switch {
		case line[0] != ' ':
			inEntries, chart = string(line) == "entries:\n", ""
		case !inEntries:
		case len(line) > 2 && line[2] != ' ' && line[2] != '-':
			chart = chartKey(bytes.TrimSpace(line))
			blocks[chart] = append(blocks[chart], line[2:]...)
		case chart != "":
			blocks[chart] = append(blocks[chart], line[2:]...)
		}
	}
	return blocks
}

// TestScanChartRealIndexes checks the line scanner against the YAML decoder on every chart of
// real indexes, and that it handles all of them without falling back.
func TestScanChartRealIndexes(t *testing.T) {
	for _, file := range indexFiles(t) {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var idx struct {
			Entries map[string][]indexVersion `yaml:"entries"`
		}
		if err := yaml.Unmarshal(data, &idx); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		blocks := chartBlocks(data)
		wanted := map[string]bool{}
		versions := 0
		for chart, block := range blocks {
			wanted[chart] = true
			want, err := decodeChartYAML(block, chart)
			if err != nil {
				t.Fatalf("%s %s: %v", file, chart, err)
			}
			got, ok := scanChart(block, chart)
			if !ok {
				t.Errorf("%s %s: scanChart fell back", file, chart)
			} else if !reflect.DeepEqual(got, want) {
				t.Errorf("%s %s: scanChart = %v, YAML = %v", file, chart, got, want)
			}
			versions += len(want)
		}
		got, err := scanIndex(bytes.NewReader(data), wanted)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if len(got) != len(idx.Entries) {
			t.Errorf("%s: %d charts, YAML %d", file, len(got), len(idx.Entries))
		}
		for chart, vs := range idx.Entries {
			if want := releases(vs); !reflect.DeepEqual(got[chart], want) {
				t.Errorf("%s %s: scanIndex = %v, YAML = %v", file, chart, got[chart], want)
			}
		}
		t.Logf("%s: %d charts, %d versions", file, len(blocks), versions)
	}
}

// chartCases are blocks with layouts outside what `helm repo index` writes; scanChart either
// decodes them like the YAML decoder or falls back.
var chartCases = []struct {
	block string
	fast  bool // scanChart handles the block
}{
	{"a:\n", true},
	{"a: # c\n", true},
	{"a:\n- version: 1.0.0\n  urls:\n  - x\n", true},
	{"a:\n  - version: 1.0.0 # c\n    urls:\n      - x\n      - 'y'\n", true},
	{"'a':\n-   version: '1.0''s'\n    urls: ~\n- version: \"2.0\"\n  urls:\n  - \"\"\n", true},
	{"\"a\":\n- version: null\n  urls:\n  - x\n- version: ~\n  urls: [] # c\n- urls: # c\n  - x\n", true},
	{"a:\n- version: 1\n  appVersion: v1\n  digest: abc\n  created: \" 2025-08-21T03:20:00.91572711Z \"\n  urls:\n  - x\n", true},
	{"a:\n- version: 1\n  created: 2025-08-21T03:20:00Z\n  urls:\n  - x\n", true},
	// Values deps does not read, ending where YAML ends them.
	{"a:\n- annotations:\n    images: |\n      - name: x\n        version: \"9\n# c\n    licenses: Apache-2.0\n  version: 1\n  urls:\n  - x\n", true},
	{"a:\n- description: A long\n    plain \"description '\n\n    continued\n  version: 1\n  urls:\n  - x\n", true},
	{"a:\n- description: \"quoted\n  version: 2\n  urls: [x]\"\n  version: 1\n  urls:\n  - x\n", true},
	{"a:\n- description: 'it''s\nversion: 2'\n  version: 1\n  urls:\n  - x\n", true},
	{"a:\n- keywords:\n  - db\n  - \"x\" # c\n  dependencies:\n  - name: common\n    tags: [a, {b: c}]\n    \"k\": v\n    l: !!str &x |-\n      text\n    m: *x\n  version: 1\n  urls:\n  - x\n", true},
	{"a:\n- k: &x\n    - [a]: b\n  version: 1\n  urls:\n  - x\n", true},
	{"a:\n- x: \"a\\\"\n  version: 2\"\n  version: 1\n  urls:\n  - x\n", true},
	{"a:\n- x:\n    - - b\n      - c\n  version: 1\n  urls:\n  - x\n", true},
	// Falls back.
	{"a: []\n", false},
	{"a:b:\n", false},
	{"null:\n", false},
	{"b:\n", false},
	{"a:\n- version: 1\n  version: 2\n", false},
	{"a:\n- k: 1\n  k: 2\n", false},
	{"a:\n- version: \"1\\t\"\n", false},
	{"a:\n- version: \"1\n  urls: [x]\"\n", false},
	{"a:\n- version: &v 1\n", false},
	{"a:\n- version: 1\n    .0\n", false},
	{"a:\n- version: 1: 2\n", false},
	{"a:\n- version: [1]\n", false},
	{"a:\n- version:\n  - 1\n", false},
	{"a:\n- version:1\n", false},
	{"a:\n- version : 1\n", false},
	{"a:\n- <<: *x\n", false},
	{"a:\n- \"version\": 1\n", false},
	{"a:\n- version: 1\n  urls: [x]\n", false},
	{"a:\n- version: 1\n  urls: x\n", false},
	{"a:\n- version: 1\n  urls:\n  -\n", false},
	{"a:\n- version: 1\n  urls:\n  - ~\n", false},
	{"a:\n- version: 1\n  urls:\n    - x\n  - y\n", false},
	{"a:\n- version: 1\n  urls:\n  - x\n    y\n", false},
	{"a:\n- version: 1\n  k: v\n  - x\n", false},
	{"a:\n- version: 1\n\tk: v\n", false},
	{"a:\n- version: 1\r\n", false},
	{"a:\n- version: 1\u2028\n", false},
	{"a:\n- version: \xff\n", false},
	{"a:\n-\n  version: 1\n", false},
	{"a:\n- - version: 1\n", false},
	{"a:\n-x\n", false},
	{"a:\n  - version: 1\n - version: 2\n", false},
	{"a:\nb: 1\n", false},
	{"a:\n- k:\n    ? x\n", false},
	{"a:\n- k: [a,\n    b]\n", false},
	{"a:\n- k:\n    plain\n", false},
	{"a:\n- k:\n    |\n", false},
	{"a:\n- k: \"open\n", false},
}

func TestScanChart(t *testing.T) {
	for _, c := range chartCases {
		got, ok := scanChart([]byte(c.block), "a")
		if ok != c.fast {
			t.Errorf("%q: handled %v, want %v", c.block, ok, c.fast)
		}
		if !ok {
			continue
		}
		want, err := decodeChartYAML([]byte(c.block), "a")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: scanChart = %v, YAML = %v, %v", c.block, got, want, err)
		}
	}
}

// FuzzScanChart checks that whenever scanChart handles a block that is valid YAML, it decodes
// it as the YAML decoder does. On invalid YAML it may still succeed: like scanIndex for the
// charts it skips, it does not validate what it does not read.
func FuzzScanChart(f *testing.F) {
	for _, c := range chartCases {
		f.Add(c.block)
	}
	for _, file := range indexFiles(f) {
		data, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		for _, block := range chartBlocks(data) {
			f.Add(string(block))
		}
	}
	f.Fuzz(func(t *testing.T, block string) {
		first, _, _ := strings.Cut(block, "\n")
		checkScanChart(t, block, chartKey([]byte(first)))
	})
}

// checkScanChart fails t when scanChart handles valid YAML differently from the YAML decoder.
func checkScanChart(t *testing.T, block, chart string) {
	t.Helper()
	got, ok := scanChart([]byte(block), chart)
	if !ok {
		return
	}
	want, err := decodeChartYAML([]byte(block), chart)
	if err == nil && !reflect.DeepEqual(got, want) {
		t.Errorf("%q: scanChart = %v, YAML = %v", block, got, want)
	}
}

// fuzzLines are adversarial line contents genChart puts inside scalars, sequences and
// mappings, and between them.
var fuzzLines = []string{
	"- version: 1", "- version: '2'", "version: 3", `version: "4"`, "version:", "version: 5 # c", "version: ~",
	"appVersion: v1", `created: "2025-01-01T00:00:00Z"`, "digest: abc # c", "- urls:", "urls:", "urls: []",
	"urls: ~", "urls: [x]", "- x", "- 'y'", `- "z"`, "-", "- - x", "- ~", "k: v", "k:", "- k: v", "- k:",
	"k: v # c", `"k": v`, "k: !!str v", "k: &a", "k: *a", "&a k: v", "<<: *a", "? k", ": v",
	"description: |", "description: >-", "k: |2", "| x", `text: "open`, `close"`, "text: 'open", "close'",
	"it''s'", "plain text", `plain: "x`, `x: "a\"`, "- [a, b]", "k: [a,", "b]", "k: {a: b}", "[a]: b",
	"# comment", "", "...", "---", "k: v\tw", "dependencies:", "- name: x", "a:", "'a':", "a: []",
}

// genChart writes a chart block shaped like `helm repo index` output, with the value shapes
// scanChart tells apart and fuzzLines inside and between them; pick(n) chooses in [0, n).
func genChart(pick func(n int) int) string {
	var b strings.Builder
	line := func(indent int, s string) {
		b.WriteString(strings.Repeat(" ", indent))
		b.WriteString(s)
		b.WriteByte('\n')
	}
	noise := func(indent int) {
		for range pick(3) {
			line(indent+pick(3)-1, fuzzLines[pick(len(fuzzLines))])
		}
	}
	keys := []string{"version", "appVersion", "created", "digest", "urls", "description", "annotations", "keywords", "k", "x", "y", "z"}
	scalars := []string{"1.0", "'1.0'", `"v1"`, "~", "", "1 # c", `"a\"b"`, "x: y", "'it''s'", "2025-01-01T00:00:00Z", "&x 1", "*x", "[a]", "|", "a #b"}
	line(0, "a:")
	dash := pick(3)
	for range 1 + pick(4) {
		col := dash + 1 + pick(2) + 1
		avail := slices.Clone(keys)
		for k := range 1 + pick(6) {
			i := pick(len(avail))
			if k == 0 && pick(2) == 0 {
				i = 0 // version first, as most items have it
			}
			key := avail[i]
			avail = slices.Delete(avail, i, i+1)
			prefix := key + ":"
			if k == 0 {
				b.WriteString(strings.Repeat(" ", dash) + "-" + strings.Repeat(" ", col-dash-1))
			} else {
				b.WriteString(strings.Repeat(" ", col))
			}
			deeper := col + 1 + pick(3)
			switch shape := pick(9); {
			case shape == 0 || key == "urls" && shape < 4:
				b.WriteString(prefix + "\n")
				seq := col + 2*pick(2)
				for range pick(3) {
					line(seq, "- "+scalars[pick(len(scalars))])
				}
			case shape == 1:
				b.WriteString(prefix + " plain value\n")
				for range pick(3) {
					line(deeper, fuzzLines[pick(len(fuzzLines))])
				}
			case shape == 2:
				b.WriteString(prefix + []string{" |", " >-", " |2", " !!str |"}[pick(4)] + "\n")
				for range 1 + pick(3) {
					line(deeper, fuzzLines[pick(len(fuzzLines))])
				}
			case shape == 3:
				q := []string{`"`, "'"}[pick(2)]
				b.WriteString(prefix + " " + q + "open\n")
				noise(col)
				line(pick(col+2), "close"+q)
			case shape == 4:
				b.WriteString(prefix + "\n")
				for range 1 + pick(3) {
					line(deeper, fuzzLines[pick(len(fuzzLines))])
				}
			default:
				b.WriteString(prefix + " " + scalars[pick(len(scalars))] + "\n")
			}
			if pick(4) == 0 {
				noise(col)
			}
		}
		if slices.Contains(avail, "urls") && pick(4) > 0 {
			line(col, "urls:")
			line(col+2*pick(2), "- x")
		}
	}
	return b.String()
}

// FuzzScanChartGen runs checkScanChart on genChart blocks chosen by the input bytes.
func FuzzScanChartGen(f *testing.F) {
	f.Add([]byte{1, 3, 0, 4, 8, 3, 8, 1, 8, 0, 4, 0, 1, 7})
	f.Add([]byte{0, 2, 1, 2, 5, 3, 2, 0, 1, 1, 1, 4, 4, 0, 1, 2, 8, 0, 0, 4, 0, 1, 2})
	f.Fuzz(func(t *testing.T, data []byte) {
		checkScanChart(t, genChart(func(n int) int {
			if len(data) == 0 {
				return 0
			}
			c := int(data[0]) % n
			data = data[1:]
			return c
		}), "a")
	})
}
