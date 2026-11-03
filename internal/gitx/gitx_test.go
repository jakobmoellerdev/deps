package gitx

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
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
	c, err := r.Commit(ctx, parent, changes, "msg title\n\nbody\n", "Bot", "bot@example.com")
	if err != nil {
		t.Fatal(err)
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
	tree := git(t, clone, "ls-tree", "-r", c)
	if !strings.Contains(tree, "100755 blob") || !strings.Contains(tree, "\tsub/run.sh") {
		t.Errorf("tree lacks executable sub/run.sh:\n%s", tree)
	}
	if !strings.Contains(tree, "100644 blob") {
		t.Errorf("tree lacks regular files:\n%s", tree)
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

	if _, err := r.Commit(ctx, "0000000000000000000000000000000000000000", changes, "m", "n", "e@x"); err == nil {
		t.Error("Commit with unknown parent succeeded")
	}
	if _, err := (Repo{Dir: t.TempDir()}).Commit(ctx, parent, changes, "m", "n", "e@x"); err == nil {
		t.Error("Commit outside a repo succeeded")
	}
}

func TestPushDeleteAuthor(t *testing.T) {
	origin, clone := setup(t)
	ctx := t.Context()
	r := Repo{Dir: clone}
	parent := git(t, clone, "rev-parse", "HEAD")
	c1, err := r.Commit(ctx, parent, []engine.FileChange{{Path: "f", Mode: 0o644, New: []byte("1")}}, "one", "Bot", "bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := r.Commit(ctx, parent, []engine.FileChange{{Path: "f", Mode: 0o644, New: []byte("2")}}, "two", "Bot", "bot@example.com")
	if err != nil {
		t.Fatal(err)
	}
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
	if err := r.Push(ctx, c2, br, ""); err == nil {
		t.Fatal("create over existing branch succeeded")
	}
	if err := r.Push(ctx, c2, br, parent); err == nil {
		t.Fatal("push with wrong expected succeeded")
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
	email, err := r.BranchAuthorEmail(ctx, br)
	if err != nil || email != "bot@example.com" {
		t.Fatalf("BranchAuthorEmail = %q, %v", email, err)
	}
	if s := git(t, clone, "rev-parse", "--is-shallow-repository"); s != "false" {
		t.Fatalf("full clone became shallow: %s", s)
	}

	// Shallow clone.
	shallow := filepath.Join(t.TempDir(), "shallow")
	git(t, clone, "clone", "-q", "--depth=1", "file://"+origin, shallow)
	if s := git(t, shallow, "rev-parse", "--is-shallow-repository"); s != "true" {
		t.Fatalf("clone not shallow: %s", s)
	}
	email, err = (Repo{Dir: shallow}).BranchAuthorEmail(ctx, br)
	if err != nil || email != "bot@example.com" {
		t.Fatalf("shallow BranchAuthorEmail = %q, %v", email, err)
	}

	if err := r.DeleteBranch(ctx, br); err != nil {
		t.Fatal(err)
	}
	if tip() != "" {
		t.Fatalf("branch still exists at %s", tip())
	}
	if _, err := r.BranchAuthorEmail(ctx, br); err == nil {
		t.Fatal("BranchAuthorEmail of deleted branch succeeded")
	}
	if _, err := (Repo{Dir: t.TempDir()}).BranchAuthorEmail(ctx, br); err == nil {
		t.Fatal("BranchAuthorEmail outside a repo succeeded")
	}
}
