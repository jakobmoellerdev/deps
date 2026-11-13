package jvmtool

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// write creates files (slash paths relative to dir) with their content.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// toolEnv sets PATH to a new directory holding a java stub (unless noJava) and clears
// JAVA_HOME; it returns the directory and a log file for stubs.
func toolEnv(t *testing.T, noJava bool) (bin, log string) {
	t.Helper()
	bin = t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	t.Setenv("PATH", bin)
	t.Setenv("JAVA_HOME", "")
	for _, k := range []string{"STUB_FAIL", "STUB_URL", "STUB_EXTRA"} {
		t.Setenv(k, "")
	}
	if !noJava {
		script(t, filepath.Join(bin, "java"), log, "")
	}
	return bin, log
}

// stubTools are find and sort, resolved at package init: toolEnv replaces PATH before the
// stubs are written.
var stubTools = func() (tools [2]string) {
	tools[0], _ = exec.LookPath("find")
	tools[1], _ = exec.LookPath("sort")
	return tools
}()

// script writes an executable shell stub at p: it appends "argv: <name> <args>" and one
// "file: <path>" line per file of its working directory to log, then runs body.
func script(t *testing.T, p, log, body string) {
	t.Helper()
	find, sort := stubTools[0], stubTools[1]
	if find == "" || sort == "" {
		t.Skip("find or sort not in PATH")
	}
	s := `#!/bin/sh
{
  printf 'argv:'
  printf ' %s' "${0##*/}" "$@"
  printf '\n'
  '` + find + `' . -type f | '` + sort + `' | while read -r f; do printf 'file: %s\n' "$f"; done
} >> '` + log + `'
` + body + "\n"
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
}

// logged returns the argv lines and the file lines of log; none when no stub ran.
func logged(t *testing.T, log string) (argv, files []string) {
	t.Helper()
	b, err := os.ReadFile(log)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		t.Fatal(err)
	}
	for l := range strings.Lines(string(b)) {
		l = strings.TrimSuffix(l, "\n")
		if a, ok := strings.CutPrefix(l, "argv: "); ok {
			argv = append(argv, a)
		} else if f, ok := strings.CutPrefix(l, "file: ./"); ok {
			files = append(files, f)
		}
	}
	return argv, files
}

func TestCheckJava(t *testing.T) {
	bin, log := toolEnv(t, true)
	if err := checkJava(); !errors.Is(err, errNoJava) {
		t.Fatalf("no java: %v", err)
	}
	home := t.TempDir()
	script(t, filepath.Join(home, "bin", "java"), log, "")
	t.Setenv("JAVA_HOME", home)
	if err := checkJava(); err != nil {
		t.Errorf("JAVA_HOME: %v", err)
	}
	t.Setenv("JAVA_HOME", t.TempDir())
	script(t, filepath.Join(bin, "java"), log, "")
	if err := checkJava(); err != nil {
		t.Errorf("PATH with a JAVA_HOME without java: %v", err)
	}
}

func TestCopyBuild(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"a.txt": "a", "sub/b.txt": "b", "skip/c.txt": "c", "m.txt": "old"})
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	root, tmp, err := copyBuild(dir, map[string][]byte{"m.txt": []byte("new"), "n/new.txt": []byte("n")}, func(p string, _ os.DirEntry) bool {
		return filepath.Base(p) == "skip"
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if filepath.Base(tmp) != filepath.Base(dir) {
		t.Errorf("copy named %s", tmp)
	}
	for p, want := range map[string]string{"a.txt": "a", "sub/b.txt": "b", "m.txt": "new", "n/new.txt": "n", "link": "a"} {
		if b, err := os.ReadFile(filepath.Join(tmp, p)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", p, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "skip")); err == nil {
		t.Error("skipped directory copied")
	}
	if st, _ := os.Stat(filepath.Join(tmp, "a.txt")); st == nil || st.Mode().Perm() != 0o755 {
		t.Errorf("mode not kept: %v", st)
	}
	if target, err := os.Readlink(filepath.Join(tmp, "link")); err != nil || target != "a.txt" {
		t.Errorf("symlink = %q, %v", target, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "m.txt")); string(b) != "old" {
		t.Error("working tree touched")
	}
}

// The copy leaves out node_modules and the dot directories of tools at any depth, which hold
// no build input; .mvn (the Maven wrapper's configuration) and the build's own files stay.
func TestCopyBuildToolDirs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"pom.xml":                               "<project/>",
		"sub/pom.xml":                           "<project/>",
		"sub/src/App.java":                      "class App {}",
		"sub/node_modules/pkg/index.js":         "x",
		"sub/target/classes/App.class":          "c",
		".idea/workspace.xml":                   "<x/>",
		".mvn/jvm.config":                       "-Xmx1g",
		".mvn/wrapper/maven-wrapper.properties": "distributionUrl=x",
	})
	root, tmp, err := copyBuild(dir, nil, mavenSkip)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	for _, p := range []string{"pom.xml", "sub/pom.xml", "sub/src/App.java", ".mvn/jvm.config", ".mvn/wrapper/maven-wrapper.properties"} {
		if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s not copied: %v", p, err)
		}
	}
	for _, p := range []string{"sub/node_modules", "sub/target", ".idea"} {
		if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(p))); err == nil {
			t.Errorf("%s copied", p)
		}
	}
}
