package main

import (
	"strings"
	"testing"
)

// baseBranchesConfig runs on main and every release/ branch; the rule groups the updates of
// release branches.
const baseBranchesConfig = `baseBranches: [$default, '/^release\//', gone]
rules:
  - baseBranches: ['/^release\//']
    group: release actions
`

// baseBranchesRepo is a repository with the workflow on main and, with a marker comment, on
// release/1.x, both pushed to origin; the checkout stays on main.
func baseBranchesRepo(t *testing.T) (origin, work string) {
	t.Helper()
	origin, work = gitRepo(t, map[string]string{".github/workflows/ci.yml": workflow, ".deps.yaml": baseBranchesConfig})
	git(t, work, "checkout", "-qb", "release/1.x")
	writeFiles(t, work, map[string]string{".github/workflows/ci.yml": "# release\n" + workflow})
	git(t, work, "commit", "-qam", "release")
	git(t, work, "push", "-q", "origin", "HEAD:release/1.x")
	git(t, work, "checkout", "-q", "main")
	return origin, work
}

func TestRunBaseBranches(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	origin, work := baseBranchesRepo(t)

	_, stderr, code := execute(t, "run", "--dir", work, "--author-email", "bot@example.invalid")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	const (
		mainBranch    = "deps/main/github-actions/actions-checkout-major"
		releaseBranch = "deps/release/1.x/group/release-actions-major"
	)
	for _, want := range []string{
		"base branch not found on origin, skipping it", "branch=gone",
		"base    main\n", "base    release/1.x\n",
		"pr      created  " + mainBranch + "  ",
		"pr      created  " + releaseBranch + "  ",
		"2 created, 0 updated, 0 closed, 0 up-to-date, 0 skipped, 0 automerge, 0 errors\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if len(f.created) != 2 || f.created[0]["head"] != mainBranch || f.created[0]["base"] != "main" ||
		f.created[1]["head"] != releaseBranch || f.created[1]["base"] != "release/1.x" {
		t.Errorf("created PRs = %v", f.created)
	}
	if content := git(t, origin, "show", releaseBranch+":.github/workflows/ci.yml"); !strings.HasPrefix(content, "# release\n") || !strings.Contains(content, "actions/checkout@v7.0.1") {
		t.Errorf("release branch file:\n%s", content)
	}
	if parent, tip := git(t, origin, "rev-parse", releaseBranch+"^"), git(t, origin, "rev-parse", "release/1.x"); parent != tip {
		t.Errorf("release branch parent = %s, want the tip of release/1.x %s", parent, tip)
	}
	if content := git(t, origin, "show", mainBranch+":.github/workflows/ci.yml"); strings.HasPrefix(content, "# release") {
		t.Errorf("main branch built from release:\n%s", content)
	}
	if trees := git(t, work, "worktree", "list", "--porcelain"); strings.Count(trees, "worktree ") != 1 {
		t.Errorf("linked work trees left behind:\n%s", trees)
	}
	if git(t, work, "status", "--porcelain") != "" {
		t.Error("run modified the working tree")
	}
}

func TestRunBaseBranchesDryRun(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	origin, work := baseBranchesRepo(t)
	// The checkout is release/1.x: main gets the linked work tree.
	git(t, work, "checkout", "-q", "release/1.x")

	_, stderr, code := execute(t, "run", "--dir", work, "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"pr      would-create  deps/main/github-actions/actions-checkout-major",
		"pr      would-create  deps/release/1.x/group/release-actions-major",
		"(dry run: 2 would change, 0 would automerge)",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if len(f.created) != 0 || git(t, origin, "branch", "--list", "deps/*") != "" {
		t.Error("dry run wrote to the platform or origin")
	}
	if trees := git(t, work, "worktree", "list", "--porcelain"); strings.Count(trees, "worktree ") != 1 {
		t.Errorf("linked work trees left behind:\n%s", trees)
	}
}

// --base keeps a single base with plain branch names; rules with baseBranches match it.
func TestRunBaseFlagOverridesBaseBranches(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	_, work := baseBranchesRepo(t)
	git(t, work, "checkout", "-q", "release/1.x")

	_, stderr, code := execute(t, "run", "--dir", work, "--dry-run", "--base", "release/1.x")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "pr      would-create  deps/group/release-actions-major") || strings.Contains(stderr, "base    ") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestRunBaseBranchesNoneOnOrigin(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	_, work := gitRepo(t, map[string]string{".github/workflows/ci.yml": workflow, ".deps.yaml": "baseBranches: [gone, '/^release/']\n"})

	_, stderr, code := execute(t, "run", "--dir", work, "--dry-run")
	if code == 0 || !strings.Contains(stderr, `baseBranches: no branch on origin matches ["gone" "/^release/"]`) {
		t.Errorf("exit %d\n%s", code, stderr)
	}
}
