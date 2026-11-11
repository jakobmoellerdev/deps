package nuget

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// solution writes a repository of projects projects with sources, two of which keep a
// packages.lock.json.
func solution(tb testing.TB, projects int) string {
	tb.Helper()
	root := tb.TempDir()
	write := func(p, content string) {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	write("Directory.Packages.props", "<Project><ItemGroup><PackageVersion Include=\"Serilog\" Version=\"4.0.0\" /></ItemGroup></Project>")
	for p := range projects {
		dir := fmt.Sprintf("src/P%03d", p)
		write(dir+fmt.Sprintf("/P%03d.csproj", p), "<Project Sdk=\"Microsoft.NET.Sdk\" />")
		for f := range 20 {
			write(fmt.Sprintf("%s/C%d.cs", dir, f), "namespace P;\n")
		}
		if p == projects/2 || p == projects-1 {
			write(dir+"/packages.lock.json", "{\"version\": 1}\n")
		}
	}
	return root
}

// BenchmarkLockerScope resolves the scope of the 30 MSBuild files patched in one run, as the
// engine does once per patched file.
func BenchmarkLockerScope(b *testing.B) {
	root := solution(b, 200)
	manifests := make([]string, 0, 30)
	for p := range 30 {
		manifests = append(manifests, fmt.Sprintf("src/P%03d/P%03d.csproj", p, p))
	}
	b.ReportAllocs()
	for b.Loop() {
		l := Locker{Locks: NewLockCache()}
		for _, m := range manifests {
			if dir, ok := l.Scope(b.Context(), root, m); !ok || dir != "." {
				b.Fatalf("Scope(%s) = %q, %v", m, dir, ok)
			}
		}
	}
}
