// Package nodetool runs npm where deps cannot avoid it: regenerating package-lock.json.
package nodetool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// PackageLock is the lockfile Locker regenerates. npm-shrinkwrap.json is not handled: npm 12
// no longer reads or writes it.
const PackageLock = "package-lock.json"

const npmrc = ".npmrc"

// unsupported are lockfiles of other package managers; deps cannot keep them in sync.
var unsupported = []string{"yarn.lock", "pnpm-lock.yaml"}

// Locker is the npm post-processor (engine.PostProcessor): it regenerates the lockfile next to
// a patched package.json with `npm install --package-lock-only` in a temporary directory.
type Locker struct{}

// Siblings lists package-lock.json and the (read-only) .npmrc.
func (Locker) Siblings() []string { return []string{PackageLock, npmrc} }

// Process copies the patched package.json, the lockfile and .npmrc into a temporary directory,
// runs npm there without scripts, audit or fund and returns the lockfile when it changed.
// Without a lockfile it does nothing; a yarn.lock or pnpm-lock.yaml in dir is an error.
func (Locker) Process(ctx context.Context, dir string, manifest []byte, files map[string][]byte) (map[string][]byte, error) {
	for _, n := range unsupported {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return nil, fmt.Errorf("lockfile %s not supported", n)
		}
	}
	lock := files[PackageLock]
	if lock == nil {
		return nil, nil
	}
	tmp, err := os.MkdirTemp("", "deps-npm-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	write := map[string][]byte{"package.json": manifest, PackageLock: lock}
	if rc := files[npmrc]; rc != nil {
		write[npmrc] = rc
	}
	for n, b := range write {
		if err := os.WriteFile(filepath.Join(tmp, n), b, 0o644); err != nil {
			return nil, err
		}
	}
	if err := run(ctx, tmp, "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("npm not found in PATH (needed to update %s)", PackageLock)
		}
		return nil, fmt.Errorf("npm install in %s: %w", dir, err)
	}
	out, err := os.ReadFile(filepath.Join(tmp, PackageLock))
	if err != nil {
		return nil, err
	}
	if bytes.Equal(out, lock) {
		return nil, nil
	}
	return map[string][]byte{PackageLock: out}, nil
}

// run executes npm with args in dir; a failure carries the tail of its stderr.
func run(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "npm", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "npm_config_update_notifier=false")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "npm command", "args", args, "dir", dir, "duration", time.Since(start), "err", err)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, exec.ErrNotFound):
		return err
	}
	if t := tail(stderr.Bytes(), 20); t != "" {
		return errors.New(t)
	}
	return err
}

// tail returns the last n lines of b, trimmed.
func tail(b []byte, n int) string {
	b = bytes.TrimSpace(b)
	i := len(b)
	for ; n > 0 && i > 0; n-- {
		i = bytes.LastIndexByte(b[:i], '\n')
		if i < 0 {
			i = 0
			break
		}
	}
	if i > 0 {
		i++ // skip the newline
	}
	return string(b[i:])
}
