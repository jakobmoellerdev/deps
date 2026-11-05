package gradle

import (
	"fmt"
	"testing"
)

// BenchmarkMatchKotlinPaths matches the path mix of a Kotlin/Android repo, where .kt
// sources outnumber build files by far.
func BenchmarkMatchKotlinPaths(b *testing.B) {
	var paths []string
	for i := range 200 {
		paths = append(paths,
			fmt.Sprintf("feature%d/src/main/java/com/example/feature%d/ui/Screen%d.kt", i, i, i),
			fmt.Sprintf("feature%d/src/test/java/com/example/Screen%dTest.kt", i, i),
			fmt.Sprintf("app/src/main/kotlin/com/example/Model%d.kt", i),
			fmt.Sprintf("feature%d/build.gradle.kts", i),
		)
	}
	paths = append(paths, "buildSrc/src/main/kotlin/Versions.kt",
		"build-logic/convention/src/main/kotlin/AndroidConvention.kt", "gradle/libs.versions.toml")
	m := New()
	for b.Loop() {
		for _, p := range paths {
			m.Match(p)
		}
	}
}
