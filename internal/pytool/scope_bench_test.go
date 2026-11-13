package pytool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// uvWorkspaceTree writes a uv workspace of n members and returns its root and their
// pyproject.toml paths. The root pyproject.toml carries a realistic dependency list, so an
// uncached lookup pays its read and TOML parse per member.
func uvWorkspaceTree(tb testing.TB, n int) (root string, manifests []string) {
	tb.Helper()
	root = tb.TempDir()
	var deps, members strings.Builder
	for i := range 120 {
		fmt.Fprintf(&deps, "  \"pkg-%d>=1.%d.0\",\n", i, i)
	}
	for i := range n {
		fmt.Fprintf(&members, "  \"packages/m%d\",\n", i)
	}
	put := func(p, c string) {
		tb.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	put("pyproject.toml", "[project]\nname = \"root\"\ndependencies = [\n"+deps.String()+"]\n\n[tool.uv.workspace]\nmembers = [\n"+members.String()+"]\n")
	put("uv.lock", "version = 1\n")
	for i := range n {
		p := fmt.Sprintf("packages/m%d/pyproject.toml", i)
		put(p, fmt.Sprintf("[project]\nname = \"m%d\"\ndependencies = []\n", i))
		manifests = append(manifests, p)
	}
	return root, manifests
}

// BenchmarkLockerScope looks the lock directory of every member of a 100 member uv workspace
// up, as the engine does for the patched files of one build.
func BenchmarkLockerScope(b *testing.B) {
	root, manifests := uvWorkspaceTree(b, 100)
	ctx := context.Background()
	for _, cached := range []bool{false, true} {
		name := "uncached"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			l := Locker{}
			if cached {
				l.Scopes = NewScopeCache()
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
	root, manifests := uvWorkspaceTree(t, 8)
	l := Locker{Scopes: NewScopeCache()}
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
