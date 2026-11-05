package cargo

import (
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// TestDepTablesOrderAndStop pins the document order of depTables and that it stops as soon as
// the consumer breaks, at every position (top-level, target, workspace, patch, replace).
func TestDepTablesOrderAndStop(t *testing.T) {
	src := []byte(`
dependencies = "not a table"
[package]
name = "x"
[dev-dependencies]
a = "1"
[target.'cfg(unix)'.dependencies]
b = "1"
[target.'cfg(unix)'.build-dependencies]
c = "1"
[workspace.dependencies]
d = "1"
[patch.crates-io]
e = { path = "e" }
[patch.other]
f = "1"
[replace]
"g:1.0.0" = { path = "g" }
`)
	root, err := tomlspan.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for typ, tab := range depTables(root) {
		got = append(got, typ+":"+tab.Keys[0])
	}
	want := []string{
		DepTypeDev + ":a", DepTypeDependencies + ":b", DepTypeBuild + ":c", DepTypeWorkspace + ":d",
		DepTypePatch + ":e", DepTypePatch + ":f", DepTypeReplace + ":g:1.0.0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("depTables = %q, want %q", got, want)
	}
	for n := 1; n <= len(want); n++ {
		var prefix []string
		for typ, tab := range depTables(root) {
			prefix = append(prefix, typ+":"+tab.Keys[0])
			if len(prefix) == n {
				break
			}
		}
		if !slices.Equal(prefix, want[:n]) {
			t.Errorf("break after %d: got %q", n, prefix)
		}
	}
}
