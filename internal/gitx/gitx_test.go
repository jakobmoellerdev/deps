package gitx

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// isolate makes git independent of the user's configuration.
func isolate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Tester")
	t.Setenv("GIT_AUTHOR_EMAIL", "tester@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Tester")
	t.Setenv("GIT_COMMITTER_EMAIL", "tester@example.com")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setup returns a bare origin holding one commit on main (README, sub/a.txt) and a full clone.
func setup(t *testing.T) (origin, clone string) {
	t.Helper()
	isolate(t)
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	clone = filepath.Join(root, "clone")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	git(t, root, "init", "-q", "-b", "main", seed)
	write(t, filepath.Join(seed, "README"), "readme\n")
	write(t, filepath.Join(seed, "sub", "a.txt"), "a\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-q", "-m", "init")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "push", "-q", "origin", "main")
	git(t, root, "clone", "-q", origin, clone)
	return origin, clone
}

func TestReadOps(t *testing.T) {
	origin, clone := setup(t)
	r := Repo{Dir: clone}
	ctx := t.Context()

	head, err := r.RevParse(ctx, "HEAD")
	if err != nil || head != git(t, clone, "rev-parse", "HEAD") {
		t.Fatalf("RevParse = %q, %v", head, err)
	}
	if _, err := r.RevParse(ctx, "nope"); err == nil || !strings.HasPrefix(err.Error(), "git rev-parse: ") {
		t.Fatalf("RevParse(nope) err = %v", err)
	}

	url, err := r.RemoteURL(ctx)
	if err != nil || url != origin {
		t.Fatalf("RemoteURL = %q, %v; want %q", url, err, origin)
	}

	git(t, origin, "update-ref", "refs/heads/deps/x", head)
	refs, err := r.LsRemote(ctx, "refs/heads/deps/*")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs["refs/heads/deps/x"] != head {
		t.Fatalf("LsRemote = %v", refs)
	}

	if _, err := (Repo{Dir: t.TempDir()}).LsRemote(ctx, "x"); err == nil {
		t.Fatal("LsRemote outside a repo succeeded")
	}
}

func TestFetchBranchAndWorktree(t *testing.T) {
	origin, clone := setup(t)
	ctx := t.Context()
	// release is a commit on origin the clone has not fetched.
	seed := filepath.Join(filepath.Dir(origin), "seed")
	git(t, seed, "checkout", "-q", "-b", "release")
	write(t, filepath.Join(seed, "sub", "a.txt"), "release\n")
	git(t, seed, "commit", "-qam", "release")
	git(t, seed, "push", "-q", "origin", "release")
	want := git(t, seed, "rev-parse", "HEAD")

	r := Repo{Dir: filepath.Join(clone, "sub")}
	sha, err := r.FetchBranch(ctx, "release")
	if err != nil || sha != want {
		t.Fatalf("FetchBranch = %q, %v; want %q", sha, err, want)
	}
	tree := filepath.Join(t.TempDir(), "tree")
	if err := r.AddWorktree(ctx, tree, sha); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(tree, "sub", "a.txt")); err != nil || string(b) != "release\n" {
		t.Fatalf("work tree file = %q, %v", b, err)
	}
	if got := git(t, tree, "rev-parse", "HEAD"); got != want {
		t.Errorf("work tree HEAD = %s, want %s", got, want)
	}
	if err := r.RemoveWorktree(ctx, tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Errorf("work tree still there: %v", err)
	}
	if _, err := r.FetchBranch(ctx, "gone"); err == nil {
		t.Error("FetchBranch of a missing branch succeeded")
	}
	if err := r.AddWorktree(ctx, tree, "nope"); err == nil {
		t.Error("AddWorktree of a missing commit succeeded")
	}
}

