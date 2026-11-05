package versioning

import (
	"fmt"
	"testing"
)

// checkPEP440SpecCompiled checks that ParsePEP440Spec and Admits answer and fail as the
// streaming PEP440Admits does.
func checkPEP440SpecCompiled(t *testing.T, spec, version string) {
	t.Helper()
	want, wantErr := PEP440Admits(spec, version)
	var got bool
	p, err := ParsePEP440Spec(spec)
	if err == nil {
		got, err = p.Admits(version)
	}
	if got != want || fmt.Sprint(err) != fmt.Sprint(wantErr) {
		t.Fatalf("ParsePEP440Spec(%q).Admits(%q) = %v, %v; want %v, %v", spec, version, got, err, want, wantErr)
	}
}

func TestPEP440SpecCompiledParity(t *testing.T) {
	specs, versions := pep440AdmitsCorpus(t)
	for _, s := range specs {
		for _, v := range versions {
			checkPEP440SpecCompiled(t, s, v)
		}
	}
}

func FuzzPEP440SpecCompiled(f *testing.F) {
	specs, versions := pep440AdmitsCorpus(f)
	for i, s := range specs {
		f.Add(s, versions[i%len(versions)])
	}
	f.Fuzz(checkPEP440SpecCompiled)
}
