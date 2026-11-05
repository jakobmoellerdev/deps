package piprequirements

import (
	"reflect"
	"strings"
	"testing"
)

// referencesOracle is the line-by-line References that splits every line into fields; the
// prefix scan in References must agree with it.
func referencesOracle(src []byte) []Reference {
	var out []Reference
	for l := range strings.SplitSeq(string(src), "\n") {
		out = appendReference(out, l)
	}
	return out
}

// headerLinesOracle is headerLines over a copy of the whole file.
func headerLinesOracle(src []byte) []string {
	var out []string
	for rest := string(src); strings.HasPrefix(rest, "#"); {
		l, r, ok := strings.Cut(rest, "\n")
		if ok {
			l += "\n"
		}
		out = append(out, l)
		rest = r
	}
	return out
}

var refScanCases = []string{
	"",
	"\n",
	"-r base.txt\n-c constraints.txt\n",
	"-rbase.txt\n-cc.txt\n-e./dir\n",
	"--requirement=a.txt\n--constraint=b.txt\n--editable=./x\n",
	"--requirement a.txt\n--constraint b.txt\n--editable ../y\n",
	"-e ./dir\n./dir\n.\n../up\n.hidden\n",
	"\t-r\ttab.txt\r\n  -c  sp.txt  \r\n\v\f-e ./v\n",
	"\u00a0-r nbsp.txt\n\u2003./em\n\u0085-c nel.txt\n\u3000-e ./ideo\n",
	"-\u00a0r x\n--\u00a0requirement x\n-x y\n--hash=sha256:00\n--index-url https://x\n",
	"# -r commented\n  # ./dir\n-r a.txt # trailing\n./dir #c\n -r #x\n",
	"pkg==1.0 \\\n    --hash=sha256:ab \\\n    # via app\n",
	"-r\n-c\n-e\n--requirement=\n-e pkg\n-e git+https://x\n",
	"\xc2-r x\n\xff./y\n-r last-no-newline",
	"#a\n#b\n",
	"#a\n#b",
	"#a\nx\n#c\n",
	"x\n#a\n",
	"#\n#\n\n#",
}

func TestReferencesScanParity(t *testing.T) {
	for _, c := range refScanCases {
		if got, want := References([]byte(c)), referencesOracle([]byte(c)); !reflect.DeepEqual(got, want) {
			t.Errorf("References(%q) = %v, want %v", c, got, want)
		}
		if got, want := headerLines([]byte(c)), headerLinesOracle([]byte(c)); !reflect.DeepEqual(got, want) {
			t.Errorf("headerLines(%q) = %q, want %q", c, got, want)
		}
	}
}

func FuzzReferencesScan(f *testing.F) {
	for _, c := range refScanCases {
		f.Add(c)
	}
	f.Fuzz(func(t *testing.T, c string) {
		if got, want := References([]byte(c)), referencesOracle([]byte(c)); !reflect.DeepEqual(got, want) {
			t.Errorf("References(%q) = %v, want %v", c, got, want)
		}
		if got, want := headerLines([]byte(c)), headerLinesOracle([]byte(c)); !reflect.DeepEqual(got, want) {
			t.Errorf("headerLines(%q) = %q, want %q", c, got, want)
		}
	})
}
