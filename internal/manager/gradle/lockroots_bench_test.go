package gradle

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkLockRootsMultiModule runs LockRoots over one build of 60 modules with 200 source
// files each, every module contributing a build script and a lockfile.
func BenchmarkLockRootsMultiModule(b *testing.B) {
	root := b.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	write("settings.gradle.kts")
	write("build.gradle.kts")
	write("gradle.lockfile")
	files := []string{"settings.gradle.kts", "build.gradle.kts"}
	for m := range 60 {
		mod := fmt.Sprintf("mod%02d", m)
		write(mod + "/build.gradle.kts")
		write(mod + "/gradle.lockfile")
		for f := range 200 {
			write(fmt.Sprintf("%s/src/main/java/p%d/F%d.java", mod, f%10, f))
		}
		files = append(files, mod+"/build.gradle.kts")
	}
	m := &gradleManager{}
	for b.Loop() {
		if got := m.LockRoots(root, files); len(got) != 1 {
			b.Fatalf("LockRoots = %v", got)
		}
	}
}
