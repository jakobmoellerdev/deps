package versioning

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// pep440AdmitsSet is the former PEP440Admits, which intersected the clauses' sets into one
// before checking the version; PEP440Admits must answer and fail as it did.
func pep440AdmitsSet(spec, version string) (bool, error) {
	set, err := parsePEP440Set(spec, false)
	if err != nil {
		return false, err
	}
	v, ok := parsePEP440(version)
	if !ok {
		return false, fmt.Errorf("invalid PEP 440 version %q", version)
	}
	v.local = ""
	return set.contains(v), nil
}

func checkPEP440Admits(t *testing.T, spec, version string) {
	t.Helper()
	got, err := PEP440Admits(spec, version)
	want, wantErr := pep440AdmitsSet(spec, version)
	if got != want || fmt.Sprint(err) != fmt.Sprint(wantErr) {
		t.Fatalf("PEP440Admits(%q, %q) = %v, %v; want %v, %v", spec, version, got, err, want, wantErr)
	}
}

// pep440AdmitsCorpus is the specifiers and versions of the Python manager fixtures plus edge
// cases.
func pep440AdmitsCorpus(t testing.TB) (specs, versions []string) {
	specs = []string{
		"", " ", "|", "||", " || ", "*", "==*", ">=1.0", ">=1.0,<2", ">=1.0, <2.0, !=1.5.*", "~=1.4.2", "~=1",
		"^0.0.3", "^1.2", "~1.2", "1.2.*", "!=1.2.*", "==1.2.*", ">1.0", ">1.0.post1", "<2", "<2.0rc1",
		"<=2", "===1.0", "=1.0", ">=2,<1", ">=1 <2 || >=3", "^1 || ^2", ">=", ">=1.0,", ",>=1.0", ">= 1.0",
		">=1.0.*", "~=1.*", "~=1.0.0+local", "==1.0+local", "!=1.0", ">=1,<2|>=3,<4", "^3.9", ">=3.8,!=3.9.*,<4",
		"abc", ">=x", "1.2.3", "==1.2.3.4.5.6.7.8.9.10.11",
	}
	versions = []string{
		"0", "1", "1.0", "1.0.0", "1.2", "1.2.3", "1.4.2", "1.5", "1.5.1", "2", "2.0", "2.0rc1", "2.0.dev1", "2.0a1",
		"1.0.post1", "1.0.post2", "1.0+local", "3.9.1", "3.10", "4", "0.0.3", "0.0.4", "v1.0", "1!1.0", "x", "",
		"1.0-1", "1.0_rc1", "3.0", "3.5",
	}
	specRe := regexp.MustCompile(`(?:===|==|!=|~=|<=|>=|<|>|\^|~)\s*[0-9][^"'\s;#\]\[]*(?:\s*,\s*(?:===|==|!=|~=|<=|>=|<|>|\^|~)\s*[0-9][^"'\s;#,\]\[]*)*`)
	verRe := regexp.MustCompile(`\b\d+(?:\.\d+)+(?:(?:a|b|rc|\.post|\.dev)\d+)?\b`)
	files, err := filepath.Glob("../manager/*/testdata/*")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		switch filepath.Base(filepath.Dir(filepath.Dir(f))) {
		case "piprequirements", "pep621", "poetry", "pipenv":
		default:
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range specRe.FindAllString(string(b), -1) {
			if !seen[s] {
				seen[s] = true
				specs = append(specs, s)
			}
		}
		for _, v := range verRe.FindAllString(string(b), -1) {
			if !seen[v] {
				seen[v] = true
				versions = append(versions, v)
			}
		}
	}
	return specs, versions
}

func TestPEP440AdmitsParity(t *testing.T) {
	specs, versions := pep440AdmitsCorpus(t)
	if len(specs) < 200 || len(versions) < 200 {
		t.Fatalf("corpus has %d specs and %d versions, want the fixtures'", len(specs), len(versions))
	}
	for _, s := range specs {
		for _, v := range versions {
			checkPEP440Admits(t, s, v)
		}
	}
}

func FuzzPEP440Admits(f *testing.F) {
	specs, versions := pep440AdmitsCorpus(f)
	for i, s := range specs {
		f.Add(s, versions[i%len(versions)])
	}
	f.Fuzz(checkPEP440Admits)
}

// BenchmarkPEP440Admits checks 3000 PyPI-style releases against the ranges of a requirements
// file, as range updates do for every candidate.
func BenchmarkPEP440Admits(b *testing.B) {
	var versions []string
	for i := range 3000 {
		versions = append(versions, fmt.Sprintf("%d.%d.%d", i/600, i/30%20, i%30))
	}
	specs := []string{">=1.2,<2", "~=1.4", ">=1.0,!=1.5.*,<3", "==2.*", "^1.2 || ^3"}
	b.ReportAllocs()
	for b.Loop() {
		for _, s := range specs {
			for _, v := range versions {
				_, _ = PEP440Admits(s, v)
			}
		}
	}
}
