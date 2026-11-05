package pattern

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		in       string
		want     bool
	}{
		{"empty list", nil, "a", false},
		{"glob", []string{"k8s.io/*"}, "k8s.io/api", true},
		{"glob stays in segment", []string{"k8s.io/*"}, "k8s.io/api/x", false},
		{"doublestar", []string{"k8s.io/**"}, "k8s.io/api/x", true},
		{"regex", []string{`/^k8s\.io\//`}, "k8s.io/api", true},
		{"regex is unanchored", []string{`/api/`}, "k8s.io/api", true},
		{"regex no match", []string{`/^K8S/`}, "k8s.io/api", false},
		{"regex i", []string{`/^K8S/i`}, "k8s.io/api", true},
		{"negation only matches others", []string{"!k8s.io/**"}, "golang.org/x/net", true},
		{"negation only excludes", []string{"!k8s.io/**"}, "k8s.io/api", false},
		{"mixed include", []string{`/^k8s\.io\//`, "!k8s.io/kube-openapi"}, "k8s.io/api", true},
		{"mixed exclude", []string{`/^k8s\.io\//`, "!k8s.io/kube-openapi"}, "k8s.io/kube-openapi", false},
		{"mixed no positive match", []string{`/^k8s\.io\//`, "!k8s.io/kube-openapi"}, "golang.org/x/net", false},
		{"negated regex", []string{"**", `!/openapi/`}, "k8s.io/kube-openapi", false},
		{"slash alone is a glob", []string{"/"}, "/", true},
		{"slashes without a regex end are a glob", []string{"/.github/workflows/**/*.yml"}, "/.github/workflows/a/b.yml", true},
		{"other flags make a glob", []string{"/a/g"}, "/a/g", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := Compile(tt.patterns)
			if err != nil {
				t.Fatal(err)
			}
			if got := l.Match(tt.in); got != tt.want {
				t.Errorf("Compile(%q).Match(%q) = %v, want %v", tt.patterns, tt.in, got, tt.want)
			}
		})
	}
}

func TestNilList(t *testing.T) {
	var l *List
	if l.Match("a") {
		t.Error("nil list matched")
	}
}

func TestCompileErrors(t *testing.T) {
	tests := []struct{ pattern, want string }{
		{"a[", `invalid glob "a[": syntax error in pattern`},
		{"/a(/", "invalid regex \"/a(/\": error parsing regexp: missing closing ): `a(`"},
		{"!", `invalid glob "!": missing pattern after !`},
		{"!/a(/i", "invalid regex \"!/a(/i\": error parsing regexp: missing closing ): `(?i)a(`"},
	}
	for _, tt := range tests {
		_, err := Compile([]string{tt.pattern})
		if err == nil || err.Error() != tt.want {
			t.Errorf("Compile(%q) error = %v, want %s", tt.pattern, err, tt.want)
		}
	}
}
