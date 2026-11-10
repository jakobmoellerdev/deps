package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseConfigRepo is a repository whose main config (mainConfig) runs on main and
// release/1.x; release/1.x has releaseConfig as its .deps.yaml ("" = none) and a marker
// comment in the workflow. The checkout stays on main.
func baseConfigRepo(t *testing.T, mainConfig, releaseConfig string) (origin, work string) {
	t.Helper()
	origin, work = gitRepo(t, map[string]string{".github/workflows/ci.yml": workflow, ".deps.yaml": mainConfig})
	git(t, work, "checkout", "-qb", "release/1.x")
	writeFiles(t, work, map[string]string{".github/workflows/ci.yml": "# release\n" + workflow})
	if releaseConfig == "" {
		git(t, work, "rm", "-q", ".deps.yaml")
	} else {
		writeFiles(t, work, map[string]string{".deps.yaml": releaseConfig})
	}
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "release")
	git(t, work, "push", "-q", "origin", "HEAD:release/1.x")
	git(t, work, "checkout", "-q", "main")
	return origin, work
}

// releaseGroupConfig groups the checkout updates on the base branch it is read from; its
// branchPrefix spans the run, so the checkout's applies.
const releaseGroupConfig = "branchPrefix: other/\nrules:\n  - names: [actions/checkout]\n    group: release checkout\n"

func TestRunUseBaseBranchConfig(t *testing.T) {
	tests := []struct {
		name, mode, release string
		code                int
		want, unwanted      []string
	}{
		{
			name: "none", mode: "none", release: releaseGroupConfig,
			want:     []string{"base    release/1.x\n", "pr      would-create  deps/release/1.x/actions-checkout-major"},
			unwanted: []string{"config .deps.yaml", "group/release-checkout"},
		},
		{
			name: "merge", mode: "merge", release: releaseGroupConfig,
			want: []string{
				"base    main\n",
				"base    release/1.x  config .deps.yaml + release/1.x:.deps.yaml\n",
				"pr      would-create  deps/main/actions-checkout-major",
				"pr      would-create  deps/release/1.x/group/release-checkout-major",
				"the base branch's config sets a key that spans the run", "key=branchPrefix",
			},
			unwanted: []string{"other/", "deps/release/1.x/actions-checkout"},
		},
		{
			name: "merge, same run-wide value", mode: "merge", release: "baseBranches: [$default, release/1.x]\nuseBaseBranchConfig: merge\n",
			want:     []string{"base    release/1.x  config .deps.yaml + release/1.x:.deps.yaml\n"},
			unwanted: []string{"spans the run"},
		},
		{
			name: "merge without the file", mode: "merge", code: 2,
			want: []string{"base branch release/1.x: useBaseBranchConfig: merge: .deps.yaml not found on the base branch"},
		},
		{
			name: "merge-if-present without the file", mode: "merge-if-present",
			want:     []string{"base    release/1.x\n", "pr      would-create  deps/release/1.x/actions-checkout-major"},
			unwanted: []string{"config .deps.yaml"},
		},
		{
			name: "invalid merged config", mode: "merge", release: "rangeStrategy: sideways\n", code: 2,
			want: []string{"release/1.x", "rangeStrategy"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanEnv(t)
			newFakeNet(t)
			giteaEnv(t)
			_, work := baseConfigRepo(t, "baseBranches: [$default, release/1.x]\nuseBaseBranchConfig: "+tt.mode+"\n", tt.release)

			_, stderr, code := execute(t, "run", "--dir", work, "--dry-run")
			if code != tt.code {
				t.Fatalf("exit %d, want %d\n%s", code, tt.code, stderr)
			}
			for _, w := range tt.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr lacks %q:\n%s", w, stderr)
				}
			}
			for _, u := range tt.unwanted {
				if strings.Contains(stderr, u) {
					t.Errorf("stderr has %q:\n%s", u, stderr)
				}
			}
			if trees := git(t, work, "worktree", "list", "--porcelain"); strings.Count(trees, "worktree ") != 1 {
				t.Errorf("linked work trees left behind:\n%s", trees)
			}
		})
	}
}

