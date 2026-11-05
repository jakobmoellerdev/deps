package gradle

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

func TestLockRoots(t *testing.T) {
	root := t.TempDir()
	for f, c := range map[string]string{
		// multi-project build with a locked root and subproject
		"settings.gradle": "include 'app'\n", "build.gradle": "", "gradle/libs.versions.toml": "",
		"gradle.lockfile": "", "app/build.gradle": "", "app/gradle.lockfile": "",
		// nested build whose root project has no lockfile
		"tools/settings.gradle.kts": "", "tools/build.gradle.kts": "", "tools/lib/build.gradle.kts": "",
		"tools/lib/buildscript-gradle.lockfile": "", "tools/lib/gradle.lockfile": "",
		// verification only, no lockfiles
		"verified/settings.gradle": "", "verified/build.gradle": "", "verified/gradle/verification-metadata.xml": "<x/>",
		// no locking at all
		"plain/build.gradle": "",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"app/build.gradle", "gradle/libs.versions.toml", "build.gradle", "settings.gradle",
		"tools/lib/build.gradle.kts", "tools/build.gradle.kts", "verified/build.gradle", "verified/settings.gradle", "plain/build.gradle"}
	got := New().(manager.LockMaintainer).LockRoots(root, files)
	want := []manager.LockRoot{
		{Manifest: "settings.gradle", Lockfile: "gradle.lockfile"},
		{Manifest: "tools/build.gradle.kts", Lockfile: "tools/lib/buildscript-gradle.lockfile"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("LockRoots = %v\nwant %v", got, want)
	}
}
