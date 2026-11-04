package terraform

import (
	"strings"
	"testing"
)

// Fixture, copied unchanged: testdata/bfd/opentofu-version is .opentofu-version of
// github.com/CMSgov/beneficiary-fhir-data@ac57ee8 (CC0-1.0), renamed without the leading dot.

func TestOpenTofuVersionFile(t *testing.T) {
	const tofu = "opentofu-version github-releases opentofu/opentofu https://github.com/opentofu/opentofu "
	for _, tt := range []struct{ src, want string }{
		{string(read(t, "bfd/opentofu-version")), "1 " + tofu + "1.12.1 hashicorp"},
		{"\n v1.8 \n", "2 " + tofu + "v1.8 hashicorp"},
		{"1.6.0-rc1\n", "1 " + tofu + "1.6.0-rc1 hashicorp"},
		{"latest\n", "1 " + tofu + "latest hashicorp [non-version value]"},
		{"latest:^1.8\n", "1 " + tofu + "latest:^1.8 hashicorp [non-version value]"},
		{"min-required\n", "1 " + tofu + "min-required hashicorp [non-version value]"},
		{"latest-allowed\n", "1 " + tofu + "latest-allowed hashicorp [non-version value]"},
		{"", ""},
	} {
		got := extract(t, NewVersion(), "ops/services/.opentofu-version", []byte(tt.src))
		if strings.Join(got, "\n") != tt.want {
			t.Errorf("%q: got %v, want %s", tt.src, got, tt.want)
		}
	}
}

func TestVersionFileMatch(t *testing.T) {
	m := NewVersion()
	for p, want := range map[string]bool{
		".terraform-version":             true,
		".opentofu-version":              true,
		"ops/services/.opentofu-version": true,
		"opentofu-version":               false,
		".opentofu-version.bak":          false,
		".tofu-version":                  false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
}
