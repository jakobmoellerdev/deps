package datasource

import (
	"path/filepath"
	"testing"
)

func TestRootOf(t *testing.T) {
	root := filepath.FromSlash("/x")
	for _, tc := range []struct{ file, dir, want string }{
		{"package.json", filepath.FromSlash("/x/y"), filepath.FromSlash("/x/y")},
		{"y/package.json", filepath.FromSlash("/x/y"), root},
		{"a/b/package.json", filepath.FromSlash("/x/y"), filepath.FromSlash("/x/y")},
	} {
		if got := RootOf(tc.file, tc.dir); got != tc.want {
			t.Errorf("RootOf(%q, %q) = %q, want %q", tc.file, tc.dir, got, tc.want)
		}
	}
}
