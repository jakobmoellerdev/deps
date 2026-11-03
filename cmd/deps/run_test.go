package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		"0 created, 1 updated, 0 closed, 0 up-to-date, 0 skipped, 0 errors\n",
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
		!strings.Contains(stderr, "0 created, 0 updated, 0 closed, 1 up-to-date, 0 skipped, 0 errors\n") {
		t.Errorf("third run: exit %d\n%s", code, stderr)
	}
	if got := git(t, origin, "rev-parse", branch); got != tip {
		t.Errorf("up-to-date run moved the branch to %s", got)
	}
}
