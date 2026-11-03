package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// openPRNet serves pr as the only open PR and delegates everything else to next.
type openPRNet struct {
	next http.RoundTripper
	pr   map[string]any
}

func (o *openPRNet) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.Host != "git.test" || req.URL.Path != "/api/v1/repos/o/r/pulls" {
		return o.next.RoundTrip(req)
	}
	rec := httptest.NewRecorder()
	if req.URL.Query().Get("state") == "open" && req.URL.Query().Get("page") == "1" {
		_ = json.NewEncoder(rec).Encode([]any{o.pr})
	} else {
		_, _ = rec.WriteString("[]")
	}
	return rec.Result(), nil
}

// With rebaseWhen: behind-base-branch, an unchanged PR whose branch is not on top of the
// moved base is rebuilt on the new base, force-pushed and counted as updated.
func TestRunRebasesBehindBase(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	origin, work := gitRepo(t, map[string]string{".github/workflows/ci.yml": workflow})
	const branch = "deps/github-actions/actions-checkout-major"
	if _, stderr, code := execute(t, "run", "--dir", work, "--author-email", "bot@example.invalid"); code != 0 || len(f.created) != 1 {
		t.Fatalf("first run: exit %d, created %v\n%s", code, f.created, stderr)
	}
	first := git(t, origin, "rev-parse", branch)

	// Move base: commit the config to main.
	writeFiles(t, work, map[string]string{".deps.yaml": "rebaseWhen: behind-base-branch\n"})
	git(t, work, "add", "-A")
	git(t, work, "commit", "-qm", "config")
	git(t, work, "push", "-q", "origin", "HEAD:main")
	base := git(t, work, "rev-parse", "HEAD")

	newHTTPClient = func() *http.Client {
		return &http.Client{Transport: &openPRNet{next: f, pr: map[string]any{
			"number": 7, "title": f.created[0]["title"], "body": f.created[0]["body"], "state": "open",
			"mergeable": true, "html_url": "https://git.test/o/r/pulls/7", "head": map[string]string{"ref": branch},
		}}}
	}
	_, stderr, code := execute(t, "run", "--dir", work, "--author-email", "bot@example.invalid")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"pr      rebased  " + branch + "  ",
		"0 created, 1 updated, 0 closed, 0 up-to-date, 0 skipped, 0 automerge, 0 errors\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if tip := git(t, origin, "rev-parse", branch); tip == first {
		t.Error("branch was not pushed")
	}
	if parent := git(t, origin, "rev-parse", branch+"^"); parent != base {
		t.Errorf("rebased parent = %s, want base %s", parent, base)
	}
	if len(f.created) != 1 {
		t.Errorf("created PRs = %v", f.created)
	}

	// Already on top of base: up to date, no push.
	tip := git(t, origin, "rev-parse", branch)
	if _, stderr, code = execute(t, "run", "--dir", work, "--author-email", "bot@example.invalid"); code != 0 ||
		!strings.Contains(stderr, "0 created, 0 updated, 0 closed, 1 up-to-date, 0 skipped, 0 automerge, 0 errors\n") {
		t.Errorf("third run: exit %d\n%s", code, stderr)
	}
	if got := git(t, origin, "rev-parse", branch); got != tip {
		t.Errorf("up-to-date run moved the branch to %s", got)
	}
}

func TestCommitAuthorPrecedence(t *testing.T) {
	const env, cfg = "Env Bot <env@example.com>", "Cfg Bot <cfg@example.com>"
	for _, tc := range []struct {
		name, env, cfg    string
		flags             map[string]string
		wantName, wantEml string
	}{
		{name: "default", wantName: "deps[bot]", wantEml: "deps-bot@noreply.invalid"},
		{name: "config", cfg: cfg, wantName: "Cfg Bot", wantEml: "cfg@example.com"},
		{name: "env over config", env: env, cfg: cfg, wantName: "Env Bot", wantEml: "env@example.com"},
		{name: "flags over env and config", env: env, cfg: cfg, flags: map[string]string{"author-name": "Flag", "author-email": "flag@example.com"}, wantName: "Flag", wantEml: "flag@example.com"},
		{name: "flags field by field", cfg: cfg, flags: map[string]string{"author-email": "flag@example.com"}, wantName: "Cfg Bot", wantEml: "flag@example.com"},
		{name: "flag set to the default still wins", cfg: cfg, flags: map[string]string{"author-name": "deps[bot]"}, wantName: "deps[bot]", wantEml: "cfg@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newRun(&rootOpts{})
			for k, v := range tc.flags {
				if err := cmd.Flags().Set(k, v); err != nil {
					t.Fatal(err)
				}
			}
			name, email, err := commitAuthor(cmd, flagOpts(t, cmd), tc.env, tc.cfg)
			if err != nil || name != tc.wantName || email != tc.wantEml {
				t.Errorf("commitAuthor = %q, %q, %v; want %q, %q", name, email, err, tc.wantName, tc.wantEml)
			}
		})
	}
	if _, _, err := commitAuthor(newRun(&rootOpts{}), &runOpts{}, "no-angle@example.com", ""); err == nil || !strings.Contains(err.Error(), "DEPS_GIT_AUTHOR: must be 'Name <email>'") {
		t.Errorf("invalid env error = %v", err)
	}
}

// flagOpts reads the author flag values back from cmd.
func flagOpts(t *testing.T, cmd *cobra.Command) *runOpts {
	t.Helper()
	n, _ := cmd.Flags().GetString("author-name")
	e, _ := cmd.Flags().GetString("author-email")
	return &runOpts{authorName: n, authorEmail: e}
}

