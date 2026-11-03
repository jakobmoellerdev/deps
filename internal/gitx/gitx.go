// Package gitx builds commits with git plumbing and pushes them, without touching the working tree.
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

// Repo is a git repository; Dir may be any directory inside the work tree.
type Repo struct{ Dir string }

func (r Repo) run(ctx context.Context, stdin []byte, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "git command", "args", args, "duration", time.Since(start), "err", err)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// RevParse resolves rev to an object id.
func (r Repo) RevParse(ctx context.Context, rev string) (string, error) {
	return r.run(ctx, nil, nil, "rev-parse", "--verify", rev+"^{commit}")
}

// RemoteURL returns the URL of origin.
func (r Repo) RemoteURL(ctx context.Context) (string, error) {
	return r.run(ctx, nil, nil, "remote", "get-url", "origin")
}

// LsRemote lists refs on origin matching pattern as refname -> object id.
func (r Repo) LsRemote(ctx context.Context, pattern string) (map[string]string, error) {
	out, err := r.run(ctx, nil, nil, "ls-remote", "origin", pattern)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if oid, ref, ok := strings.Cut(sc.Text(), "\t"); ok {
			refs[ref] = oid
		}
	}
	return refs, nil
}

// Commit creates a commit on top of parent with changes applied, using a temporary index.
// Change paths are relative to Dir. The working tree and the real index are not touched.
func (r Repo) Commit(ctx context.Context, parent string, changes []engine.FileChange, msg, name, email string) (string, error) {
	prefix, err := r.run(ctx, nil, nil, "rev-parse", "--show-prefix")
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "deps-index-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	idx := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}

	if _, err := r.run(ctx, nil, idx, "read-tree", parent); err != nil {
		return "", err
	}
	var info strings.Builder
	for _, c := range changes {
		oid, err := r.run(ctx, c.New, nil, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		mode := "100644"
		if c.Mode&0o111 != 0 {
			mode = "100755"
		}
		fmt.Fprintf(&info, "%s %s\t%s\n", mode, oid, path.Join(prefix, c.Path))
	}
	if _, err := r.run(ctx, []byte(info.String()), idx, "update-index", "--index-info"); err != nil {
		return "", err
	}
	tree, err := r.run(ctx, nil, idx, "write-tree")
	if err != nil {
		return "", err
	}
	ident := []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
	return r.run(ctx, []byte(msg), ident, "commit-tree", tree, "-p", parent, "-F", "-")
}

// Push sets refs/heads/<branch> on origin to commit if it currently points at expected
// ("" = must not exist).
func (r Repo) Push(ctx context.Context, commit, branch, expected string) error {
	ref := "refs/heads/" + branch
	_, err := r.run(ctx, nil, nil, "push", "--porcelain",
		"--force-with-lease="+ref+":"+expected, "origin", commit+":"+ref)
	return err
}

// DeleteBranch deletes refs/heads/<branch> on origin.
func (r Repo) DeleteBranch(ctx context.Context, branch string) error {
	_, err := r.run(ctx, nil, nil, "push", "origin", ":refs/heads/"+branch)
	return err
}

// BranchAuthorEmail returns the author email of the tip of branch on origin. Shallow clones
// (CI) fetch with --depth=1; full clones fetch normally so they are not turned shallow.
func (r Repo) BranchAuthorEmail(ctx context.Context, branch string) (string, error) {
	shallow, err := r.run(ctx, nil, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return "", err
	}
	args := []string{"fetch", "--no-tags", "origin", "refs/heads/" + branch}
	if shallow == "true" {
		args = []string{"fetch", "--no-tags", "--depth=1", "origin", "refs/heads/" + branch}
	}
	if _, err := r.run(ctx, nil, nil, args...); err != nil {
		return "", err
	}
	return r.run(ctx, nil, nil, "log", "-1", "--format=%ae", "FETCH_HEAD")
}
