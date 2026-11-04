package versioning

import "testing"

func TestPEP440Admits(t *testing.T) {
	// PEP 440 rows agree with python's packaging.specifiers.SpecifierSet(spec).contains(
	// version, prereleases=True) on the public version.
	cases := []struct {
		spec, version string
		want          bool
	}{
		{"", "1.0", true},
		{">=3.8", "3.8", true},
		{">=3.8", "3.8.0", true},
		{">=3.8", "3.7.99", false},
		{">=3.8", "3.8rc1", false},
		{">3.8", "3.8.post1", false},
		{">3.8", "3.8.1", true},
		{">3.8rc1", "3.8", true},
		{">3.8.post1", "3.8.post2", true},
		{"<4", "3.99", true},
		{"<4", "4.0rc1", false},
		{"<4", "4.0.dev0", false},
		{"<4", "4", false},
		{"<4rc2", "4rc1", true},
		{"<=3.11", "3.11", true},
		{"<=3.11", "3.11.1", false},
		{"<=3.11", "3.11+local", true},
		{"==3.8", "3.8.0", true},
		{"==3.8", "3.8.1", false},
		{"==3.8", "3.8+ubuntu.1", true},
		{"==3.8.*", "3.8.19", true},
		{"==3.8.*", "3.8rc1", true},
		{"==3.8.*", "3.9", false},
		{"==3.8.*", "3.80", false},
		{"!=3.0.*", "3.0.1", false},
		{"!=3.0.*", "3.1", true},
		{"!=3.0.*", "2.7", true},
		{"!=3.9", "3.9.0", false},
		{"!=3.9", "3.9.1", true},
		{"~=3.8", "3.9", true},
		{"~=3.8", "4.0", false},
		{"~=3.8.1", "3.8.5", true},
		{"~=3.8.1", "3.9", false},
		{"~=3.8.1", "3.8.0", false},
		{"===1.0", "1.0", true},
		{">=3.8,<4", "3.12", true},
		{">=3.8, <4", "4.1", false},
		{">=2.7, !=3.0.*, !=3.1.*, !=3.2.*, !=3.3.*", "3.2.5", false},
		{">=2.7, !=3.0.*, !=3.1.*, !=3.2.*, !=3.3.*", "3.4", true},
		{">= 3.8 , < 4", "3.9", true},
		// Poetry.
		{"*", "1.0", true},
		{"^3.9", "3.9", true},
		{"^3.9", "3.13", true},
		{"^3.9", "4.0", false},
		{"^3.9", "3.8", false},
		{"^0.2.3", "0.2.9", true},
		{"^0.2.3", "0.3", false},
		{"^0.0.3", "0.0.4", false},
		{"^0.0", "0.0.9", true},
		{"^0.0", "0.1", false},
		{"~3.9", "3.9.7", true},
		{"~3.9", "3.10", false},
		{"~3", "3.12", true},
		{"~3", "4", false},
		{"3.9", "3.9.0", true},
		{"3.9", "3.9.1", false},
		{"3.9.*", "3.9.1", true},
		{"=3.9", "3.9", true},
		{">=3.9 <3.13", "3.12", true},
		{">=3.9 <3.13", "3.13", false},
		{"^2.7 || ^3.6", "2.7.18", true},
		{"^2.7 || ^3.6", "3.5", false},
		{"^2.7 || ^3.6", "3.12", true},
		{"~2.7 | >=3.6", "3.6", true},
	}
	for _, c := range cases {
		got, err := PEP440Admits(c.spec, c.version)
		if err != nil || got != c.want {
			t.Errorf("PEP440Admits(%q, %q) = %v, %v; want %v", c.spec, c.version, got, err, c.want)
		}
	}
}

func TestPEP440AdmitsErrors(t *testing.T) {
	for _, c := range []struct{ spec, version string }{
		{">=", "1.0"},
		{">=x", "1.0"},
		{"~=3", "3.1"},
		{">=3.6.*", "3.6"},
		{"==3.8a1.*", "3.8"},
		{">=3.8", "not-a-version"},
		{"@3.8", "3.8"},
	} {
		if _, err := PEP440Admits(c.spec, c.version); err == nil {
			t.Errorf("PEP440Admits(%q, %q): no error", c.spec, c.version)
		}
	}
}

func TestPEP440Subset(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{">=3.9", ">=3.9", true},
		{">=3.9", ">=3.10", false}, // mypy 1.20 on a >=3.9 project
		{">=3.9", ">=3.8", true},
		{">=3.9", "", true},
		{"", ">=3.8", false},
		{">=3.9", ">=3.8,<4", false},
		{"^3.9", ">=3.8,<4", true},
		{"^3.9", ">=3.8,<4.0", true},
		{"^3.9", ">=3.9,<3.13", false},
		{">=3.9,<3.13", ">=3.9,<3.13", true},
		{">=3.9", ">=2.7, !=3.0.*, !=3.1.*, !=3.2.*, !=3.3.*", true},
		{">=3.0", ">=2.7, !=3.0.*, !=3.1.*", false},
		{">=3.9", ">=3.9.1", false},
		{"~=3.11", ">=3.11", true},
		{"==3.11.*", ">=3.11", true},
		{"==3.11.*", ">=3.11.4", false},
		{"==3.11.4", ">=3.11", true},
		{"==3.11.4", "!=3.11.4", false},
		{">=3.8,!=3.9.*", ">=3.8,!=3.9.*", true},
		{">=3.8", ">=3.8,!=3.9.*", false},
		{">3.9", ">=3.9", true},
		{">=3.9", ">3.9", false},
		{"^2.7 || ^3.6", ">=2.7,!=3.0.*,!=3.1.*,!=3.2.*,!=3.3.*,!=3.4.*,!=3.5.*", true},
		{"^2.7 || ^3.5", ">=2.7,!=3.0.*,!=3.1.*,!=3.2.*,!=3.3.*,!=3.4.*,!=3.5.*", false},
		{">=3.10,<3.9", ">=4", true}, // empty a
	}
	for _, c := range cases {
		got, err := PEP440Subset(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("PEP440Subset(%q, %q) = %v, %v; want %v", c.a, c.b, got, err, c.want)
		}
	}
	if _, err := PEP440Subset(">=", ">=3.8"); err == nil {
		t.Error("PEP440Subset: invalid a: no error")
	}
	if _, err := PEP440Subset(">=3.8", "~=3"); err == nil {
		t.Error("PEP440Subset: invalid b: no error")
	}
}
