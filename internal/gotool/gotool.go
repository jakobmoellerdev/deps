// Package gotool runs the go command where deps cannot avoid it: go env, go list and go mod tidy.
package gotool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultProxy is the GOPROXY used when the go command is unavailable and GOPROXY is unset.
const DefaultProxy = "https://proxy.golang.org,direct"

// GoEnv is the module proxy configuration of the go command.
type GoEnv struct {
	Proxy   string // GOPROXY
	NoProxy string // GONOPROXY, or GOPRIVATE if GONOPROXY is empty
}

// Env reads GOPROXY, GONOPROXY and GOPRIVATE via `go env -json`.
// If go is not in PATH it falls back to the process environment.
func Env(ctx context.Context) (GoEnv, error) {
	var v struct{ GOPROXY, GONOPROXY, GOPRIVATE string }
	out, err := run(ctx, "", nil, "env", "-json", "GOPROXY", "GONOPROXY", "GOPRIVATE")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		v.GOPROXY, v.GONOPROXY, v.GOPRIVATE = os.Getenv("GOPROXY"), os.Getenv("GONOPROXY"), os.Getenv("GOPRIVATE")
	case err != nil:
		return GoEnv{}, err
	default:
		if err := json.Unmarshal(out, &v); err != nil {
			return GoEnv{}, fmt.Errorf("go env: %w", err)
		}
	}
	e := GoEnv{Proxy: v.GOPROXY, NoProxy: v.GONOPROXY}
	if e.Proxy == "" {
		e.Proxy = DefaultProxy
	}
	if e.NoProxy == "" {
		e.NoProxy = v.GOPRIVATE
	}
	return e, nil
}

// ListVersions returns the known versions of module path via `go list -m -versions -json`, run in dir.
func ListVersions(ctx context.Context, dir, path string) ([]string, error) {
	out, err := run(ctx, dir, []string{"GOFLAGS=-mod=mod", "GOWORK=off"}, "list", "-m", "-versions", "-json", path)
	if err != nil {
		return nil, err
	}
	var m struct{ Versions []string }
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("go list -m -versions %s: %w", path, err)
	}
	return m.Versions, nil
}

// Tidy runs `go mod tidy` in dir against copies of mod and sum (sum may be nil) and returns
// the tidied contents. The real go.mod and go.sum in dir are never touched.
func Tidy(ctx context.Context, dir string, mod, sum []byte) (newMod, newSum []byte, err error) {
	tmp, err := os.MkdirTemp("", "deps-tidy-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	modPath, sumPath := filepath.Join(tmp, "go.mod"), filepath.Join(tmp, "go.sum")
	if err := os.WriteFile(modPath, mod, 0o644); err != nil {
		return nil, nil, err
	}
	if sum != nil {
		if err := os.WriteFile(sumPath, sum, 0o644); err != nil {
			return nil, nil, err
		}
	}
	if _, err := run(ctx, dir, []string{"GOWORK=off", "GOFLAGS="}, "mod", "tidy", "-modfile="+modPath); err != nil {
		var ce *cmdError
		if errors.As(err, &ce) && ce.stderr != "" {
			return nil, nil, fmt.Errorf("go mod tidy in %s: %s", dir, ce.stderr)
		}
		return nil, nil, fmt.Errorf("go mod tidy in %s: %w", dir, err)
	}
	if newMod, err = os.ReadFile(modPath); err != nil {
		return nil, nil, err
	}
	if newSum, err = os.ReadFile(sumPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return newMod, newSum, nil
}

// Tidier is the gomod post-processor (engine.PostProcessor): Tidy on copies of go.mod and
// go.sum.
type Tidier struct{}

// Scope is the module directory: every go.mod is post-processed on its own.
func (Tidier) Scope(_, manifest string) (string, bool) {
	if dir, _, ok := strings.CutLast(manifest, "/"); ok {
		return dir, true
	}
	return ".", true
}

// Siblings lists go.sum.
func (Tidier) Siblings() []string { return []string{"go.sum"} }

// Process tidies the patched go.mod and returns the tidied go.mod and go.sum (nil when tidy
// wrote none).
func (Tidier) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	newMod, newSum, err := Tidy(ctx, dir, manifests["go.mod"], files["go.sum"])
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"go.mod": newMod, "go.sum": newSum}, nil
}

// cmdError is a failed go command with the tail of its stderr.
type cmdError struct {
	args   string
	stderr string
	err    error
}

func (e *cmdError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("go %s: %v", e.args, e.err)
	}
	return fmt.Sprintf("go %s: %s", e.args, e.stderr)
}

func (e *cmdError) Unwrap() error { return e.err }

// run executes go with args in dir, with env appended to the process environment, and returns stdout.
func run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "go command", "args", args, "dir", dir, "duration", time.Since(start), "err", err)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, err
		}
		return nil, &cmdError{args: strings.Join(args, " "), stderr: tail(stderr.Bytes(), 20), err: err}
	}
	return stdout.Bytes(), nil
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
