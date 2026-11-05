package cargo

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// BenchmarkReadLocks reads the ripgrep lock for a workspace of 8 member manifests with 30
// registry requirements each.
func BenchmarkReadLocks(b *testing.B) {
	root := b.TempDir()
	lock, err := os.ReadFile("testdata/ripgrep.Cargo.lock")
	if err != nil {
		b.Fatal(err)
	}
	var members, deps strings.Builder
	for i := range 30 {
		fmt.Fprintf(&deps, "c%d = \"1\"\n", i)
	}
	files := map[string]string{"Cargo.lock": string(lock)}
	var names []string
	for i := range 8 {
		name := fmt.Sprintf("crates/m%d/Cargo.toml", i)
		names = append(names, name)
		files[name] = "[dependencies]\n" + deps.String()
		fmt.Fprintf(&members, "%q, ", fmt.Sprintf("crates/m%d", i))
	}
	files["Cargo.toml"] = "[workspace]\nmembers = [" + members.String() + "]\n"
	write(b, root, files)
	src := []byte("[dependencies]\n" + deps.String())
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		in := make([][]dep.Dep, 0, len(names))
		for _, n := range names {
			ds, err := cargoManager{}.Extract(n, src, manager.Env{})
			if err != nil {
				b.Fatal(err)
			}
			in = append(in, ds)
		}
		b.StartTimer()
		cargoManager{}.ReadLocks(root, in)
	}
}