func TestDefaultBranchAndReadFile(t *testing.T) {
	origin, clone := setup(t)
	ctx := t.Context()
	r := Repo{Dir: filepath.Join(clone, "sub")}
	if b, err := r.DefaultBranch(ctx); err != nil || b != "main" {
		t.Fatalf("DefaultBranch = %q, %v", b, err)
	}
	head := git(t, clone, "rev-parse", "HEAD")
	// Leading blank lines and the trailing newline are kept: positions in the file must not move.
	seed := filepath.Join(filepath.Dir(origin), "seed")
	write(t, filepath.Join(seed, "sub", "a.txt"), "\n\na\n")
	git(t, seed, "commit", "-qam", "blank lines")
	git(t, seed, "push", "-q", "origin", "main")
	tip, err := r.FetchBranch(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ rev, want string }{{head, "a\n"}, {tip, "\n\na\n"}} {
		if b, err := r.ReadFile(ctx, tc.rev, "a.txt"); err != nil || string(b) != tc.want {
			t.Errorf("ReadFile(%s) = %q, %v; want %q", tc.rev, b, err, tc.want)
		}
	}
	if _, err := r.ReadFile(ctx, head, "gone.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile of a missing file: %v", err)
	}
	if _, err := (Repo{Dir: t.TempDir()}).DefaultBranch(ctx); err == nil {
		t.Error("DefaultBranch without origin succeeded")
	}
	// An origin without HEAD (an empty bare repository) has no default branch.
	empty := filepath.Join(t.TempDir(), "empty.git")
	git(t, "", "init", "-q", "--bare", empty)
	git(t, clone, "remote", "set-url", "origin", empty)
	if b, err := r.DefaultBranch(ctx); err == nil {
		t.Errorf("DefaultBranch of an empty origin = %q", b)
	}
}

func TestObjectIDs(t *testing.T) {
	_, clone := setup(t)
	ctx := t.Context()
	head := git(t, clone, "rev-parse", "HEAD")
	want := []string{
		git(t, clone, "rev-parse", head+"^{tree}"),
		git(t, clone, "rev-parse", head+":sub"),
		git(t, clone, "rev-parse", head+":sub/a.txt"),
		"",
	}
	got, err := Repo{Dir: clone}.ObjectIDs(ctx, head, []string{".", "sub", "sub/a.txt", "sub/missing file"})
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("ObjectIDs = %q, %v; want %q", got, err, want)
	}
	// Paths are relative to Dir, not to the top of the work tree.
	got, err = Repo{Dir: filepath.Join(clone, "sub")}.ObjectIDs(ctx, head, []string{".", "a.txt"})
	if err != nil || !slices.Equal(got, want[1:3]) {
		t.Fatalf("ObjectIDs in sub = %q, %v; want %q", got, err, want[1:3])
	}
	if got, err := (Repo{Dir: clone}).ObjectIDs(ctx, head, nil); got != nil || err != nil {
		t.Fatalf("ObjectIDs(nil) = %q, %v", got, err)
	}
	if _, err := (Repo{Dir: clone}).ObjectIDs(ctx, head, []string{"a\nb"}); err == nil {
		t.Fatal("ObjectIDs accepted a path with a line break")
	}
	if _, err := (Repo{Dir: t.TempDir()}).ObjectIDs(ctx, head, []string{"."}); err == nil {
		t.Fatal("ObjectIDs outside a repo succeeded")
	}
}

