// Package gitcache keeps a warm bare mirror of every repository a long-running deps process
// works on and hands out a fresh detached work tree per cycle.
//
// The mirror is reused across cycles, so only the first cycle of a repository pays for a
// shallow snapshot and later ones pay for a delta. The work tree is new for every cycle and
// never reused, deliberately: the per-directory memos of the datasources and tools (the
// npm .npmrc, the cargo registry configuration, the NuGet configuration of a directory) are
// keyed by absolute path, so a path whose content changed under them would be answered from
// a stale memo. A unique work tree path per cycle makes that impossible.
package gitcache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// lastUseFile records when a mirror was last checked out; GC removes the least recently used
// mirrors first.
const lastUseFile = ".deps-lastuse"

// Cache is the mirror store of one process. Root holds the mirrors as
// <host>/<owner>/<name>.git, its sibling directory work the work trees; both are created on
// demand. A cache belongs to one replica: the work tree names are unique within the process
// and the mirrors are updated under a process-local lock, so two replicas need their own
// directories.
type Cache struct {
	Root     string // <cacheDir>/git
	MaxBytes int64  // size GC keeps the mirrors under; 0 = unlimited

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per mirror path, so one repository is fetched once at a time
}

// Repo is one checkout: the bare mirror it came from and the work tree to read.
type Repo struct {
	Mirror string
	Work   string
	// Base is the branch the work tree is detached at, resolved when the caller passed none.
	Base string
}

// workRoot is where the work trees live: a sibling of the mirrors, so removing it leaves the
// mirrors (the expensive part) alone.
func (c *Cache) workRoot() string { return filepath.Join(filepath.Dir(c.Root), "work") }

// mirrorPath is the mirror directory of fullName on host. fullName may carry nested GitLab
// group segments; they become directories.
func (c *Cache) mirrorPath(host, fullName string) string {
	parts := append([]string{c.Root, safeSegment(host)}, strings.Split(fullName, "/")...)
	for i := 1; i < len(parts); i++ {
		parts[i] = safeSegment(parts[i])
	}
	return filepath.Join(parts...) + ".git"
}

// safeSegment keeps a path segment inside the cache: no separators, no traversal.
func safeSegment(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\\", "-"), "/", "-")
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
}

// lock returns the mutex of mirror, so concurrent cycles of one repository (a stolen lease
// racing its owner) do not fetch into it at the same time.
func (c *Cache) lock(mirror string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	m, ok := c.locks[mirror]
	if !ok {
		m = &sync.Mutex{}
		c.locks[mirror] = m
	}
	return m
}

// Checkout fetches base ("" = the repository's default branch) of url into the mirror of
// fullName on host, creating the mirror on first use, and adds a work tree for it named
// after id. The returned cleanup removes the work tree; the mirror stays for the next cycle.
//
// A non-empty extraHeader (as actions/checkout builds it) is written into the mirror's
// config for scope, the server URL prefix: a linked work tree shares the mirror's config, so
// this is what authenticates the fetches and pushes of the cycle. It is rewritten at every
// checkout, so a rotated token takes effect with the next cycle; callers must hand out
// tokens that outlive one cycle.
func (c *Cache) Checkout(ctx context.Context, id, url, host, fullName, base, scope, extraHeader string) (Repo, func() error, error) {
	mirror := c.mirrorPath(host, fullName)
	l := c.lock(mirror)
	l.Lock()
	defer l.Unlock()

	if err := c.initMirror(ctx, mirror, url); err != nil {
		return Repo{}, nil, err
	}
	if err := setHeader(ctx, mirror, scope, extraHeader); err != nil {
		return Repo{}, nil, err
	}
	if base == "" {
		var err error
		if base, err = defaultBranch(ctx, mirror, url); err != nil {
			return Repo{}, nil, err
		}
	}
	if err := fetchBranch(ctx, mirror, base); err != nil {
		return Repo{}, nil, err
	}
	work := filepath.Join(c.workRoot(), id)
	if err := os.MkdirAll(filepath.Dir(work), 0o700); err != nil {
		return Repo{}, nil, err
	}
	if _, err := git(ctx, mirror, "worktree", "add", "--quiet", "--detach", work, "refs/heads/"+base); err != nil {
		return Repo{}, nil, err
	}
	touch(mirror)
	cleanup := func() error {
		_, err := git(context.WithoutCancel(ctx), mirror, "worktree", "remove", "--force", work)
		if rmErr := os.RemoveAll(work); err == nil {
			err = rmErr
		}
		return err
	}
	return Repo{Mirror: mirror, Work: work, Base: base}, cleanup, nil
}

// initMirror creates the bare mirror of url at dir unless it is there already.
func (c *Cache) initMirror(ctx context.Context, dir, url string) error {
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		// The URL may have changed (a renamed repository, another server name).
		_, err := git(ctx, dir, "remote", "set-url", "origin", url)
		return err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := git(ctx, "", "init", "--bare", "--quiet", dir); err != nil {
		return err
	}
	_, err := git(ctx, dir, "remote", "add", "origin", url)
	return err
}

