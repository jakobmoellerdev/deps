package beam

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// hexScheme orders and validates Hex versions.
var hexScheme, _ = versioning.Get(Versioning)

// mirror builds a temporary tree (toolrun.Mirror) for running a tool on dir: the directories
// below dir are recreated (top-level ones named in skip left out, so the tool fetches and
// builds into fresh ones), copies (slash paths relative to dir) written as copies and every
// other file symlinked, so path dependencies outside dir resolve as in the checkout. The
// caller removes tmp; base is the mirror of dir.
func mirror(dir, pattern string, copies map[string][]byte, skip []string) (tmp, base string, err error) {
	return toolrun.Mirror(dir, toolrun.MirrorOptions{
		Pattern:  pattern,
		Files:    copies,
		Skip:     func(rel string, d fs.DirEntry) bool { return d.IsDir() && slices.Contains(skip, rel) },
		Recreate: func(string, fs.DirEntry) bool { return true },
	})
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
	cmd.Env = append(toolrun.Env("NO_COLOR=1"), env...)
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