// A custom manager of the base branch's config edits that base's files: bot.Base.Managers.
func TestRunUseBaseBranchConfigCustomManager(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	release := `custom:
  - files: [versions.txt]
    match: 'checkout=(?P<currentValue>\S+)'
    name: https://github.com/actions/checkout
    datasource: git-tags
`
	origin, work := baseConfigRepo(t, "baseBranches: [$default, release/1.x]\nuseBaseBranchConfig: merge\n", release)
	git(t, work, "checkout", "-q", "release/1.x")
	writeFiles(t, work, map[string]string{"versions.txt": "checkout=v6.0.0\n"})
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "versions")
	git(t, work, "push", "-q", "origin", "HEAD:release/1.x")
	git(t, work, "checkout", "-q", "main")

	_, stderr, code := execute(t, "run", "--dir", work, "--author-email", "bot@example.invalid")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	const branch = "deps/release/1.x/https-github.com-actions-checkout-major"
	if !strings.Contains(stderr, "pr      created  "+branch+"  ") {
		t.Fatalf("stderr lacks the custom manager's PR:\n%s", stderr)
	}
	if content := git(t, origin, "show", branch+":versions.txt"); content != "checkout=v7.0.1" {
		t.Errorf("versions.txt = %q", content)
	}
	if len(f.created) != 3 {
		t.Errorf("created PRs = %v", f.created)
	}
}

func TestExplainBase(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	_, work := baseConfigRepo(t, "baseBranches: [$default, release/1.x]\nuseBaseBranchConfig: merge\n", releaseGroupConfig)

	stdout, stderr, code := execute(t, "explain", "--dir", work, "--base", "release/1.x", "actions/checkout")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "base    release/1.x  config .deps.yaml + release/1.x:.deps.yaml\n") {
		t.Errorf("stderr:\n%s", stderr)
	}
	// The release work tree's workflow (line 7 after the marker) and the rule of its config.
	if !strings.Contains(stdout, ".github/workflows/ci.yml:7") || !strings.Contains(stdout, "from release/1.x:.deps.yaml:3") || !strings.Contains(stdout, "group=release checkout") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if trees := git(t, work, "worktree", "list", "--porcelain"); strings.Count(trees, "worktree ") != 1 {
		t.Errorf("linked work trees left behind:\n%s", trees)
	}

	// The checkout's own base: its config as is, no work tree.
	stdout, stderr, code = execute(t, "explain", "--dir", work, "--base", "main", "actions/checkout")
	if code != 0 || !strings.Contains(stderr, "base    main\n") || strings.Contains(stdout, "release checkout") {
		t.Errorf("exit %d\n%s\n%s", code, stdout, stderr)
	}

	_, stderr, code = execute(t, "explain", "--dir", work, "--base", "gone", "actions/checkout")
	if code != 2 || !strings.Contains(stderr, "base branch gone not found on origin") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
}

// preview of a pull request into release/1.x: with useBaseBranchConfig on the default branch,
// both checkouts' configs are merged over main's, whose rule groups the checkout updates.
func TestPreviewUseBaseBranchConfig(t *testing.T) {
	const mainConfig = "baseBranches: [$default, release/1.x]\nuseBaseBranchConfig: merge\nrules:\n  - names: [actions/checkout]\n    group: main checkout\n"
	for _, tt := range []struct {
		name, mainConfig string
		want             []string
	}{
		{"merge", mainConfig, []string{
			"base    release/1.x  config main:.deps.yaml + release/1.x:.deps.yaml\n",
			"removed deps/group/main-checkout-major",
		}},
		{"none", strings.Replace(mainConfig, "merge", "none", 1), []string{
			"base    release/1.x\n",
			"removed deps/actions-checkout-major",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cleanEnv(t)
			newFakeNet(t)
			origin, base := baseConfigRepo(t, tt.mainConfig, "builtins: {monorepos: true}\n")
			git(t, base, "checkout", "-q", "release/1.x")
			pr := t.TempDir()
			git(t, pr, "clone", "-q", "-b", "release/1.x", origin, ".")
			writeFiles(t, pr, map[string]string{".deps.yaml": "rules:\n  - names: [actions/checkout]\n    ignore: true\n"})
			summary := filepath.Join(t.TempDir(), "summary.md")
			t.Setenv("GITHUB_STEP_SUMMARY", summary)
			t.Setenv("GITHUB_BASE_REF", "release/1.x")

			_, stderr, code := execute(t, "preview", "--dir", pr, "--base-dir", base)
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			for _, w := range tt.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr lacks %q:\n%s", w, stderr)
				}
			}
			md, _ := os.ReadFile(summary)
			if !strings.Contains(string(md), "Base branch `release/1.x`, config `") {
				t.Errorf("step summary:\n%s", md)
			}
		})
	}
}

