package jvmtool

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// gradleBuild writes a Gradle build of projects subprojects with sources, an .idea directory
// and a node_modules of a bundled frontend, and no lockfile: the common case, in which Scope
// has to look at the whole build before it reports that there is nothing to lock.
func gradleBuild(tb testing.TB, projects int) string {
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
	write("settings.gradle", "rootProject.name = 'bench'\n")
	write("build.gradle", "plugins { id 'java' }\n")
	for p := range projects {
		write(fmt.Sprintf("p%d/build.gradle", p), "dependencies { implementation 'org.example:lib:1.0' }\n")
		for f := range 20 {
			write(fmt.Sprintf("p%d/src/main/java/com/example/p%d/C%d.java", p, p, f), "package com.example;\n")
			write(fmt.Sprintf("p%d/src/test/java/com/example/p%d/C%dTest.java", p, p, f), "package com.example;\n")
		}
	}
	for i := range 500 {
		write(fmt.Sprintf("frontend/node_modules/pkg%d/index.js", i), "module.exports = {};\n")
		write(fmt.Sprintf(".idea/libraries/lib%d.xml", i), "<component/>\n")
	}
	return root
}

// BenchmarkGradleScope resolves the scope of the 30 build files of a build patched in one
// run, as the engine does once per patched file.
func BenchmarkGradleScope(b *testing.B) {
	root := gradleBuild(b, 40)
	manifests := make([]string, 0, 30)
	for p := range 30 {
		manifests = append(manifests, fmt.Sprintf("p%d/build.gradle", p))
	}
	b.ReportAllocs()
	for b.Loop() {
		l := GradleLocker{Scopes: NewScopeCache()}
		for _, m := range manifests {
			if dir, ok := l.Scope(b.Context(), root, m); ok || dir != "." {
				b.Fatalf("Scope(%s) = %q, %v", m, dir, ok)
			}
		}
	}
}
