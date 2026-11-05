package versioning

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// parseMavenStack is the former parseMaven, which built each nested list in a slice of its
// own; parseMaven must parse every version to the same items.
func parseMavenStack(s string) (mavenVersion, bool) {
	if !validMaven(s) {
		return mavenVersion{}, false
	}
	s = strings.ToLower(s)
	stack := [][]mavenItem{nil}
	push := func() { stack = append(stack, nil) }
	add := func(it mavenItem) { stack[len(stack)-1] = append(stack[len(stack)-1], it) }
	digit, start := false, 0
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '.' || c == '-':
			if i == start {
				add(mavenItem{kind: mavenInt})
			} else {
				add(mavenParseItem(digit, s[start:i]))
			}
			start = i + 1
			if c == '-' {
				push()
			}
		case '0' <= c && c <= '9':
			if !digit && i > start {
				add(mavenStr(s[start:i], true))
				start = i
				push()
			}
			digit = true
		default:
			if digit && i > start {
				add(mavenParseItem(true, s[start:i]))
				start = i
				push()
			}
			digit = false
		}
	}
	if len(s) > start {
		add(mavenParseItem(digit, s[start:]))
	}
	for len(stack) > 1 {
		child := mavenNormalize(stack[len(stack)-1])
		stack = stack[:len(stack)-1]
		add(mavenItem{kind: mavenList, list: child})
	}
	items := mavenNormalize(stack[0])
	// The rest of the result does not depend on how the lists are built.
	v, ok := parseMaven(s)
	v.items = items
	return v, ok
}

// equalMavenItems compares item lists; a nil and an empty list are equal.
func equalMavenItems(a, b []mavenItem) bool {
	return slices.EqualFunc(a, b, func(x, y mavenItem) bool {
		return x.kind == y.kind && x.num == y.num && x.str == y.str && x.key == y.key && equalMavenItems(x.list, y.list)
	})
}

func checkMavenParse(t *testing.T, s string) {
	t.Helper()
	got, ok := parseMaven(s)
	want, wantOK := parseMavenStack(s)
	if ok != wantOK {
		t.Fatalf("parseMaven(%q) ok = %v, want %v", s, ok, wantOK)
	}
	if !equalMavenItems(got.items, want.items) {
		t.Fatalf("parseMaven(%q) items = %+v, want %+v", s, got.items, want.items)
	}
}

// TestParseMavenAllocs checks that mavenItemCount covers every item: the buffer is the only
// allocation for lowercase versions with known qualifiers.
func TestParseMavenAllocs(t *testing.T) {
	for _, s := range []string{"1", "1.2.3", "1.0-rc-1", "1-alpha1-beta2.3", "1--2", "1a1b1m1-rc1", "1-1-1-1-1-1-1-1"} {
		if n := testing.AllocsPerRun(10, func() { parseMaven(s) }); n != 1 {
			t.Errorf("parseMaven(%q) allocates %v times, want 1", s, n)
		}
	}
}

// mavenParseCorpus is every <version> of the repository's Maven fixtures plus edge cases.
func mavenParseCorpus(t testing.TB) []string {
	corpus := []string{
		"1", "0", "1.0", "1.0.0", "1-", "1--2", "1..2", "1.-2", "1-.2", "1-0", "1.0-0.0-0",
		"1a", "1a1", "1-a1", "1.a.1", "1-alpha-1", "1-ALPHA1", "1.0-rc", "1-cr2", "1-ga",
		"1-final", "1-release", "1-sp1", "1-SNAPSHOT", "1.0.0-jre", "33.3.1-android",
		"9.4.51.v20230217", "6.2250.va_d01d559a_209", "1_2", "1.2_3-x_y", "2.0.0-M1",
		"1-1-1-1-1-1-1-1-1-1-1-1", "1a2b3c4d5e6f7g8h9i0j", "007", "1.0.0.0.0.0", "1-0-0-a",
	}
	re := regexp.MustCompile(`<version>([^<$]+)</version>`)
	for _, root := range []string{"../datasource/maven/testdata", "../manager/maven/testdata"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".xml") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				corpus = append(corpus, strings.TrimSpace(m[1]))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return corpus
}

func TestParseMavenParity(t *testing.T) {
	corpus := mavenParseCorpus(t)
	if len(corpus) < 400 {
		t.Fatalf("corpus has %d versions, want the fixtures' versions", len(corpus))
	}
	for _, s := range corpus {
		checkMavenParse(t, s)
	}
}

func FuzzParseMaven(f *testing.F) {
	for _, s := range mavenParseCorpus(f) {
		f.Add(s)
	}
	f.Fuzz(checkMavenParse)
}

// BenchmarkParseMaven parses the fixtures' Maven versions.
func BenchmarkParseMaven(b *testing.B) {
	corpus := mavenParseCorpus(b)
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range corpus {
			parseMaven(s)
		}
	}
}