func TestPullRequestBase(t *testing.T) {
	for _, tt := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"GITHUB_BASE_REF": "release/1.x", "CI_MERGE_REQUEST_TARGET_BRANCH_NAME": "x"}, "release/1.x"},
		{map[string]string{"CI_MERGE_REQUEST_TARGET_BRANCH_NAME": "main"}, "main"},
		{nil, ""},
	} {
		if got := pullRequestBase(func(k string) string { return tt.env[k] }); got != tt.want {
			t.Errorf("pullRequestBase(%v) = %q, want %q", tt.env, got, tt.want)
		}
	}
}

// With --renovate, each base branch's Renovate config is translated and merged.
func TestRunUseBaseBranchConfigRenovate(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	_, work := gitRepo(t, map[string]string{
		".github/workflows/ci.yml": workflow,
		"renovate.json":            `{"baseBranchPatterns": ["$default", "release/1.x"], "useBaseBranchConfig": "merge"}`,
	})
	git(t, work, "checkout", "-qb", "release/1.x")
	writeFiles(t, work, map[string]string{"renovate.json": `{"packageRules": [{"matchPackageNames": ["actions/checkout"], "groupName": "release checkout"}]}`})
	git(t, work, "commit", "-qam", "release")
	git(t, work, "push", "-q", "origin", "HEAD:release/1.x")
	git(t, work, "checkout", "-q", "main")

	_, stderr, code := execute(t, "run", "--dir", work, "--dry-run", "--renovate")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, w := range []string{
		" + release/1.x:renovate.json\n",
		"pr      would-create  renovate/main/actions-checkout-major",
		"pr      would-create  renovate/release/1.x/group/release-checkout-major",
	} {
		if !strings.Contains(stderr, w) {
			t.Errorf("stderr lacks %q:\n%s", w, stderr)
		}
	}
	if strings.Contains(stderr, "spans the run") {
		t.Errorf("run-wide warning for an unchanged value:\n%s", stderr)
	}
}

// Without a git origin the default branch is unknown: both checkouts keep their config and
// are resolved for the base given; the default branch itself merges nothing.
func TestPreviewBaseWithoutMerge(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	work := t.TempDir()
	base, pr := filepath.Join(work, "base"), filepath.Join(work, "pr")
	cfg := "rules:\n  - baseBranches: [main]\n    names: [actions/checkout]\n    group: on main\n"
	writeFiles(t, base, map[string]string{".github/workflows/ci.yml": workflow, ".deps.yaml": cfg})
	writeFiles(t, pr, map[string]string{".github/workflows/ci.yml": workflow, ".deps.yaml": cfg + "  - names: [actions/checkout]\n    ignore: true\n"})
	_, stderr, code := execute(t, "preview", "--dir", pr, "--base-dir", base, "--base", "main")
	if code != 0 || !strings.Contains(stderr, "default branch unknown, the checkouts keep their config") ||
		!strings.Contains(stderr, "base    main\n") || !strings.Contains(stderr, "removed deps/group/on-main-major") {
		t.Errorf("exit %d\n%s", code, stderr)
	}

	_, work2 := baseConfigRepo(t, "baseBranches: [$default, release/1.x]\nuseBaseBranchConfig: merge\n", releaseGroupConfig)
	_, stderr, code = execute(t, "preview", "--dir", work2, "--base-dir", work2, "--base", "main")
	if code != 0 || !strings.Contains(stderr, "base    main\n") || strings.Contains(stderr, "default branch") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
}
