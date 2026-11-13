package npm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// monorepo writes a pnpm workspace of n members below a new directory and returns it with the
// member package.json paths. The root package.json carries a realistic set of fields, so the
// per-manifest read and parse of the lock root is the cost the benchmark measures.
func monorepo(tb testing.TB, n int) (root string, files []string) {
	tb.Helper()
	root = tb.TempDir()
	var deps strings.Builder
	for i := range 120 {
		fmt.Fprintf(&deps, "%q: \"^1.2.%d\",\n", fmt.Sprintf("@scope/pkg-%d", i), i)
	}
	var globs strings.Builder
	for i := range n {
		fmt.Fprintf(&globs, "  - packages/m%d\n", i)
	}
	write := func(p, c string) {
		tb.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	write("package.json", `{"name": "monorepo", "private": true, "packageManager": "pnpm@9.1.0", "devDependencies": {`+
		strings.TrimSuffix(deps.String(), ",\n")+"}}")
	write("pnpm-workspace.yaml", "packages:\n"+globs.String())
	write("pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	for i := range n {
		p := fmt.Sprintf("packages/m%d/package.json", i)
		write(p, fmt.Sprintf(`{"name": "m%d", "dependencies": {"left-pad": "^1.3.0"}}`, i))
		files = append(files, p)
	}
	return root, files
}

func BenchmarkLockRoots(b *testing.B) {
	for _, n := range []int{10, 300} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			root, files := monorepo(b, n)
			m := New().(manager.LockMaintainer)
			b.ReportAllocs()
			for b.Loop() {
				if got := m.LockRoots(root, files); len(got) != 1 {
					b.Fatalf("lock roots = %v", got)
				}
			}
		})
	}
}
