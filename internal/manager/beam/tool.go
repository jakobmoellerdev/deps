package beam

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// hexScheme orders and validates Hex versions.
var hexScheme, _ = versioning.Get(Versioning)

// mirror builds a temporary tree for running a tool on dir: the directories below dir are
// recreated (top-level ones named in skip left out, so the tool fetches and builds into fresh ones),
// copies (slash paths relative to dir) written as copies and every other file symlinked, below
// the mirrored repository ancestors (toolrun.MirrorAncestors), so path dependencies outside dir
// resolve as in the checkout. The caller removes tmp; base is the mirror of dir.
func mirror(dir, pattern string, copies map[string][]byte, skip []string) (tmp, base string, err error) {
	if tmp, err = os.MkdirTemp("", pattern); err != nil {
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
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		target := filepath.Join(base, rel)
		switch {
		case rel == ".":
			return nil
		case d.IsDir() && rel == d.Name() && slices.Contains(skip, rel):
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		}
		if _, ok := copies[filepath.ToSlash(rel)]; ok {
			return nil
		}
		return os.Symlink(p, target)
	})
	if err != nil {
		return "", "", err
	}
	if err := toolrun.WriteFiles(base, copies); err != nil {
		return "", "", err
	}
	return tmp, base, nil
}

// tool runs `<name> args...` in dir (the mirror of scope) with env added; a failure carries
// the tail of the tool's output, with the mirror's paths shown as scope's.
func tool(ctx context.Context, name, lock, dir, scope string, args []string, env ...string) error {
	bin, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s not found (needed to update %s)", name, lock)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "NO_COLOR=1"), env...)
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err = cmd.Run()
	slog.DebugContext(ctx, name+" command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), dir, scope), err.Error())
	return fmt.Errorf("%s %s in %s: %s", name, args[0], scope, msg)
}
