package crate

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// gitLog puts a `git` in front of PATH that appends its arguments to a file and runs the real
// git, and returns the file's lines. Nothing of the test's own setup must run git afterwards.
func gitLog(t *testing.T) func() []string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not in PATH")
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "git.log")
	script := "#!/bin/sh\necho \"$@\" >> \"$DEPS_TEST_GIT_LOG\"\nexec " + gitBin + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEPS_TEST_GIT_LOG", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
}

// Every crate of a git index used to fork its own `git cat-file`; the reads now share the
// `git cat-file --batch` process of the clone, so the index costs one clone and one git
// process however many crates are looked up.
func TestGitIndexReadsShareProcess(t *testing.T) {
	index := gitIndex(t)
	// The fixture lines are serde's; what the lookups need is one index path per crate.
	dir := strings.TrimPrefix(index, "file://")
	serde, err := os.ReadFile(filepath.Join(dir, "se", "rd", "serde"))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{"an/yh/anyhow": string(serde), "re/ge/regex": string(serde)})
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "more"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	lines := gitLog(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{".cargo/config.toml": "[registries.corp]\nindex = \"" + index + "\"\n"})
	s := New(http.DefaultClient, nil, nil, envOf(map[string]string{"CARGO_HOME": t.TempDir()}))
	t.Cleanup(func() { _ = s.Close() })
	for _, name := range []string{"serde", "anyhow", "regex", "missing"} {
		d := dep.Dep{Name: name, Source: "corp", File: "Cargo.toml"}
		rels, err := s.Releases(t.Context(), d, root)
		if (err != nil) != (name == "missing") {
			t.Fatalf("%s: %d releases, %v", name, len(rels), err)
		}
	}
	// config.json is read as well: the registry's API base comes from it.
	if _, err := s.apiOf(t.Context(), s.locate(dep.Dep{Name: "serde", Source: "corp", File: "Cargo.toml"})); err != nil {
		t.Fatal(err)
	}
	var clones, cats int
	for _, l := range lines() {
		switch {
		case strings.HasPrefix(l, "clone "):
			clones++
		case strings.HasPrefix(l, "cat-file "):
			cats++
		default:
			t.Errorf("unexpected git %s", l)
		}
	}
	if clones != 1 || cats != 1 {
		t.Errorf("%d clones, %d cat-file processes for 5 reads, want 1 and 1", clones, cats)
	}
}

// A read of a git index fetches the blob from the index host (the clone is blob-less), so it
// holds a request slot as a sparse index's GET does: with every slot taken, the lookup waits
// instead of running git, and it finishes once the slot is free.
func TestGitIndexReadTakesSlot(t *testing.T) {
	index := gitIndex(t)
	lines := gitLog(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{".cargo/config.toml": "[registries.corp]\nindex = \"" + index + "\"\n"})
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // the only slot, held by something else
	s := New(http.DefaultClient, sem, nil, envOf(map[string]string{"CARGO_HOME": t.TempDir()}))
	t.Cleanup(func() { _ = s.Close() })
	type result struct {
		rels []datasource.Release
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rels, err := s.Releases(t.Context(), dep.Dep{Name: "serde", Source: "corp", File: "Cargo.toml"}, root)
		done <- result{rels, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("lookup ran without a slot: %d releases, %v", len(r.rels), r.err)
	case <-time.After(300 * time.Millisecond):
	}
	if got := lines(); len(got) != 0 {
		t.Errorf("git ran without a slot: %v", got)
	}
	<-sem // the slot is free again
	select {
	case r := <-done:
		if r.err != nil || len(r.rels) == 0 {
			t.Fatalf("after the slot was free: %d releases, %v", len(r.rels), r.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("lookup did not finish after the slot was free")
	}
}
