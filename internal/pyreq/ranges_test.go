package pyreq

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestAnchor(t *testing.T) {
	for _, c := range []struct {
		spec string
		g    Grammar
		ver  string
		skip string
	}{
		{"==2.31.0", PEP440, "2.31.0", ""},
		{"===1.0", PEP440, "1.0", ""},
		{"~= 1.4", PEP440, "1.4", ""},
		{">=2.0", PEP440, "2.0", ""},
		{"==4.*", PEP440, "4", ""},
		{"==1.2.*", Pipenv, "1.2", ""},
		{"3.*", Pipenv, "3", ""},
		{"1.2.3", Pipenv, "1.2.3", ""},
		{">=1.2,<2", PEP440, "1.2", ""},
		{">= 1.2, < 2.0", PEP440, "1.2", ""},
		{"<1.0.0,>=0.114.2", PEP440, "0.114.2", ""}, // fastapi-template style
		{"<2", PEP440, "2", ""},
		{"<=2,!=1.5", PEP440, "2", ""},
		{">1.0", PEP440, "1.0", ""},
		{"!=1.5,>=1.2", PEP440, "1.2", ""},
		{"^1.2.3", Poetry, "1.2.3", ""},
		{"~1.2", Poetry, "1.2", ""},
		{"=1.0", Poetry, "1.0", ""},
		{"1.0", Poetry, "1.0", ""},
		{"1.2.*", Poetry, "1.2", ""},
		{">=1.2 <2", Poetry, "1.2", ""},
		{"^1 || ^2", Poetry, "2", ""},
		{"^1|^2", Poetry, "2", ""},
		{"", PEP440, "", SkipUnconstrained},
		{"*", Pipenv, "", SkipUnconstrained},
		{"!=1.0", PEP440, "", SkipUnconstrained},
		{"!=1.0,!=1.1", PEP440, "", SkipUnconstrained},
		{"1.2.3", PEP440, "", SkipRange},
		{"^1.2", PEP440, "", SkipRange},
		{">=1.2 <2", PEP440, "", SkipRange},
		{"^1 || ^2", Pipenv, "", SkipRange},
		{"~=1.*", PEP440, "", SkipRange},
		{">=1.*", PEP440, "", SkipRange},
		{"^1.*", Poetry, "", SkipRange},
		{"==", PEP440, "", SkipRange},
		{"== 1 2", PEP440, "", SkipRange},
		{">=1.2,", PEP440, "", SkipRange},
		{"===foobar", PEP440, "", SkipRange},
		{">=1.0-SNAPSHOT", PEP440, "", SkipRange},
	} {
		ver, skip := anchor(c.spec, c.g)
		if ver != c.ver || skip != c.skip {
			t.Errorf("anchor(%q, %d) = %q, %q; want %q, %q", c.spec, c.g, ver, skip, c.ver, c.skip)
		}
	}
}

