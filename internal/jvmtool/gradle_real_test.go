package jvmtool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGradleLockerMaintainReal runs Gradle from PATH (network: Maven Central): a dynamic
// version locked at an older release moves to the newest it admits, build files unchanged.
func TestGradleLockerMaintainReal(t *testing.T) {
	if testing.Short() {
		t.Skip("needs network")
	}
	if _, err := exec.LookPath("gradle"); err != nil {
		t.Skip("gradle not in PATH")
	}
	if checkJava() != nil {
		t.Skip("java not found")
	}
	build := "plugins { id 'java' }\nrepositories { mavenCentral() }\ndependencyLocking { lockAllConfigurations() }\n" +
		"dependencies { implementation 'org.slf4j:slf4j-api:2.0.+' }\n"
	old := "# This is a Gradle generated file for dependency locking.\n# Manual edits can break the build and are not advised.\n# This file is expected to be part of source control.\n" +
		"org.slf4j:slf4j-api:2.0.1=compileClasspath,runtimeClasspath\nempty=annotationProcessor\n"
	dir := t.TempDir()
	write(t, dir, map[string]string{"settings.gradle": "rootProject.name = 'x'\n", "build.gradle": build, Lockfile: old})
	p := GradleLocker{}
	files := map[string][]byte{}
	for _, n := range p.ScopeSiblings(dir) {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		files[n] = b
	}
	out, err := p.Maintain(t.Context(), dir, files)
	if err != nil {
		t.Fatal(err)
	}
	lock := string(out[Lockfile])
	t.Logf("before:\n%s\nafter:\n%s", old, lock)
	if !strings.Contains(lock, "org.slf4j:slf4j-api:2.0.") || strings.Contains(lock, "slf4j-api:2.0.1=") {
		t.Errorf("lockfile not moved: %q", lock)
	}
	if len(out) != 1 {
		t.Errorf("changed %v", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "build.gradle")); string(b) != build {
		t.Error("build.gradle changed")
	}
}
