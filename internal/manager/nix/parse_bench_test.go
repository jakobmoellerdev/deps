package nix

import (
	"fmt"
	"strings"
	"testing"
)

// packageSet is a generated package set (nixpkgs' node-packages.nix, haskell package sets) of
// n fetchurl sets, the shape whose line numbers parseSets counts.
func packageSet(n int) []byte {
	var sb strings.Builder
	sb.WriteString("{ fetchurl }:\n\n{\n")
	for i := range n {
		fmt.Fprintf(&sb, `  "pkg-%d" = {
    name = "pkg-%d";
    version = "1.%d.0";
    src = fetchurl {
      url = "https://registry.npmjs.org/pkg-%d/-/pkg-%d-1.%d.0.tgz";
      sha256 = "%064x";
    };
    # A comment, so that the set is not only bindings.
    meta = { description = "package %d"; };
  };
`, i, i, i, i, i, i, i, i)
	}
	sb.WriteString("}\n")
	return []byte(sb.String())
}

// BenchmarkParseSets parses a 5000-package set, which holds 15000 download bindings.
func BenchmarkParseSets(b *testing.B) {
	src := packageSet(5000)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		if sets := parseSets(src); len(sets) == 0 {
			b.Fatal("no sets")
		}
	}
}