// TestNewRange checks the rewrite rules against Renovate's pep440 getNewValue test table
// (lib/modules/versioning/pep440/index.spec.ts, fetched 2026-11-04): out-of-range versions
// with its `replace` results, in-range ones with its `bump` results; "" = no update.
func TestNewRange(t *testing.T) {
	for _, c := range []struct{ spec, v, want string }{
		// Renovate replace (the version is outside the range).
		{"==1.0.3", "1.2.3", "==1.2.3"},
		{"~=1.0.3", "1.2.3", "~=1.2.3"},
		{"==1.0.*", "1.2.3", "==1.2.*"},
		{"<1.2.2.3", "1.2.3", "<1.2.3.1"},
		{"<1.2.3", "1.2.3", "<1.2.4"},
		{"<1.2", "1.2.3", "<1.3"},
		{"<1", "1.2.3", "<2"},
		{">2.0.0", "1.2.3", ">=1.2.3"},
		{">=2.0.0", "1.2.3", ">=1.2.3"},
		{"~=1.1.0, !=1.1.1", "1.2.3", "~=1.2.3, !=1.1.1"},
		{"~=1.1.0,!=1.1.1", "1.2.3", "~=1.2.3,!=1.1.1"},
		{">=19.12.2,<20.13.9", "21.3.1", ">=21.3.1,<22.0.0"},
		{">=19.12.2,<19.13.9", "20.3.1", ">=20.3.1,<20.4.0"},
		{">=19.12.2,<19.13.0", "20.3.1", ">=20.3.1,<20.4.0"},
		{">=19.12.2,<19.13.0", "20.3.0", ">=20.3.0,<20.4.0"},
		{">=19.12.2,<19.13.0", "19.13.1", ">=19.13.1,<19.14.0"},
		{">=19.12.2,<19.13.0", "19.13.0", ">=19.13.0,<19.14.0"},
		{"~=7.2", "8.0.1", "~=8.0"},
		{"~=7.2", "8", "~=8.0"},
		{"~=7.2.0", "8.2", "~=8.2.0"},
		{">=1.0.0,<1.1.0", "1.2.0", ">=1.2.0,<1.3.0"},
		{"<1.3.0", "1.6.0", "<1.6.1"},
		{"<=1.3.0", "1.6.0", "<=1.6.0"},
		{"==3.2.*,>=3.2.2", "4.1.1", "==4.1.*,>=4.1.1"}, // Renovate then drops the redundant >=
		{"!=1.2.3", "1.2.3", ""},
		// Renovate bump (the version is inside the range).
		{">=1.2.0", "1.2.3", ">=1.2.3"},
		{"~=1.2.0", "1.2.3", "~=1.2.3"},
		{"==1.2.*", "1.2.3", ""},
		{"<2.0.0", "1.2.3", ""},
		{">0.9.8", "1.2.3", ""},
		{"<1.3.0", "0.9.2", ""},
		{"<=1.3.0", "0.9.2", ""},
		// Compound ranges as written in real manifests.
		{">=1.2,<2", "3.1.0", ">=3.1,<4"},
		{">=1.2,<2", "1.5.0", ">=1.5.0,<2"},
		{">= 1.2, < 2.0", "3.0", ">= 3, < 3.1"}, // a major bound followed by 0 counts as minor precision
		{"<1.0.0,>=0.114.2", "1.2.0", "<1.3.0,>=1.2.0"},
		{"<1.0.0,>=0.114.2", "0.115.4", "<1.0.0,>=0.115.4"},
		{">1.0,<2", "3.1.4", ">3.1,<4"},
		{">=1.2,!=1.5.0,<2", "1.5.0", ""},
		{">=2.31.0", "2.32.3", ">=2.32.3"},
		{"==4.*", "5", "==5.*"},
		{"<2", "1.5", ""},
		{"<2", "3", "<4"},
		// Poetry.
		{"^2.31.0", "3.0.0", "^3.0.0"},
		{"^2.31", "2.32.3", "^2.32"},
		{"~1.2", "1.3.4", "~1.3"},
		{"1.2.*", "2.0.1", "2.0.*"},
		{"1.0", "1.1", "1.1"},
		{">=1.2 <2", "3.1", ">=3.1 <4"},
		{"^1 || ^2", "3.0.0", "^1 || ^2 || ^3"},
		{"^1.2|^2.0", "3.1.0", "^1.2|^2.0|^3.1"},
		{"^1.2 || ^2.0", "1.5.3", "^1.5 || ^2.0"},
		{"^1 || ^2", "1.5.0", ""},
		{">=1.2,<2", "not a version", ""},
	} {
		got, ok := NewRange(dep.Dep{Range: c.spec}, c.v)
		if !ok {
			got = ""
		}
		if got != c.want {
			t.Errorf("NewRange(%q, %s) = %q, want %q", c.spec, c.v, got, c.want)
		}
	}
}

func TestRangeEditsSpans(t *testing.T) {
	src := `x = ">= 1.2, < 2.0"`
	d := NewDep("pep621", "project.dependencies", "pyproject.toml", "x", 1)
	SetVersion(&d, ">= 1.2, < 2.0", 5, true, PEP440)
	edits, err := Edits(dep.Update{Dep: d, NewVersion: "3.0"})
	if err != nil || len(edits) != 2 || src[edits[0].Start:edits[0].End] != edits[0].Old || edits[0].Old != "1.2" || edits[1].Old != "2.0" {
		t.Fatalf("edits = %+v, %v", edits, err)
	}
	if !InRange(d, "1.9") || InRange(d, "2.0") || InRange(dep.Dep{Range: ">=="}, "1") {
		t.Error("InRange")
	}
}
