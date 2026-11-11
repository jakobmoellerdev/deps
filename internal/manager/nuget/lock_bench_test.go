package nuget

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
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

// repoTree writes a repository of projects projects, each with sources and a
// packages.lock.json, and assets files of documentation, test data and a web frontend that
// hold no project.
func repoTree(tb testing.TB, projects, assets int) string {
	tb.Helper()
	root := solution(tb, projects)
	for i := range assets {
		for _, d := range []string{"docs", "tests/data", "web/public"} {
			p := filepath.Join(root, filepath.FromSlash(fmt.Sprintf("%s/g%d/f%d.txt", d, i%20, i)))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				tb.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
				tb.Fatal(err)
			}
		}
	}
	return root
}

// BenchmarkRestoreMirror builds the mirror a restore of a repository of 50 projects and 18000
// other files runs in.
func BenchmarkRestoreMirror(b *testing.B) {
	root := repoTree(b, 50, 6000)
	files := map[string][]byte{"src/P025/packages.lock.json": []byte("{\"version\": 1}\n"), "src/P049/packages.lock.json": []byte("{\"version\": 1}\n")}
	b.ReportAllocs()
	for b.Loop() {
		tmp, _, err := toolrun.Mirror(root, mirrorOptions(root, files))
		if err != nil {
			b.Fatal(err)
		}
		if err := os.RemoveAll(tmp); err != nil {
			b.Fatal(err)
		}
	}
}
