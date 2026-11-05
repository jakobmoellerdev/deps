package main

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPreview(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	var mu sync.Mutex
	var checkRuns []map[string]any
	f.mux.HandleFunc("POST api.github.com/repos/o/r/check-runs", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if err := json.UnmarshalRead(r.Body, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		checkRuns = append(checkRuns, in)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"html_url":"https://github.com/o/r/runs/1"}`)
	})
	work := t.TempDir()
	base, pr := filepath.Join(work, "base"), filepath.Join(work, "pr")
	writeFiles(t, base, map[string]string{".github/workflows/ci.yml": workflow, "go.mod": gomodSrc})
	// The PR ignores gomod and groups the checkout updates under a new name.
	writeFiles(t, pr, map[string]string{
		".github/workflows/ci.yml": workflow, "go.mod": gomodSrc,
		".deps.yaml": "rules:\n  - manager: gomod\n    ignore: true\n  - names: [actions/checkout]\n    group: checkout\n",
	})
	event := filepath.Join(work, "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"head":{"sha":"abc123"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	summary := filepath.Join(work, "summary.md")
	for k, v := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_API_URL": "https://api.github.com", "GITHUB_REPOSITORY": "o/r",
		"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": event, "GITHUB_TOKEN": "ghs_x", "GITHUB_STEP_SUMMARY": summary,
	} {
		t.Setenv(k, v)
	}

	_, stderr, code := execute(t, "preview", "--dir", pr, "--base-dir", base)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	for _, want := range []string{
		"new     deps/group/checkout-major  chore(deps): update actions/checkout to v7.0.1\n",
		"removed deps/github-actions/actions-checkout-major  chore(deps): update actions/checkout to v7.0.1\n",
		"removed deps/gomod/example.com-a  fix(deps): update example.com/a to v1.1.0\n",
		"check   https://github.com/o/r/runs/1\n",
		"1 new, 0 changed, 2 no longer opened, 0 the same (",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if len(checkRuns) != 1 {
		t.Fatalf("check runs = %v", checkRuns)
	}
	cr := checkRuns[0]
	out, _ := cr["output"].(map[string]any)
	if cr["head_sha"] != "abc123" || cr["conclusion"] != "success" || cr["name"] != "deps preview" || out["title"] != "1 new, 0 changed, 2 no longer opened, 0 the same" ||
		!strings.Contains(out["summary"].(string), "### No longer opened") {
		t.Errorf("check run = %v", cr)
	}
	md, _ := os.ReadFile(summary)
	if !strings.Contains(string(md), "#### chore(deps): update actions/checkout to v7.0.1\n\n`deps/group/checkout-major`\n\n| Dependency |") {
		t.Errorf("step summary:\n%s", md)
	}
}

func TestPreviewWithoutCheckRunPermission(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	f.mux.HandleFunc("POST api.github.com/repos/o/r/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
	})
	work := t.TempDir()
	writeFiles(t, work, map[string]string{"go.mod": gomodSrc})
	event := filepath.Join(work, "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"head":{"sha":"abc123"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_API_URL": "https://api.github.com", "GITHUB_REPOSITORY": "o/r",
		"GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": event,
	} {
		t.Setenv(k, v)
	}
	_, stderr, code := execute(t, "preview", "--dir", work, "--base-dir", work)
	if code != 0 || !strings.Contains(stderr, "note    no check run") || !strings.Contains(stderr, "No change to the update PRs (1 the same).") {
		t.Errorf("exit %d:\n%s", code, stderr)
	}
}
