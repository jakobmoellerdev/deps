package nodetool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// pnpmMonorepo writes a pnpm workspace of n members and returns its root and their manifest
// paths. The root package.json carries a realistic set of fields, so an uncached lookup pays
// its read and parse per member.
func pnpmMonorepo(tb testing.TB, n int) (root string, manifests []string) {
	tb.Helper()
	root = tb.TempDir()
	var deps, globs strings.Builder
	for i := range 120 {
		fmt.Fprintf(&deps, "%q: \"^1.2.%d\",\n", fmt.Sprintf("@scope/pkg-%d", i), i)
	}
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
	write("package.json", `{"name": "monorepo", "private": true, "devDependencies": {`+strings.TrimSuffix(deps.String(), ",\n")+"}}")
	write("pnpm-workspace.yaml", "packages:\n"+globs.String())
	write("pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	for i := range n {
		p := fmt.Sprintf("packages/m%d/package.json", i)
		write(p, fmt.Sprintf(`{"name": "m%d", "dependencies": {"left-pad": "^1.3.0"}}`, i))
		manifests = append(manifests, p)
	}
	return root, manifests
}

// BenchmarkLockerScope looks the lock root of every member of a 300 member workspace up, as
// the engine does for the patched files of one build.
func BenchmarkLockerScope(b *testing.B) {
	root, manifests := pnpmMonorepo(b, 300)
	ctx := context.Background()
	for _, cached := range []bool{false, true} {
		name := "uncached"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			l := Locker{}
			if cached {
				l.Scopes = NewScopes()
			}
			b.ReportAllocs()
			for b.Loop() {
				for _, m := range manifests {
					if dir, ok := l.Scope(ctx, root, m); !ok || dir != "." {
						b.Fatalf("Scope(%s) = %q, %v", m, dir, ok)
					}
				}
			}
		})
	}
}

// TestLockerScopeConcurrent: the engine post-processes scopes in parallel, so one Locker
// answers from several goroutines at once.
func TestLockerScopeConcurrent(t *testing.T) {
	root, manifests := pnpmMonorepo(t, 8)
	l := Locker{Scopes: NewScopes()}
	var wg sync.WaitGroup
	for _, m := range manifests {
		wg.Go(func() {
			if dir, ok := l.Scope(t.Context(), root, m); !ok || dir != "." {
				t.Errorf("Scope(%s) = %q, %v", m, dir, ok)
			}
		})
	}
	wg.Wait()
}