func TestCommit(t *testing.T) {
	_, clone := setup(t)
	ctx := t.Context()
	parent := git(t, clone, "rev-parse", "HEAD")
	indexBefore, err := os.ReadFile(filepath.Join(clone, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	// Dir is a subdirectory: paths are relative to it.
	r := Repo{Dir: filepath.Join(clone, "sub")}
	changes := []engine.FileChange{
		{Path: "a.txt", Mode: 0o644, New: []byte("changed\n")},
		{Path: "run.sh", Mode: 0o755, New: []byte("#!/bin/sh\n")},
	}
	tree, err := r.WriteTree(ctx, parent, changes)
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Commit(ctx, parent, changes, "msg title\n\nbody\n", "Bot", "bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, clone, "rev-parse", c+"^{tree}"); got != tree {
		t.Errorf("commit tree = %s, want %s", got, tree)
	}

	if got := git(t, clone, "rev-parse", c+"^"); got != parent {
		t.Errorf("parent = %s, want %s", got, parent)
	}
	if got := git(t, clone, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%B", c); got != "Bot <bot@example.com>|Bot <bot@example.com>|msg title\n\nbody" {
		t.Errorf("identity/message = %q", got)
	}
	if got := git(t, clone, "show", c+":sub/a.txt"); got != "changed" {
		t.Errorf("sub/a.txt = %q", got)
	}
	if got := git(t, clone, "show", c+":README"); got != "readme" {
		t.Errorf("README = %q (unchanged file lost)", got)
	}
	files := git(t, clone, "ls-tree", "-r", c)
	if !strings.Contains(files, "100755 blob") || !strings.Contains(files, "\tsub/run.sh") {
		t.Errorf("tree lacks executable sub/run.sh:\n%s", files)
	}
	if !strings.Contains(files, "100644 blob") {
		t.Errorf("tree lacks regular files:\n%s", files)
	}

	// Working tree, real index and HEAD untouched.
	if got, _ := os.ReadFile(filepath.Join(clone, "sub", "a.txt")); string(got) != "a\n" {
		t.Errorf("working tree a.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(clone, "sub", "run.sh")); !os.IsNotExist(err) {
		t.Errorf("run.sh created in working tree: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(clone, ".git", "index")); !bytes.Equal(after, indexBefore) {
		t.Error("real index modified")
	}
	if s := git(t, clone, "status", "--porcelain"); s != "" {
		t.Errorf("status not clean: %q", s)
	}
	if got := git(t, clone, "rev-parse", "HEAD"); got != parent {
		t.Errorf("HEAD moved to %s", got)
	}

	// A change without new content deletes the file.
	del, err := Repo{Dir: clone}.Commit(ctx, parent, []engine.FileChange{{Path: "README", Mode: 0o644, Old: []byte("readme\n")}}, "rm", "Bot", "bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, clone, "ls-tree", "-r", "--name-only", del); got != "sub/a.txt" {
		t.Errorf("tree after deleting README = %q", got)
	}

	if _, err := r.Commit(ctx, "0000000000000000000000000000000000000000", changes, "m", "n", "e@x"); err == nil {
		t.Error("Commit with unknown parent succeeded")
	}
	if _, err := r.WriteTree(ctx, "0000000000000000000000000000000000000000", changes); err == nil {
		t.Error("WriteTree with unknown parent succeeded")
	}
	if _, err := (Repo{Dir: t.TempDir()}).WriteTree(ctx, parent, changes); err == nil {
		t.Error("WriteTree outside a repo succeeded")
	}
}

// mkCommit commits changes on top of parent.
func mkCommit(t *testing.T, r Repo, parent string, changes []engine.FileChange, msg string) string {
	t.Helper()
	c, err := r.Commit(t.Context(), parent, changes, msg, "Bot", "bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGitlinks(t *testing.T) {
	_, clone := setup(t)
	ctx := t.Context()
	const old, next = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	git(t, clone, "update-index", "--add", "--cacheinfo", "160000,"+old+",sub/lib")
	git(t, clone, "commit", "-q", "-m", "add submodule")
	parent := git(t, clone, "rev-parse", "HEAD")

	// Commit records the new commit without having its objects.
	r := Repo{Dir: filepath.Join(clone, "sub")}
	c, err := r.Commit(ctx, parent, []engine.FileChange{{Path: "lib", Gitlink: true, Old: []byte(old), New: []byte(next)}}, "m", "Bot", "bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, clone, "ls-tree", c, "sub/lib"); got != "160000 commit "+next+"\tsub/lib" {
		t.Errorf("ls-tree = %q", got)
	}
	if got := git(t, clone, "ls-files", "--stage", "sub/lib"); !strings.HasPrefix(got, "160000 "+old) {
		t.Errorf("real index changed by Commit: %q", got)
	}

	if err := r.StageGitlink(ctx, "lib", next); err != nil {
		t.Fatal(err)
	}
	if got := git(t, clone, "ls-files", "--stage", "sub/lib"); !strings.HasPrefix(got, "160000 "+next) {
		t.Errorf("staged gitlink = %q", got)
	}
}

func TestPushDeleteAuthor(t *testing.T) {
	origin, clone := setup(t)
	ctx := t.Context()
	r := Repo{Dir: clone}
	parent := git(t, clone, "rev-parse", "HEAD")
	c1 := mkCommit(t, r, parent, []engine.FileChange{{Path: "f", Mode: 0o644, New: []byte("1")}}, "one")
	c2 := mkCommit(t, r, parent, []engine.FileChange{{Path: "f", Mode: 0o644, New: []byte("2")}}, "two")
	const br = "deps/x"
	tip := func() string {
		out, _ := exec.Command("git", "-C", origin, "rev-parse", "--verify", "-q", "refs/heads/"+br).Output()
		return strings.TrimSpace(string(out))
	}

	if err := r.Push(ctx, c1, br, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if tip() != c1 {
		t.Fatalf("tip = %s, want %s", tip(), c1)
	}
	if err := r.Push(ctx, c2, br, ""); !errors.Is(err, ErrStale) {
		t.Fatalf("create over existing branch: %v", err)
	}
	if err := r.Push(ctx, c2, br, parent); !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "[rejected] (stale info)") {
		t.Fatalf("push with wrong expected: %v", err)
	}
	if tip() != c1 {
		t.Fatalf("rejected push moved tip to %s", tip())
	}
	if err := r.Push(ctx, c2, br, c1); err != nil {
		t.Fatalf("update: %v", err)
	}
	if tip() != c2 {
		t.Fatalf("tip = %s, want %s", tip(), c2)
	}

	// Full clone: fetch must not make it shallow.
	ct, err := strconv.ParseInt(git(t, clone, "log", "-1", "--format=%ct", c2), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	want := platform.Tip{Author: "bot@example.com", Parents: []string{parent}, Tree: git(t, clone, "rev-parse", c2+"^{tree}"), Date: time.Unix(ct, 0)}
	if got, err := r.BranchTip(ctx, br); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("BranchTip = %+v, %v; want %+v", got, err, want)
	}
	if s := git(t, clone, "rev-parse", "--is-shallow-repository"); s != "false" {
		t.Fatalf("full clone became shallow: %s", s)
	}

	// Shallow clone: the fetched tip is grafted, but its recorded parent is still reported.
	shallow := filepath.Join(t.TempDir(), "shallow")
	git(t, clone, "clone", "-q", "--depth=1", "file://"+origin, shallow)
	if s := git(t, shallow, "rev-parse", "--is-shallow-repository"); s != "true" {
		t.Fatalf("clone not shallow: %s", s)
	}
	if got, err := (Repo{Dir: shallow}).BranchTip(ctx, br); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("shallow BranchTip = %+v, %v; want %+v", got, err, want)
	}

	if err := r.DeleteBranch(ctx, br); err != nil {
		t.Fatal(err)
	}
	if tip() != "" {
		t.Fatalf("branch still exists at %s", tip())
	}
	if _, err := r.BranchTip(ctx, br); err == nil {
		t.Fatal("BranchTip of deleted branch succeeded")
	}
	if _, err := (Repo{Dir: t.TempDir()}).BranchTip(ctx, br); err == nil {
		t.Fatal("BranchTip outside a repo succeeded")
	}

	// A push origin declines (branch protection) is ErrRejected, not ErrStale.
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'protected branch' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, c1, "main", parent); !errors.Is(err, ErrRejected) || errors.Is(err, ErrStale) {
		t.Fatalf("declined push: %v", err)
	}
	if err := (Repo{Dir: t.TempDir()}).Push(ctx, c1, br, ""); err == nil || errors.Is(err, ErrStale) || errors.Is(err, ErrRejected) {
		t.Fatalf("push outside a repo: %v", err)
	}
}

func TestParseCommitHeader(t *testing.T) {
	for _, tc := range []struct {
		name, obj string
		want      platform.Tip
	}{
		{"root", "tree t\nauthor A B <a@b> 1 +0000\ncommitter C <c@d> 1700000000 +0100\n\nmsg\n", platform.Tip{Author: "a@b", Tree: "t", Date: time.Unix(1700000000, 0)}},
		{"malformed committer", "tree t\ncommitter C <c@d> soon\ncommitter C\n", platform.Tip{Tree: "t"}},
		{"merge", "tree t\nparent p1\nparent p2\nauthor A <a@b> 1 +0000\n\nparent fake\n", platform.Tip{Author: "a@b", Parents: []string{"p1", "p2"}, Tree: "t"}},
		{"malformed author", "tree t\nparent p\nauthor nobody\n", platform.Tip{Parents: []string{"p"}, Tree: "t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCommitHeader(tc.obj); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClone(t *testing.T) {
	origin, _ := setup(t)
	dir := filepath.Join(t.TempDir(), "c")
	if err := Clone(t.Context(), "file://"+origin, dir, "", "https://git.example.com/", "AUTHORIZATION: basic eDp5"); err != nil {
		t.Fatal(err)
	}
	r := Repo{Dir: dir}
	head, err := r.RevParse(t.Context(), "HEAD")
	if err != nil || head != git(t, origin, "rev-parse", "main") {
		t.Errorf("HEAD = %q, %v", head, err)
	}
	if got := git(t, dir, "config", "--get", "http.https://git.example.com/.extraheader"); got != "AUTHORIZATION: basic eDp5" {
		t.Errorf("extraheader = %q", got)
	}
	if u, err := r.RemoteURL(t.Context()); err != nil || u != "file://"+origin {
		t.Errorf("origin = %q, %v", u, err)
	}
	if git(t, dir, "rev-parse", "--is-shallow-repository") != "true" {
		t.Error("clone is not shallow")
	}
	if err := Clone(t.Context(), "file://"+origin, filepath.Join(t.TempDir(), "d"), "nope", "", ""); err == nil || !strings.Contains(err.Error(), "git fetch") {
		t.Errorf("missing branch: %v", err)
	}
}
