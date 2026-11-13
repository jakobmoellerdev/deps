package cargo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// bigLock is a Cargo.lock of n crates.io packages, the size of a large workspace's.
func bigLock(n int) string {
	names := make([]string, 0, n)
	for i := range n {
		names = append(names, fmt.Sprintf("crate-%d@1.%d.0", i, i%40))
	}
	return lockOf(names...)
}

// stubCargo puts a cargo on PATH that only appends a line to Cargo.lock, so the benchmark
// measures what process does around the commands, not cargo itself.
func stubCargo(tb testing.TB) {
	tb.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		tb.Skip("sh not in PATH")
	}
	bin := tb.TempDir()
	script := "#!" + sh + "\necho \"# $*\" >> Cargo.lock\n"
	write(tb, bin, map[string]string{"cargo": script})
	if err := os.Chmod(filepath.Join(bin, "cargo"), 0o755); err != nil {
		tb.Fatal(err)
	}
	tb.Setenv("PATH", bin)
}

// BenchmarkProcessLocked moves 50 lockfile-only updates of a workspace with a 2000 package
// lockfile, with cargo stubbed: the cost measured is the reading and parsing of Cargo.lock
// around the commands. "moved" needs a command per update, "settled" none, as the first
// `cargo update --workspace` already left the versions the updates ask for.
func BenchmarkProcessLocked(b *testing.B) {
	const crates = 2000
	for _, tt := range []struct{ name, newVersion string }{{"moved", "1.%d.9"}, {"settled", "1.%d.0"}} {
		b.Run(tt.name, func(b *testing.B) {
			stubCargo(b)
			dir := b.TempDir()
			lock := bigLock(crates)
			write(b, dir, map[string]string{"Cargo.toml": "[workspace]\nmembers = []\n", "Cargo.lock": lock})
			var ups []dep.Update
			for i := range 50 {
				ups = append(ups, dep.Update{
					Dep:        dep.Dep{File: "Cargo.toml", Name: fmt.Sprintf("crate-%d", i), Datasource: Datasource, Locked: fmt.Sprintf("1.%d.0", i%40), Range: "1"},
					NewVersion: fmt.Sprintf(tt.newVersion, i%40),
				})
			}
			locks := map[string][]dep.Update{"Cargo.toml": ups}
			files := map[string][]byte{lockFile: []byte(lock)}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := (Locker{}).ProcessLocked(b.Context(), dir, nil, files, locks); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
