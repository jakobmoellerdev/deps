package engine

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// git runs git in dir and returns its trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgSign=false", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo makes dir a repository with files and one commit and returns the commit.
func repo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	return git(t, dir, "rev-parse", "HEAD")
}

// repoState describes the repository of the work tree dir: objects, refs, worktrees, status
// and index.
func repoState(t *testing.T, dir string) string {
	t.Helper()
	index, err := os.ReadFile(git(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return git(t, dir, "count-objects", "-v") + git(t, dir, "for-each-ref") + git(t, dir, "worktree", "list", "--porcelain") +
		git(t, dir, "status", "--porcelain") + string(index)
}

// From a linked worktree with --dir below its top: the commands run in --dir of a copy of the
// whole work tree, the .gitignore files above --dir apply, changes outside --dir are left
// out, paths stay relative to --dir, and the repository is not written to.
func TestBuildWithTasksSubdir(t *testing.T) {
	main := t.TempDir()
	repo(t, main, map[string]string{".gitignore": "*.log\n", "mod/a.txt": "a\n", "other.txt": "o\n"})
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, main, "worktree", "add", "-q", "--detach", wt)
	before := repoState(t, main)
	ups := []dep.Update{{Tasks: dep.Tasks{Commands: []string{"test -e ../other.txt && echo o2 > ../other.txt && echo x > debug.log && echo gen > gen.txt && git status --porcelain > status.log"}}}}
	changes := []FileChange{{Path: "a.txt", Mode: 0o644, Old: []byte("a\n"), New: []byte("b\n")}}
	got, err := BuildWithTasks(t.Context(), filepath.Join(wt, "mod"), git(t, wt, "rev-parse", "HEAD"), ups, changes, TaskAllowlist{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "a.txt" || string(got[0].New) != "b\n" || string(got[0].Old) != "a\n" ||
		got[1].Path != "gen.txt" || string(got[1].New) != "gen\n" || got[1].Old != nil {
		t.Errorf("changes = %+v", got)
	}
	if repoState(t, main) != before {
		t.Error("the build changed the repository")
	}
}

// Initialized submodules are repositories in the copy too, but their content never becomes a
// change; an uninitialized one stays an empty directory; moving a submodule's HEAD fails.
func TestBuildWithTasksSubmodules(t *testing.T) {
	sub := t.TempDir()
	repo(t, sub, map[string]string{"s.txt": "s\n"})
	super := t.TempDir()
	repo(t, super, map[string]string{"p.txt": "p\n"})
	git(t, super, "submodule", "add", "-q", sub, "sub")
	git(t, super, "submodule", "add", "-q", sub, "sub2")
	git(t, super, "commit", "-qm", "submodules")
	git(t, super, "submodule", "deinit", "-q", "-f", "sub2")
	base := git(t, super, "rev-parse", "HEAD")
	before, beforeSub := repoState(t, super), repoState(t, filepath.Join(super, "sub"))

	ups := []dep.Update{{Tasks: dep.Tasks{Commands: []string{
		"git -C sub status --porcelain | grep -q . && exit 1; echo gen > gen.txt && echo x > sub/inside.txt && echo y > sub2/inside.txt",
		"git submodule status | grep -q '^ [0-9a-f]* sub ' && git submodule status | grep -q '^-[0-9a-f]* sub2$' && git -C sub add -A",
	}}}}
	got, err := BuildWithTasks(t.Context(), super, base, ups, nil, TaskAllowlist{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "gen.txt" || string(got[0].New) != "gen\n" {
		t.Errorf("changes = %+v", got)
	}

	ups[0].Tasks.Commands = []string{"cd sub && git -c user.name=t -c user.email=t@t -c commit.gpgSign=false commit -q --allow-empty -m moved"}
	if _, err := BuildWithTasks(t.Context(), super, base, ups, nil, TaskAllowlist{"*"}); err == nil || err.Error() != "postUpgradeTasks: "+ups[0].Tasks.Commands[0]+" changed submodule sub; submodule updates are not supported" {
		t.Errorf("err = %v", err)
	}
	if repoState(t, super) != before || repoState(t, filepath.Join(super, "sub")) != beforeSub {
		t.Error("the build changed a repository")
	}
}

// Update-mode commands run once per update, in the group's order and with DEPS_* variables,
// each seeing the previous runs' output; branch-mode commands run once, after them. A failing
// update-mode run names its dep.
func TestBuildWithTasksExecutionModes(t *testing.T) {
	dir := t.TempDir()
	base := repo(t, dir, map[string]string{"a.txt": "a\n"})
	line := dep.Tasks{Commands: []string{`echo "$DEPS_DEP_NAME=$DEPS_CURRENT_VERSION>$DEPS_NEW_VERSION $DEPS_MANAGER $DEPS_UPDATE_TYPE" >> deps.txt`}, Update: true}
	branch := dep.Tasks{Commands: []string{"echo branch >> deps.txt", "echo branch >> deps.txt"}}
	ups := []dep.Update{
		{Dep: dep.Dep{Name: "c", Manager: "npm"}, Tasks: branch},
		{Dep: dep.Dep{Name: "a", Manager: "npm", Version: "1", Locked: "1.5"}, Type: dep.Minor, NewVersion: "2", Lockfile: true, Tasks: line},
		{Dep: dep.Dep{Name: "b", Manager: "gomod", Version: "v1"}, Type: dep.Major, NewVersion: "v3", Tasks: line},
	}
	got, err := BuildWithTasks(t.Context(), dir, base, ups, nil, TaskAllowlist{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "a=1.5>2 npm minor\nb=v1>v3 gomod major\nbranch\n"; len(got) != 1 || got[0].Path != "deps.txt" || string(got[0].New) != want {
		t.Errorf("changes = %+v, want deps.txt %q", got, want)
	}

	ups[2].Tasks = dep.Tasks{Commands: []string{`test "$DEPS_DEP_NAME" != b`}, Update: true}
	if _, err := BuildWithTasks(t.Context(), dir, base, ups, nil, TaskAllowlist{"*"}); err == nil || !strings.HasPrefix(err.Error(), `postUpgradeTasks: test "$DEPS_DEP_NAME" != b (b): exit status 1`) {
		t.Errorf("err = %v", err)
	}
}

func TestTaskAllowlist(t *testing.T) {
	runs := func(cmds ...string) []TaskRun {
		var rs []TaskRun
		for _, c := range cmds {
			rs = append(rs, TaskRun{Command: c})
		}
		return rs
	}
	for _, tc := range []struct {
		allow TaskAllowlist
		cmd   string
		ok    bool
	}{
		{TaskAllowlist{"go generate ./..."}, "go generate ./...", true},
		{TaskAllowlist{"go generate ./..."}, "go generate ./... && curl x", false},
		{TaskAllowlist{"go generate ./..."}, "  go   generate\t./...  ", true},
		{TaskAllowlist{"make *"}, "make generate manifests", true},
		{TaskAllowlist{"make *"}, "gmake generate", false},
		{TaskAllowlist{"make * docs"}, "make api docs", true},
		{TaskAllowlist{"make * docs"}, "make docs", false},
		{TaskAllowlist{"*"}, "rm -rf /tmp/x; echo hi", true},
		{TaskAllowlist{"make *"}, "make x; curl y", false},
		{TaskAllowlist{"make *"}, "make x && curl y", false},
		{TaskAllowlist{"make *"}, "make x | sh", false},
		{TaskAllowlist{"make *"}, "make `id`", false},
		{TaskAllowlist{"make *"}, "make $(id)", false},
		{TaskAllowlist{"make *"}, "make x\ncurl y", false},
		{TaskAllowlist{"make *"}, "make X=$HOME docs > out.txt", true},
		{TaskAllowlist{"echo * >> deps.txt"}, "echo $DEPS_DEP_NAME >> deps.txt", true},
		{TaskAllowlist{"echo * >> deps.txt"}, "echo a; rm -rf x >> deps.txt", false},
		{TaskAllowlist{"echo *; echo *"}, "echo a; echo b", true},
		{TaskAllowlist{"echo *; echo *"}, "echo a; echo b; echo c", false},
		{TaskAllowlist{"go *", "make *"}, "make", false},
		{nil, "make", false},
	} {
		if err := tc.allow.Check(runs(tc.cmd)); (err == nil) != tc.ok {
			t.Errorf("%q allows %q: %v, want %v", tc.allow, tc.cmd, err, tc.ok)
		}
	}
	err := TaskAllowlist{"make *"}.Check(runs("make gen", "go  generate ./..."))
	if !errors.Is(err, ErrTasksNotAllowed) || err.Error() != `postUpgradeTasks command not allowed: "go  generate ./..."; allow it with --allow-tasks 'go generate ./...' (or DEPS_ALLOW_TASKS)` {
		t.Errorf("err = %v", err)
	}
}

// The postUpgradeTasks example of the configuration reference passes its own allowlist.
func TestTaskAllowlistDocExample(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "config", "schema.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, sec, _ := strings.Cut(string(doc), "\n## Rule.postUpgradeTasks\n")
	sec, _, _ = strings.Cut(sec, "\n## ")
	var allow TaskAllowlist
	_, with, _ := strings.Cut(sec, "```\n\nwith `deps run")
	for _, m := range regexp.MustCompile(`--allow-tasks '([^']*)'`).FindAllStringSubmatch(with, -1) {
		allow = append(allow, m[1])
	}
	var runs []TaskRun
	for _, m := range regexp.MustCompile(`(?m)^\s+(?:postUpgradeTasks|commands): \[(?:"([^"]*)"|'([^']*)')\]$`).FindAllStringSubmatch(sec, -1) {
		runs = append(runs, TaskRun{Command: m[1] + m[2]})
	}
	if len(allow) != 2 || len(runs) != 2 {
		t.Fatalf("example not found: allow %q, runs %v", allow, runs)
	}
	if err := allow.Check(runs); err != nil {
		t.Error(err)
	}
}

// files limit what the tasks add to the build's changes, as the union over the task entries;
// elsewhere the build's changes stay.
func TestBuildWithTasksFiles(t *testing.T) {
	dir := t.TempDir()
	base := repo(t, dir, map[string]string{"a.txt": "a\n", "keep.txt": "k\n"})
	changes := []FileChange{{Path: "a.txt", Mode: 0o644, Old: []byte("a\n"), New: []byte("b\n")}}
	ups := []dep.Update{
		{Dep: dep.Dep{Name: "x"}, Tasks: dep.Tasks{Commands: []string{
			"mkdir gen && echo x > gen/x.txt && echo x > gen/x.bak && echo o > other.txt && echo c > a.txt && rm keep.txt",
		}, Files: []string{"gen/**", "!**/*.bak"}}},
		{Dep: dep.Dep{Name: "y"}, Tasks: dep.Tasks{Commands: []string{"echo y > y.txt"}, Files: []string{"y.txt"}}},
	}
	got, err := BuildWithTasks(t.Context(), dir, base, ups, changes, TaskAllowlist{"*"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range got {
		paths = append(paths, c.Path+"="+string(c.New))
	}
	if want := []string{"a.txt=b\n", "gen/x.txt=x\n", "y.txt=y\n"}; !slices.Equal(paths, want) {
		t.Errorf("changes = %q, want %q", paths, want)
	}
}

// apply keeps what files allow: other files the commands changed below --dir return to their
// state before them (the build's or the user's changes included); ignored files stay.
func TestApplyTasksFiles(t *testing.T) {
	dir := t.TempDir()
	repo(t, dir, map[string]string{".gitignore": "*.log\n", "a.txt": "a\n", "keep.txt": "k\n", "dirty.txt": "d0\n", "run.sh": "x\n"})
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "run.sh")
	git(t, dir, "commit", "-qm", "exec")
	for name, content := range map[string]string{"a.txt": "b\n", "dirty.txt": "d1\n", "mine.txt": "m\n"} { // the build's and the user's changes
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ups := []dep.Update{{Tasks: dep.Tasks{Commands: []string{
		"mkdir gen && echo x > gen/x.txt && echo o > other.txt && echo c > a.txt && rm keep.txt run.sh && echo d2 > dirty.txt && echo l > x.log && echo m2 > mine.txt",
	}, Files: []string{"gen/**"}}}}
	if err := ApplyTasks(t.Context(), dir, ups); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"gen/x.txt": "x\n", "other.txt": "", "a.txt": "b\n", "keep.txt": "k\n", "run.sh": "x\n", "dirty.txt": "d1\n", "mine.txt": "m\n", "x.log": "l\n"} {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if want == "" && !errors.Is(err, fs.ErrNotExist) || want != "" && string(got) != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "run.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("run.sh restored without its executable bit: %v %v", fi, err)
	}
}
