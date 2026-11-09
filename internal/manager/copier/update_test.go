package copier

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// write creates files (slash paths relative to root).
func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		f := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// git runs git in dir with a fixed identity.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestScope(t *testing.T) {
	if New().Name() != Name {
		t.Errorf("Name() = %q", New().Name())
	}
	for manifest, want := range map[string]string{".copier-answers.yml": ".", "a/b/.copier-answers.ci.yaml": "a/b"} {
		if dir, ok := (Updater{}).Scope(t.Context(), "/", manifest); !ok || dir != want {
			t.Errorf("Scope(%q) = %q, %v", manifest, dir, ok)
		}
	}
	if _, ok := (Updater{}).Scope(t.Context(), "/", "copier.yml"); ok {
		t.Error("Scope(copier.yml) ok")
	}
	if s := (Updater{}).Siblings(); s != nil {
		t.Errorf("Siblings() = %v", s)
	}
	if tr := (Updater{}).Trees("/x"); !slices.Equal(tr, []string{"."}) {
		t.Errorf("Trees() = %v", tr)
	}
}

// stub installs a fake copier on PATH (with git) that logs its arguments and the answers file
// it sees, then plays a template update: it writes the --vcs-ref as `_commit`, rewrites
// README.md, adds new.txt and deletes old.txt. With STUB_FAIL set it fails, with STUB_NOOP it
// changes nothing, with STUB_LINK it replaces README.md by a symlink, with STUB_CONFLICT it
// records README.md as unmerged, as Copier records inline conflicts.
func stub(t *testing.T) (log string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("needs git")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := `#!/bin/sh
echo "args: $*" >> "$STUB_LOG"
answers= ref=
while [ $# -gt 0 ]; do
  case "$1" in
    --answers-file) answers="$2"; shift ;;
    --vcs-ref) ref="$2"; shift ;;
  esac
  shift
done
while IFS= read -r l; do echo "  $l"; done < "$answers" >> "$STUB_LOG"
[ -n "$(git status --porcelain)" ] && echo "dirty" >> "$STUB_LOG"
[ -e build ] && echo "build copied" >> "$STUB_LOG"
[ -e untracked.txt ] || echo "untracked.txt missing" >> "$STUB_LOG"
[ -L link ] || echo "link missing" >> "$STUB_LOG"
[ -e gone.txt ] && echo "gone.txt copied" >> "$STUB_LOG"
if [ -n "$STUB_FAIL" ]; then
  echo "UnsafeTemplateError: Template uses potentially unsafe feature: tasks. ($PWD)"
  exit 1
fi
[ -n "$STUB_NOOP" ] && exit 0
printf '_commit: %s\n_src_path: gh:o/tpl\n' "$ref" > "$answers"
echo "readme $ref" > README.md
echo new > new.txt
rm -f old.txt
[ -n "$STUB_LINK" ] && rm README.md && ln -s new.txt README.md
if [ -n "$STUB_CONFLICT" ]; then
  h=$(git hash-object -w README.md)
  printf '0 0000000000000000000000000000000000000000\tREADME.md\n100644 %s 1\tREADME.md\n100644 %s 2\tREADME.md\n100644 %s 3\tREADME.md\n' "$h" "$h" "$h" | git update-index --index-info
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "copier"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+filepath.Dir(gitBin))
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_FAIL", "")
	t.Setenv("STUB_NOOP", "")
	t.Setenv("STUB_LINK", "")
	t.Setenv("STUB_CONFLICT", "")
	return log
}

// project writes a Copier project with answers file at a commit of 1.0.0 into a git
// repository, with an ignored build directory, a symlink, a tracked file deleted from the work
// tree and an untracked file, and returns its directory.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{
		".copier-answers.yml": "_commit: 1.0.0\n_src_path: gh:o/tpl\n", "README.md": "readme 1.0.0\n", "old.txt": "old\n",
		".gitignore": "build/\n", "build/out.bin": "x", "src/main.py": "print()\n", "gone.txt": "gone\n",
	}
	maps.Copy(all, files)
	write(t, dir, all)
	if err := os.Symlink("README.md", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{"untracked.txt": "u\n"})
	return dir
}

func TestProcessStub(t *testing.T) {
	log := stub(t)
	dir := project(t, map[string]string{".copier-answers.ci.yml": "_commit: 2.0.0\n_src_path: gh:o/ci\n"})
	out, err := Updater{}.Process(t.Context(), dir, map[string][]byte{
		".copier-answers.yml":    []byte("_commit: 1.1.0\n_src_path: gh:o/tpl\n"),
		".copier-answers.ci.yml": []byte("_commit: 2.1.0\n_src_path: gh:o/ci\n"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		".copier-answers.yml": "_commit: 1.1.0\n_src_path: gh:o/tpl\n", ".copier-answers.ci.yml": "_commit: 2.1.0\n_src_path: gh:o/tpl\n",
		"README.md": "readme 1.1.0\n", "new.txt": "new\n",
	}
	if len(out) != len(want)+1 || out["old.txt"] != nil {
		t.Errorf("out = %q", out)
	}
	for p, s := range want {
		if string(out[p]) != s {
			t.Errorf("%s = %q, want %q", p, out[p], s)
		}
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	// The answers files are updated in path order, each from the file in the checkout (the
	// old _commit) in a clean repository; the build directory git ignores is not copied.
	wantLog := "args: update --skip-answered --defaults --answers-file .copier-answers.ci.yml --vcs-ref 2.1.0\n" +
		"  _commit: 2.0.0\n  _src_path: gh:o/ci\n" +
		"args: update --skip-answered --defaults --answers-file .copier-answers.yml --vcs-ref 1.1.0\n" +
		"  _commit: 1.0.0\n  _src_path: gh:o/tpl\n"
	if got != wantLog {
		t.Errorf("log:\n%s\nwant:\n%s", got, wantLog)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err == nil {
		t.Error("the checkout was changed")
	}
}

// TestProcessStubNoGit updates a project outside a git repository: every file but .git is
// copied.
func TestProcessStubNoGit(t *testing.T) {
	stub(t)
	dir := t.TempDir()
	write(t, dir, map[string]string{".copier-answers.yml": "_commit: 1.0.0\n_src_path: gh:o/tpl\n", "README.md": "readme 1.0.0\n"})
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	out, err := Updater{}.Process(t.Context(), dir, map[string][]byte{".copier-answers.yml": []byte("_commit: 1.1.0\n")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out["README.md"]) != "readme 1.1.0\n" || string(out["new.txt"]) != "new\n" {
		t.Errorf("out = %q", out)
	}
}

func TestProcessStubErrors(t *testing.T) {
	stub(t)
	dir := project(t, nil)
	patched := map[string][]byte{".copier-answers.yml": []byte("_commit: 1.1.0\n_src_path: gh:o/tpl\n")}
	for env, want := range map[string]string{
		"STUB_FAIL":     "copier update in " + dir + ": UnsafeTemplateError: Template uses potentially unsafe feature: tasks. (" + dir + ")",
		"STUB_NOOP":     "copier update left .copier-answers.yml in " + dir + " unchanged",
		"STUB_LINK":     "copier update made README.md no regular file, which deps cannot write",
		"STUB_CONFLICT": "copier update of .copier-answers.yml to 1.1.0 in " + dir + " left merge conflicts in README.md",
	} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "1")
			if _, err := (Updater{}).Process(t.Context(), dir, patched, nil); err == nil || err.Error() != want {
				t.Errorf("err = %v, want %s", err, want)
			}
		})
	}
	if _, err := (Updater{}).Process(t.Context(), dir, map[string][]byte{".copier-answers.yml": []byte("_src_path: x\n")}, nil); err == nil || err.Error() != ".copier-answers.yml: no _commit" {
		t.Errorf("no _commit: %v", err)
	}
	if _, err := (Updater{}).Process(t.Context(), dir, map[string][]byte{".copier-answers.yml": []byte("[\n")}, nil); err == nil {
		t.Error("invalid YAML: no error")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := (Updater{}).Process(t.Context(), dir, patched, nil); err == nil || err.Error() != "copier not found (needed to update from Copier templates)" {
		t.Errorf("no copier: %v", err)
	}
}

func TestCopierArgvUVX(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "uvx"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	argv, err := copierArgv()
	if err != nil || !slices.Equal(argv, []string{filepath.Join(bin, "uvx"), "--from", "copier", "copier"}) {
		t.Errorf("copierArgv() = %q, %v", argv, err)
	}
}

// template creates a template repository with tags 1.0.0 and 1.1.0: 1.1.0 changes README.md,
// adds new.txt and deletes old.txt.
func template(t *testing.T) string {
	t.Helper()
	tpl := t.TempDir()
	write(t, tpl, map[string]string{
		"copier.yml":                          "name:\n  type: str\n  default: demo\n_templates_suffix: .jinja\n",
		"{{_copier_conf.answers_file}}.jinja": "{{ _copier_answers|to_nice_yaml -}}\n",
		"README.md.jinja":                     "# {{ name }}\n\nline 1\nline 2\nversion 1\n",
		"old.txt":                             "old\n",
		"src/{{ name }}.py.jinja":             "NAME = '{{ name }}'\n",
	})
	git(t, tpl, "init", "-q")
	git(t, tpl, "add", "-A")
	git(t, tpl, "commit", "-qm", "v1")
	git(t, tpl, "tag", "1.0.0")
	write(t, tpl, map[string]string{"README.md.jinja": "# {{ name }}\n\nline 1\nline 2\nversion 2\n", "new.txt": "new\n"})
	if err := os.Remove(filepath.Join(tpl, "old.txt")); err != nil {
		t.Fatal(err)
	}
	git(t, tpl, "add", "-A")
	git(t, tpl, "commit", "-qm", "v2")
	git(t, tpl, "tag", "1.1.0")
	return tpl
}

// realProject generates a project from tpl at 1.0.0 with the real copier and commits it.
func realProject(t *testing.T, tpl string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "copier", "copy", "--defaults", "--vcs-ref", "1.0.0", tpl, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("copier copy: %v\n%s", err, out)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

// realCopier skips the test unless copier and git are on PATH.
func realCopier(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"copier", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s", tool)
		}
	}
}

// TestProcessReal updates a project with the real copier: the template's changes and the new
// _commit come back, a local edit is kept.
func TestProcessReal(t *testing.T) {
	realCopier(t)
	tpl := template(t)
	dir := realProject(t, tpl)
	write(t, dir, map[string]string{"src/demo.py": "NAME = 'demo'\nEXTRA = 1\n"})
	git(t, dir, "commit", "-qam", "local edit")
	answers, err := os.ReadFile(filepath.Join(dir, ".copier-answers.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(answers), "_commit: 1.0.0\n") {
		t.Fatalf("answers:\n%s", answers)
	}
	patched := strings.Replace(string(answers), "_commit: 1.0.0", "_commit: 1.1.0", 1)
	out, err := Updater{}.Process(context.Background(), dir, map[string][]byte{".copier-answers.yml": []byte(patched)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out[".copier-answers.yml"]) != patched || string(out["README.md"]) != "# demo\n\nline 1\nline 2\nversion 2\n" ||
		string(out["new.txt"]) != "new\n" || out["old.txt"] != nil || len(out) != 4 {
		t.Errorf("out = %q", out)
	}
}

// TestProcessRealConflict fails an update whose template change conflicts with a local edit.
func TestProcessRealConflict(t *testing.T) {
	realCopier(t)
	tpl := template(t)
	dir := realProject(t, tpl)
	write(t, dir, map[string]string{"README.md": "# demo\n\nline 1\nline 2\nversion local\n"})
	git(t, dir, "commit", "-qam", "local edit")
	answers, err := os.ReadFile(filepath.Join(dir, ".copier-answers.yml"))
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(answers), "_commit: 1.0.0", "_commit: 1.1.0", 1)
	_, err = Updater{}.Process(t.Context(), dir, map[string][]byte{".copier-answers.yml": []byte(patched)}, nil)
	if want := "copier update of .copier-answers.yml to 1.1.0 in " + dir + " left merge conflicts in README.md"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}