// gitAuthor in .deps.yaml sets the commit author and committer of pushed branches.
func TestRunGitAuthorFromConfig(t *testing.T) {
	cleanEnv(t)
	newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	origin, work := gitRepo(t, map[string]string{
		".github/workflows/ci.yml": workflow,
		".deps.yaml":               "gitAuthor: 'OCM Bot <ocm-bot@example.com>'\n",
	})
	if _, stderr, code := execute(t, "run", "--dir", work); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	const branch = "deps/github-actions/actions-checkout-major"
	if got := git(t, origin, "log", "-1", "--format=%an <%ae>|%cn <%ce>", branch); got != "OCM Bot <ocm-bot@example.com>|OCM Bot <ocm-bot@example.com>" {
		t.Errorf("author|committer = %q", got)
	}
}

// automerge and dependencyDashboard end to end on Gitea: the created PR is scheduled for
// auto-merge and the dashboard issue is created with resolved label ids; a dry run only reports.
func TestRunAutomergeAndDashboard(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	giteaEnv(t)
	t.Setenv("DEPS_TOKEN", "tok")
	origin, work := gitRepo(t, map[string]string{
		".github/workflows/ci.yml": workflow,
		".deps.yaml":               "automerge: true\nautomergeStrategy: squash\ndependencyDashboard: true\ndependencyDashboardLabels: [dependencies]\n",
	})
	const branch = "deps/github-actions/actions-checkout-major"

	_, stderr, code := execute(t, "run", "--dir", work, "--dry-run")
	if code != 0 || len(f.merges)+len(f.issues)+len(f.created) != 0 {
		t.Fatalf("dry run: exit %d merges %v issues %v\n%s", code, f.merges, f.issues, stderr)
	}
	for _, want := range []string{
		"pr      would-create, would-automerge  " + branch,
		"dashboard would-create  Dependency Dashboard  \n",
		"(dry run: 1 would change, 1 would automerge)\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("dry run stderr lacks %q:\n%s", want, stderr)
		}
	}

	_, stderr, code = execute(t, "run", "--dir", work, "--author-email", "bot@example.invalid")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"pr      created, automerge  " + branch + "  chore(deps): update actions/checkout to v7.0.1  https://git.test/o/r/pulls/7\n",
		"dashboard created  Dependency Dashboard  https://git.test/o/r/issues/3\n",
		"1 created, 0 updated, 0 closed, 0 up-to-date, 0 skipped, 1 automerge, 0 errors\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	tip := git(t, origin, "rev-parse", branch)
	if len(f.merges) != 1 || f.merges[0]["do"] != "squash" || f.merges[0]["merge_when_checks_succeed"] != true ||
		f.merges[0]["delete_branch_after_merge"] != true || f.merges[0]["head_commit_id"] != tip {
		t.Errorf("merge requests = %v (tip %s)", f.merges, tip)
	}
	if len(f.issues) != 1 || f.issues[0]["title"] != "Dependency Dashboard" || fmt.Sprint(f.issues[0]["labels"]) != "[11]" {
		t.Fatalf("issues = %v", f.issues)
	}
	body, _ := f.issues[0]["body"].(string)
	for _, want := range []string{
		"- [ ] <!-- deps:rebase=" + branch + " --> chore(deps): update actions/checkout to v7.0.1 ([#7](https://git.test/o/r/pulls/7))\n",
		"<details><summary>.github/workflows/ci.yml</summary>",
		"- `actions/checkout v6.0.0`\n",
		"<!-- deps:dashboard -->",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body lacks %q:\n%s", want, body)
		}
	}
}

func TestDetectTrigger(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	tests := []struct {
		name, flag string
		env        map[string]string
		want       schedule.Trigger
		wantErr    string
	}{
		{name: "flag", flag: "0 0 * * 0", env: map[string]string{"DEPS_TRIGGER_SCHEDULE": "0 5 * * *", "GITHUB_EVENT_NAME": "push"}, want: schedule.Trigger{Event: "schedule", Cron: "0 0 * * 0"}},
		{name: "env", env: map[string]string{"DEPS_TRIGGER_SCHEDULE": "0  5 * * *", "GITHUB_EVENT_NAME": "push"}, want: schedule.Trigger{Event: "schedule", Cron: "0 5 * * *"}},
		{name: "ci event", env: map[string]string{"GITHUB_EVENT_NAME": "workflow_dispatch"}, want: schedule.Trigger{Event: "workflow_dispatch"}},
		{name: "unreadable payload warns", env: map[string]string{"GITHUB_EVENT_NAME": "schedule", "GITHUB_EVENT_PATH": "/nonexistent"}, want: schedule.Trigger{Event: "schedule"}},
		{name: "invalid flag", flag: "0 5 * *", wantErr: `--trigger-schedule: "0 5 * *": want 5 fields, got 4`},
		{name: "invalid env", env: map[string]string{"DEPS_TRIGGER_SCHEDULE": "x"}, wantErr: `DEPS_TRIGGER_SCHEDULE: "x": want 5 fields, got 1`},
	}
	for _, tt := range tests {
		got, err := detectTrigger(t.Context(), tt.flag, env(tt.env))
		var ue *usageError
		if tt.wantErr != "" {
			if !errors.As(err, &ue) || err.Error() != tt.wantErr {
				t.Errorf("%s: err = %v, want usage error %q", tt.name, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%s: got %+v, %v; want %+v", tt.name, got, err, tt.want)
		}
	}
}
