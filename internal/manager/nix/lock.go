package nix

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// nixpkgsRepo is the repository of the NixOS channels.
const nixpkgsRepo = "https://github.com/NixOS/nixpkgs"

var (
	// channelRe matches a NixOS channel tarball; the channel is a branch of nixpkgs.
	channelRe = regexp.MustCompile(`^https://(?:channels\.nixos\.org|nixos\.org/channels)/([^/]+)/nixexprs\.tar\.(?:xz|zst|bz2|gz)$`)
	// archiveRe matches a forge archive tarball (`https://<host>/<owner>/<repo>/archive/<ref>.tar.gz`).
	archiveRe = regexp.MustCompile(`^https://([^/]+)/([^/]+)/([^/]+)/archive/.+\.tar\.gz$`)
)

// flakeLock is a flake.lock: its nodes by key, root the key of the flake itself.
type flakeLock struct {
	Nodes map[string]lockNode `json:"nodes"`
	Root  string              `json:"root"`
}

// lockNode is a node of flake.lock; an input is a node key or, for a `follows`, a path.
type lockNode struct {
	Inputs   map[string]json.RawMessage `json:"inputs"`
	Locked   *flakeRef                  `json:"locked"`
	Original *flakeRef                  `json:"original"`
}

// flakeRef is the attribute form of a flake reference, as flake.lock records the reference
// written in flake.nix (original) and what it resolved to (locked).
type flakeRef struct {
	Type  string `json:"type"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Host  string `json:"host"`
	Ref   string `json:"ref"`
	Rev   string `json:"rev"`
	URL   string `json:"url"`
}

// parseLock reads a flake.lock; one without its root node is not a lockfile.
func parseLock(b []byte) (*flakeLock, error) {
	var l flakeLock
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	l.Root = cmp.Or(l.Root, "root")
	if _, ok := l.Nodes[l.Root]; !ok {
		return nil, errors.New("not a flake.lock: no root node")
	}
	return &l, nil
}

// ReadLocks completes the deps of each flake.nix from the flake.lock next to it: the repository
// (Source) of the input's original reference, the branch or version tag it tracks and the
// commit it is locked to (Digest), unless flake.nix pins one. An unreadable flake.lock is
// reported once and its deps are skipped.
func (nixManager) ReadLocks(root string, files [][]dep.Dep) []error {
	var errs []error
	for _, deps := range files {
		if len(deps) == 0 {
			continue
		}
		file := lockPath(deps[0].File)
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if errors.Is(err, fs.ErrNotExist) {
			skipAll(deps, skipNoLock)
			continue
		}
		var l *flakeLock
		if err == nil {
			l, err = parseLock(b)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file, err))
			skipAll(deps, skipUnreadable)
			continue
		}
		for i := range deps {
			if deps[i].Skip == "" {
				l.resolve(&deps[i])
			}
		}
	}
	return errs
}

func skipAll(deps []dep.Dep, reason string) {
	for i := range deps {
		if deps[i].Skip == "" {
			deps[i].Skip = reason
		}
	}
}

// resolve completes d, an input extracted from flake.nix, from its root input node.
func (l *flakeLock) resolve(d *dep.Dep) {
	raw, ok := l.Nodes[l.Root].Inputs[d.Name]
	if !ok {
		d.Skip = skipNotLocked
		return
	}
	var key string
	if json.Unmarshal(raw, &key) != nil {
		d.Skip = skipFollows
		return
	}
	n := l.Nodes[key]
	o, lk := n.Original, n.Locked
	if o == nil || lk == nil {
		d.Skip = skipNotLocked
		return
	}
	source, ref, pin, skip := origin(o)
	if skip == "" && lk.Rev == "" {
		skip = skipNoRev
	}
	if skip != "" {
		d.Skip = skip
		return
	}
	d.Source = source
	// The spans Extract found hold what the lock's original reference says, or nothing.
	if d.Version != ref {
		d.RefSpan = dep.Span{}
	}
	if d.Digest != pin {
		d.DigestSpan = dep.Span{}
	}
	written := ref
	tag := false
	if t, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
		ref, tag = t, true
	} else {
		ref = strings.TrimPrefix(ref, "refs/heads/")
	}
	if d.RefSpan != (dep.Span{}) {
		d.RefSpan.Start += len(written) - len(ref)
	}
	if tag || validTag(ref) {
		d.Datasource, d.Version = dsTags, ref
		if d.RefSpan == (dep.Span{}) {
			d.Skip = skipRefSpan
		}
	} else {
		d.Datasource, d.Version, d.RefSpan = dsRefs, cmp.Or(ref, defaultBranch), dep.Span{}
	}
	if pin == "" {
		d.Digest, d.DigestSpan = lk.Rev, dep.Span{}
		return
	}
	d.Digest = pin
	if d.DigestSpan == (dep.Span{}) && d.Skip == "" {
		d.Skip = skipRevSpan
	}
}

// origin returns the https repository URL of an original flake reference, the ref it tracks
// and the rev it pins as written, or why the input is skipped.
func origin(o *flakeRef) (source, ref, pin, skip string) {
	switch o.Type {
	case "github":
		return "https://" + cmp.Or(o.Host, "github.com") + "/" + o.Owner + "/" + o.Repo, o.Ref, o.Rev, ""
	case "gitlab":
		owner, err := url.PathUnescape(o.Owner) // subgroups: group%2Fsub
		if err != nil {
			return "", "", "", skipURL
		}
		return "https://" + cmp.Or(o.Host, "gitlab.com") + "/" + owner + "/" + o.Repo, o.Ref, o.Rev, ""
	case "sourcehut":
		return "https://" + cmp.Or(o.Host, "git.sr.ht") + "/" + o.Owner + "/" + o.Repo, o.Ref, o.Rev, ""
	case "git":
		if strings.HasPrefix(o.URL, "file:") || strings.HasPrefix(o.URL, "/") {
			return "", "", "", skipPath
		}
		if _, source = manager.GitRepo(o.URL); source == "" {
			return "", "", "", skipURL
		}
		return source, o.Ref, o.Rev, ""
	case "tarball":
		u := strings.TrimPrefix(o.URL, "tarball+")
		if m := channelRe.FindStringSubmatch(u); m != nil {
			return nixpkgsRepo, m[1], "", ""
		}
		m := archiveRe.FindStringSubmatch(u)
		if m == nil {
			return "", "", "", skipTarball
		}
		r := parseRef(u, -1)
		return "https://" + m[1] + "/" + m[2] + "/" + m[3], r.ref, r.rev, ""
	case "indirect":
		return "", "", "", skipIndirect
	case "path":
		return "", "", "", skipPath
	}
	return "", "", "", skipUnsupported + o.Type
}

// Locker is the nix post-processor (engine.PostProcessor, UpdatePinner and LockMaintainer):
// it updates the flake.lock of a flake.nix with `nix flake update <input>…` in a temporary
// mirror of the flake's directory. The working tree is never touched.
type Locker struct {
	// GitHubToken is the access token nix sends to github.com (extra-access-tokens), which
	// lifts the GitHub API's anonymous rate limit; "" = none.
	GitHubToken string
}

// Siblings lists flake.lock.
func (Locker) Siblings() []string { return []string{lockFile} }

// Scope returns the directory of a flake.nix that has a flake.lock next to it.
func (Locker) Scope(_ context.Context, root, manifest string) (string, bool) {
	if path.Base(manifest) != manifestName {
		return "", false
	}
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(lockPath(manifest))))
	return path.Dir(manifest), err == nil && st.Mode().IsRegular()
}

// Process updates the flake.lock for the patched flake.nix.
func (l Locker) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	return l.ProcessUpdates(ctx, dir, manifests, files, nil, nil)
}

// ProcessUpdates re-locks every updated input (manifest updates ups and lockfile-only ones
// locks alike, by dep name) with `nix flake update <input>…`: a branch input moves to the
// branch's newest commit, an input whose ref or rev flake.nix now names to that. Without
// updates `nix flake lock` brings the lock in line with flake.nix. It returns flake.lock when
// it changed; without one it does nothing.
func (l Locker) ProcessUpdates(ctx context.Context, dir string, manifests, files map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	src, ok := manifests[manifestName]
	if !ok {
		var err error
		if src, err = os.ReadFile(filepath.Join(dir, manifestName)); err != nil {
			return nil, err
		}
	}
	var inputs []string
	for _, u := range slices.Concat(ups[manifestName], locks[manifestName]) {
		if !slices.Contains(inputs, u.Dep.Name) {
			inputs = append(inputs, u.Dep.Name)
		}
	}
	if len(inputs) == 0 {
		return l.run(ctx, dir, src, lock, "lock", nil)
	}
	return l.run(ctx, dir, src, lock, "update", inputs)
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): `nix flake update` in a
// mirror of dir moves every input, transitive ones included, to the newest commit its
// reference admits; flake.nix is unchanged. It returns flake.lock when it changed; without one
// it does nothing.
func (l Locker) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[lockFile]
	if !ok {
		return nil, nil
	}
	src, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	return l.run(ctx, dir, src, lock, "update", nil)
}

// run runs `nix flake <sub> [inputs…]` on a mirror of dir holding src as flake.nix and lock as
// flake.lock (mirrorDir) and returns flake.lock when it changed. The flake is named as
// `path:<mirror>`, so nix neither looks for a git repository above the temporary directory nor
// copies more than the mirror.
func (l Locker) run(ctx context.Context, dir string, src, lock []byte, sub string, inputs []string) (map[string][]byte, error) {
	tmp, base, err := mirrorDir(dir, map[string][]byte{manifestName: src, lockFile: lock})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	args := append([]string{"flake", sub}, inputs...)
	if sub == "update" {
		args = append(args, "--flake", "path:"+base)
	} else {
		args = append(args, "path:"+base)
	}
	if err := l.nix(ctx, base, dir, args...); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(filepath.Join(base, lockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{lockFile: out}, nil
}

// mirrorDir builds a temporary tree in which the files of dir in copies (flake.nix and
// flake.lock) are written and every other entry of dir is symlinked, below the mirrored
// repository ancestors (toolrun.MirrorAncestors), so relative path inputs (`path:../lib`)
// resolve as in the checkout. The mirror has no .git: nix reads it as a path flake, so files
// git does not track are visible too. The caller removes tmp; base is the mirror of dir.
func mirrorDir(dir string, copies map[string][]byte) (tmp, base string, err error) {
	if tmp, err = os.MkdirTemp("", "deps-nix-*"); err != nil {
		return "", "", err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(tmp)
		}
	}()
	if base, err = toolrun.MirrorAncestors(dir, tmp); err != nil {
		return "", "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}
	for _, e := range entries {
		if _, ok := copies[e.Name()]; ok || e.Name() == ".git" {
			continue
		}
		if err := os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(base, e.Name())); err != nil {
			return "", "", err
		}
	}
	if err := toolrun.WriteFiles(base, copies); err != nil {
		return "", "", err
	}
	return tmp, base, nil
}

// nix runs `nix --extra-experimental-features 'nix-command flakes' args...` in dir (the
// mirror of scope), with GitHubToken added to NIX_CONFIG as an access token for github.com; a
// failure carries the tail of nix's output, with the mirror's paths shown as scope's.
func (l Locker) nix(ctx context.Context, dir, scope string, args ...string) error {
	bin, err := exec.LookPath("nix")
	if err != nil {
		return fmt.Errorf("nix not found (needed to update %s)", lockFile)
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"--extra-experimental-features", "nix-command flakes"}, args...)...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if l.GitHubToken != "" {
		conf := "extra-access-tokens = github.com=" + l.GitHubToken
		if prev := os.Getenv("NIX_CONFIG"); prev != "" {
			conf = prev + "\n" + conf
		}
		cmd.Env = append(cmd.Env, "NIX_CONFIG="+conf)
	}
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, "nix command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("nix %s in %s: %s", strings.Join(args[:2], " "), scope, msg)
}
