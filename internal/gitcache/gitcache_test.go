package gitcache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.invalid", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// origin creates a repository with one commit on branch main and returns its path.
func origin(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "--initial-branch=main", dir)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "file.txt")
	run(t, dir, "commit", "--quiet", "-m", "one")
	return dir
}

func TestCheckoutReusesMirrorAndFetchesNewCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := origin(t, "v1")
	c := &Cache{Root: filepath.Join(t.TempDir(), "git")}
	ctx := context.Background()

	first, done1, err := c.Checkout(ctx, "run1", src, "example.com", "o/n", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Base != "main" {
		t.Errorf("default branch = %q, want main", first.Base)
	}
	if b, err := os.ReadFile(filepath.Join(first.Work, "file.txt")); err != nil || string(b) != "v1" {
		t.Fatalf("work tree content = %q, %v", b, err)
	}

	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, src, "commit", "--quiet", "-am", "two")

	second, done2, err := c.Checkout(ctx, "run2", src, "example.com", "o/n", "main", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Mirror != first.Mirror {
		t.Errorf("mirror not reused: %s != %s", second.Mirror, first.Mirror)
	}
	if second.Work == first.Work {
		t.Error("work tree path reused between cycles")
	}
	if b, err := os.ReadFile(filepath.Join(second.Work, "file.txt")); err != nil || string(b) != "v2" {
		t.Fatalf("second work tree content = %q, %v", b, err)
	}
	// The mirror is shallow: one commit, whatever the history of origin.
	if got := run(t, second.Mirror, "rev-list", "--count", "refs/heads/main"); got != "1" {
		t.Errorf("mirror depth = %s commits, want 1", got)
	}

	if err := done1(); err != nil {
		t.Fatal(err)
	}
	if err := done2(); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{first.Work, second.Work} {
		if _, err := os.Stat(w); !os.IsNotExist(err) {
			t.Errorf("work tree %s left behind: %v", w, err)
		}
	}
}

func TestCheckoutMissingBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	c := &Cache{Root: filepath.Join(t.TempDir(), "git")}
	_, _, err := c.Checkout(context.Background(), "run1", origin(t, "v1"), "example.com", "o/n", "nope", "", "")
	if err == nil || !strings.Contains(err.Error(), "branch not found on origin") {
		t.Fatalf("err = %v, want ErrNoBranch", err)
	}
}

func TestGCRemovesIdleMirrorsOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	c := &Cache{Root: filepath.Join(t.TempDir(), "git")}
	kept, done, err := c.Checkout(ctx, "run1", origin(t, "v1"), "example.com", "o/live", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	idle, doneIdle, err := c.Checkout(ctx, "run2", origin(t, "v1"), "example.com", "o/idle", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := doneIdle(); err != nil {
		t.Fatal(err)
	}

	c.MaxBytes = 1 // everything is over budget
	removed, freed, err := c.GC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || freed <= 0 {
		t.Errorf("GC removed %d mirrors, freed %d bytes; want the one idle mirror", removed, freed)
	}
	if _, err := os.Stat(idle.Mirror); !os.IsNotExist(err) {
		t.Errorf("idle mirror survived GC: %v", err)
	}
	if _, err := os.Stat(kept.Mirror); err != nil {
		t.Errorf("mirror with a live work tree was removed: %v", err)
	}
	if err := done(); err != nil {
		t.Fatal(err)
	}
}

func TestPruneWorkRemovesLeftovers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	c := &Cache{Root: filepath.Join(t.TempDir(), "git")}
	src := origin(t, "v1")
	r, _, err := c.Checkout(ctx, "crashed", src, "example.com", "o/n", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PruneWork(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Work); !os.IsNotExist(err) {
		t.Errorf("leftover work tree survived: %v", err)
	}
	if out := run(t, r.Mirror, "worktree", "list", "--porcelain"); strings.Contains(out, "crashed") {
		t.Errorf("mirror still registers the work tree:\n%s", out)
	}
	// The mirror is still usable afterwards.
	if _, _, err := c.Checkout(ctx, "next", src, "example.com", "o/n", "main", "", ""); err != nil {
		t.Fatalf("checkout after prune: %v", err)
	}
}
