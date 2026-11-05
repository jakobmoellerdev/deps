// Package gitx builds commits with git plumbing and pushes them, without touching the working tree.
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// Push errors, wrapped in the git error.
var (
	// ErrStale means origin refused the push because the ref no longer points at the
	// expected commit (it moved since it was read) or the push is not a fast-forward.
	ErrStale = errors.New("ref moved on origin")
	// ErrRejected means origin itself declined the update, e.g. branch protection or a
	// pre-receive hook.
	ErrRejected = errors.New("push rejected by origin")
)

// Repo is a git repository; Dir may be any directory inside the work tree. A non-nil Signer
// signs the commits Commit creates; without one, git's own config (commit.gpgSign) applies.
type Repo struct {
	Dir    string
	Signer *Signer
}

// run runs git and returns its trimmed stdout, also when it fails.
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
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", subcommand(args), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// subcommand returns the git subcommand of args, skipping leading -c key=value options.
func subcommand(args []string) string {
	i := 0
	for i+2 < len(args) && args[i] == "-c" {
		i += 2
	}
	return args[i]
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

// Prefix returns the path of Dir relative to the top of the work tree, with a trailing '/'
// ("" at the top).
func (r Repo) Prefix(ctx context.Context) (string, error) {
	return r.run(ctx, nil, nil, "rev-parse", "--show-prefix")
}

// ObjectIDs returns the ids of the objects at paths in commit rev, in the order of paths: the
// blob of a file, the tree of a directory, "" when rev has nothing there. Paths are slash
// paths relative to Dir ("." is Dir itself) inside the work tree. One `git cat-file
// --batch-check` resolves all of them.
func (r Repo) ObjectIDs(ctx context.Context, rev string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	for _, p := range paths {
		if strings.ContainsAny(p, "\n\r") {
			return nil, fmt.Errorf("path %q contains a line break", p)
		}
		in.WriteString(rev)
		in.WriteString(":./") // relative to Dir, not the top of the work tree
		if p != "." {
			in.WriteString(p)
		}
		in.WriteByte('\n')
	}
	out, err := r.run(ctx, in.Bytes(), nil, "cat-file", "--batch-check=%(objectname)")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(out, "\n")
	if len(lines) != len(paths) {
		return nil, fmt.Errorf("git cat-file: %d results for %d paths", len(lines), len(paths))
	}
	ids := make([]string, len(paths))
	for i, l := range lines {
		if !strings.ContainsRune(l, ' ') { // "<rev>:./<path> missing" (or "ambiguous") otherwise
			ids[i] = l
		}
	}
	return ids, nil
}

// WriteTree writes the tree of parent with changes applied, using a temporary index, and
// returns its id. Change paths are relative to Dir; a change with nil New deletes its file.
// The working tree and the real index are not touched.
func (r Repo) WriteTree(ctx context.Context, parent string, changes []engine.FileChange) (string, error) {
	prefix, err := r.Prefix(ctx)
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
	// Hash every new blob with one git process: write the contents to temp files and feed
	// their paths to hash-object --stdin-paths (--no-filters, as hashing stdin applies none).
	var blobs strings.Builder
	for i, c := range changes {
		if c.New == nil {
			continue
		}
		f := filepath.Join(tmp, "blob-"+strconv.Itoa(i))
		if err := os.WriteFile(f, c.New, 0o600); err != nil {
			return "", err
		}
		blobs.WriteString(f)
		blobs.WriteByte('\n')
	}
	var oids []string
	if blobs.Len() > 0 {
		out, err := r.run(ctx, []byte(blobs.String()), nil, "hash-object", "-w", "--no-filters", "--stdin-paths")
		if err != nil {
			return "", err
		}
		oids = strings.Split(out, "\n")
	}
	var info strings.Builder
	for _, c := range changes {
		p := path.Join(prefix, c.Path)
		if c.New == nil {
			fmt.Fprintf(&info, "0 %s\t%s\n", strings.Repeat("0", 40), p) // mode 0 removes the entry
			continue
		}
		mode := "100644"
		if c.Mode&0o111 != 0 {
			mode = "100755"
		}
		fmt.Fprintf(&info, "%s %s\t%s\n", mode, oids[0], p)
		oids = oids[1:]
	}
	if _, err := r.run(ctx, []byte(info.String()), idx, "update-index", "--index-info"); err != nil {
		return "", err
	}
	return r.run(ctx, nil, idx, "write-tree")
}

// Commit creates a commit on top of parent with changes applied (WriteTree).
func (r Repo) Commit(ctx context.Context, parent string, changes []engine.FileChange, msg, name, email string) (string, error) {
	tree, err := r.WriteTree(ctx, parent, changes)
	if err != nil {
		return "", err
	}
	ident := []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
	args := []string{"commit-tree", tree, "-p", parent, "-F", "-"}
	if s := r.Signer; s != nil {
		args = slices.Concat(s.config, []string{"commit-tree", "-S", tree, "-p", parent, "-F", "-"})
		ident = append(ident, s.env...)
	}
	return r.run(ctx, []byte(msg), ident, args...)
}

// Push sets refs/heads/<branch> on origin to commit if it currently points at expected
// ("" = must not exist). A refused update wraps ErrStale (expected is out of date) or
// ErrRejected (origin declined it).
func (r Repo) Push(ctx context.Context, commit, branch, expected string) error {
	ref := "refs/heads/" + branch
	out, err := r.run(ctx, nil, nil, "push", "--porcelain",
		"--force-with-lease="+ref+":"+expected, "origin", commit+":"+ref)
	if err == nil {
		return nil
	}
	// Porcelain status lines: "!\t<src>:<dst>\t[rejected] (stale info)" or
	// "!\t<src>:<dst>\t[remote rejected] (<reason>)".
	for line := range strings.Lines(out) {
		switch f := strings.Split(strings.TrimSpace(line), "\t"); {
		case len(f) < 3 || f[0] != "!":
		case strings.HasPrefix(f[2], "[remote rejected]"):
			return fmt.Errorf("%w: %s: %w", ErrRejected, f[2], err)
		case strings.HasPrefix(f[2], "[rejected]"):
			return fmt.Errorf("%w: %s: %w", ErrStale, f[2], err)
		}
	}
	return err
}

// DeleteBranch deletes refs/heads/<branch> on origin.
func (r Repo) DeleteBranch(ctx context.Context, branch string) error {
	_, err := r.run(ctx, nil, nil, "push", "origin", ":refs/heads/"+branch)
	return err
}

// BranchTip returns the tip commit of branch on origin. Shallow clones (CI) fetch with
// --depth=1; full clones fetch normally so they are not turned shallow. The raw commit object
// is parsed, so parents are reported even when shallow grafts hide them.
func (r Repo) BranchTip(ctx context.Context, branch string) (platform.Tip, error) {
	shallow, err := r.run(ctx, nil, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return platform.Tip{}, err
	}
	args := []string{"fetch", "--no-tags", "origin", "refs/heads/" + branch}
	if shallow == "true" {
		args = []string{"fetch", "--no-tags", "--depth=1", "origin", "refs/heads/" + branch}
	}
	if _, err := r.run(ctx, nil, nil, args...); err != nil {
		return platform.Tip{}, err
	}
	obj, err := r.run(ctx, nil, nil, "cat-file", "commit", "FETCH_HEAD")
	if err != nil {
		return platform.Tip{}, err
	}
	return parseCommitHeader(obj), nil
}

// parseCommitHeader returns the author email, parents and tree of a raw commit object.
func parseCommitHeader(obj string) platform.Tip {
	var t platform.Tip
	for line := range strings.Lines(obj) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			break // end of header
		}
		if p, ok := strings.CutPrefix(line, "parent "); ok {
			t.Parents = append(t.Parents, p)
		} else if tr, ok := strings.CutPrefix(line, "tree "); ok {
			t.Tree = tr
		} else if a, ok := strings.CutPrefix(line, "author "); ok {
			if i, j := strings.IndexByte(a, '<'), strings.LastIndexByte(a, '>'); i >= 0 && j > i {
				t.Author = a[i+1 : j]
			}
		}
	}
	return t
}