// setHeader writes the git auth header of scope into the mirror's config, or removes it
// without one. Every work tree of the mirror sends it.
func setHeader(ctx context.Context, mirror, scope, header string) error {
	key := "http." + strings.TrimSuffix(scope, "/") + "/.extraheader"
	if header == "" {
		_, err := git(ctx, mirror, "config", "--unset-all", key)
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 5 { // 5: the key was not set
			return nil
		}
		return err
	}
	_, err := git(ctx, mirror, "config", "--replace-all", key, header)
	return err
}

// defaultBranch resolves the branch HEAD points at on origin with one git process, so it
// costs none of the credential's API budget.
func defaultBranch(ctx context.Context, mirror, url string) (string, error) {
	out, err := git(ctx, mirror, "ls-remote", "--symref", url, "HEAD")
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(out) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ref: "); ok {
			ref, _, _ := strings.Cut(rest, "\t")
			if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok && b != "" {
				return b, nil
			}
		}
	}
	return "", fmt.Errorf("no default branch on %s", url)
}

// ErrNoBranch means origin has no branch of that name (any more).
var ErrNoBranch = errors.New("branch not found on origin")

// fetchBranch updates refs/heads/<base> of the mirror from origin, shallow: the cycle only
// ever reads the tip.
func fetchBranch(ctx context.Context, mirror, base string) error {
	_, err := git(ctx, mirror, "fetch", "--quiet", "--no-tags", "--prune", "--depth=1", "origin",
		"+refs/heads/"+base+":refs/heads/"+base)
	if err != nil && strings.Contains(err.Error(), "couldn't find remote ref") {
		return fmt.Errorf("%s: %w", base, ErrNoBranch)
	}
	return err
}

// touch records the mirror's last use for GC.
func touch(mirror string) {
	p := filepath.Join(mirror, lastUseFile)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
}

// mirrorInfo is one mirror GC considers.
type mirrorInfo struct {
	dir   string
	size  int64
	used  time.Time
	stale bool // no work tree of this mirror can be in flight
}

// GC removes least recently used mirrors until the cache is under MaxBytes, which 0 turns
// off. A mirror with a live work tree is never removed, so a running cycle keeps its
// checkout.
func (c *Cache) GC(ctx context.Context) (removed int, freed int64, err error) {
	if c.MaxBytes <= 0 {
		return 0, 0, nil
	}
	mirrors, total, err := c.scan()
	if err != nil || total <= c.MaxBytes {
		return 0, 0, err
	}
	slices.SortFunc(mirrors, func(a, b mirrorInfo) int { return a.used.Compare(b.used) })
	for _, m := range mirrors {
		if total <= c.MaxBytes {
			break
		}
		if ctx.Err() != nil {
			return removed, freed, ctx.Err()
		}
		if !m.stale {
			continue
		}
		lock := c.lock(m.dir)
		if !lock.TryLock() { // a cycle is fetching into it right now
			continue
		}
		rmErr := os.RemoveAll(m.dir)
		lock.Unlock()
		if rmErr != nil {
			slog.WarnContext(ctx, "git cache gc", "mirror", m.dir, "err", rmErr)
			continue
		}
		removed, freed, total = removed+1, freed+m.size, total-m.size
	}
	return removed, freed, nil
}

// scan lists the mirrors with their size and last use.
func (c *Cache) scan() ([]mirrorInfo, int64, error) {
	var out []mirrorInfo
	var total int64
	err := filepath.WalkDir(c.Root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		case !d.IsDir() || !strings.HasSuffix(p, ".git"):
			return nil
		}
		m := mirrorInfo{dir: p, stale: true}
		if st, err := os.Stat(filepath.Join(p, lastUseFile)); err == nil {
			m.used = st.ModTime()
		}
		if entries, err := os.ReadDir(filepath.Join(p, "worktrees")); err == nil && len(entries) > 0 {
			m.stale = false
		}
		_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // an unreadable entry only costs accuracy
			}
			if info, err := d.Info(); err == nil {
				m.size += info.Size()
			}
			return nil
		})
		total += m.size
		out = append(out, m)
		return fs.SkipDir
	})
	return out, total, err
}

// Size is the size of the mirrors on disk, for the cache metric.
func (c *Cache) Size() int64 {
	_, total, _ := c.scan()
	return total
}

// gitTimeout bounds one git process of the cache: a fetch of a large repository is slow, but
// a hung one must not hold a cycle forever.
const gitTimeout = 20 * time.Minute

// git runs git in dir ("" = the current directory) and returns its trimmed stdout.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "git command", "args", args, "dir", dir, "duration", time.Since(start), "err", err)
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// PruneWork removes work trees left behind by a crashed process: every directory under the
// work root that no live cycle registered. It is called once at startup, before any cycle
// runs.
func (c *Cache) PruneWork(ctx context.Context) error {
	entries, err := os.ReadDir(c.workRoot())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(c.workRoot(), e.Name())); err != nil {
			return err
		}
	}
	mirrors, _, err := c.scan()
	if err != nil {
		return err
	}
	for _, m := range mirrors {
		if _, err := git(ctx, m.dir, "worktree", "prune"); err != nil {
			slog.WarnContext(ctx, "pruning stale work trees", "mirror", m.dir, "err", err)
		}
	}
	return nil
}
