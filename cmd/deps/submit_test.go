package main

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSubmit(t *testing.T) {
	cleanEnv(t)
	f := newFakeNet(t)
	var got map[string]any
	f.mux.HandleFunc("POST api.github.com/repos/o/r/dependency-graph/snapshots", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghs_x" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		if err := json.UnmarshalRead(r.Body, &got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":1,"result":"SUCCESS","message":"ok"}`)
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"go.mod": gomodSrc, "Dockerfile": "FROM alpine:3.20\nFROM $X\n"})
	for k, v := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_API_URL": "https://api.github.com", "GITHUB_REPOSITORY": "o/r",
		"GITHUB_TOKEN": "ghs_x", "GITHUB_SHA": "abc", "GITHUB_REF": "refs/heads/main", "GITHUB_WORKFLOW": "deps", "GITHUB_JOB": "submit", "GITHUB_RUN_ID": "42",
	} {
		t.Setenv(k, v)
	}
	_, stderr, code := execute(t, "submit", "--dir", dir)
	if code != 0 || !strings.Contains(stderr, "manifest  Dockerfile  1\n") || !strings.Contains(stderr, "submitted SUCCESS\n") || !strings.Contains(stderr, "1 dependencies in 1 manifests, 0 errors\n") {
		t.Fatalf("exit %d:\n%s", code, stderr)
	}
	job, _ := got["job"].(map[string]any)
	manifests, _ := got["manifests"].(map[string]any)
	df, _ := manifests["Dockerfile"].(map[string]any)
	resolved, _ := df["resolved"].(map[string]any)
	if got["sha"] != "abc" || got["ref"] != "refs/heads/main" || job["correlator"] != "deps submit deps" || job["id"] != "42" ||
		len(manifests) != 1 || resolved["pkg:docker/library/alpine@3.20"] == nil {
		t.Errorf("snapshot = %v", got)
	}
}

func TestSubmitOutsideGitHub(t *testing.T) {
	cleanEnv(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"Dockerfile": "FROM alpine:3.20\n"})
	if _, stderr, code := execute(t, "submit", "--dir", dir); code != 2 || !strings.Contains(stderr, "use --dry-run elsewhere") {
		t.Errorf("exit %d:\n%s", code, stderr)
	}
	stdout, _, code := execute(t, "submit", "--dir", dir, "--dry-run")
	if code != 0 || !strings.Contains(stdout, `"pkg:docker/library/alpine@3.20"`) || !strings.Contains(stdout, `"correlator":"deps"`) {
		t.Errorf("exit %d, stdout %s", code, stdout)
	}
}
