package engine

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// statScopePost scopes a manifest to the nearest ancestor directory holding a lockfile, as
// the npm post-processor does, statting below root; calls counts the Scope calls.
type statScopePost struct {
	postFunc
	calls *int
}

var statScopeLocks = []string{"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock"}

func (p statScopePost) Scope(_ context.Context, root, manifest string) (string, bool) {
	*p.calls++
	for dir := path.Dir(manifest); ; dir = path.Dir(dir) {
		for _, l := range statScopeLocks {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), l)); err == nil {
				return dir, true
			}
		}
		if dir == "." {
			return "", false
		}
	}
}

// scopeOnceFixture writes files package.json manifests below a workspace with one lockfile
// and returns groups groups of perGroup updates spread over those manifests.
func scopeOnceFixture(tb testing.TB, files, groups, perGroup int) (string, [][]dep.Update) {
	tb.Helper()
	root := tb.TempDir()
	writeScopeOnce(tb, root, "ws/package-lock.json")
	for i := range files {
		writeScopeOnce(tb, root, fmt.Sprintf("ws/packages/p%d/package.json", i))
	}
	out := make([][]dep.Update, groups)
	for g := range out {
		for u := range perGroup {
			f := fmt.Sprintf("ws/packages/p%d/package.json", (g*perGroup+u)%files)
			out[g] = append(out[g], dep.Update{Dep: dep.Dep{Manager: "statscope", File: f, Name: fmt.Sprintf("d%d", u)}})
		}
	}
	return root, out
}

func writeScopeOnce(tb testing.TB, root, name string) {
	tb.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		tb.Fatal(err)
	}
}

func TestInputsScopeOncePerFile(t *testing.T) {
	root, groups := scopeOnceFixture(t, 3, 4, 6)
	var calls int
	post := map[string]PostProcessor{"statscope": statScopePost{calls: &calls}}
	got, errs := Inputs(t.Context(), root, nil, post, groups)
	for g, err := range errs {
		if err != nil {
			t.Fatal(g, err)
		}
		if len(got[g].Paths) != 4 || got[g].Paths[0] != "ws" {
			t.Fatalf("group %d inputs = %v", g, got[g].Paths)
		}
	}
	if calls != 3 {
		t.Fatalf("Scope calls = %d, want one per distinct file (3)", calls)
	}
}

func BenchmarkInputsScopeOnce(b *testing.B) {
	root, groups := scopeOnceFixture(b, 20, 40, 15)
	var calls int
	post := map[string]PostProcessor{"statscope": statScopePost{calls: &calls}}
	b.ReportAllocs()
	for b.Loop() {
		if _, errs := Inputs(b.Context(), root, nil, post, groups); errs[0] != nil {
			b.Fatal(errs[0])
		}
	}
}
