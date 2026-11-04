// Package jvmtool runs Gradle and Maven where deps cannot avoid it: regenerating Gradle
// dependency lockfiles and verification metadata (GradleLocker), and the Gradle and Maven
// wrapper scripts and jars (GradleWrapper, MavenWrapper). Each works on a temporary copy of
// the build with the patched manifests overlaid, needs Java, and prefers the build's own
// wrapper script over the tool on PATH.
package jvmtool

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// waitDelay bounds the wait for a killed tool's children (Gradle daemons, forked JVMs) to
// release its output pipes.
const waitDelay = 5 * time.Second

// errNoJava is returned by checkJava when neither PATH nor JAVA_HOME has java.
var errNoJava = errors.New("java not found")

// checkJava reports errNoJava unless java is on PATH or JAVA_HOME/bin/java is executable;
// the wrapper scripts look it up the same way.
func checkJava() error {
	if h := os.Getenv("JAVA_HOME"); h != "" && executable(filepath.Join(h, "bin", "java")) {
		return nil
	}
	if _, err := exec.LookPath("java"); err == nil {
		return nil
	}
	return errNoJava
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

// toolPath returns the wrapper script in dir when it is executable, else tool from PATH, else
// exec.ErrNotFound.
func toolPath(dir, wrapper, tool string) (string, error) {
	if p := filepath.Join(dir, wrapper); executable(p) {
		return p, nil
	}
	p, err := exec.LookPath(tool)
	if err != nil {
		return "", exec.ErrNotFound
	}
	return p, nil
}

// copyBuild copies the build tree at dir into a new temporary directory, named like dir so
// that Gradle's default root project name stays the same, and writes overlay (slash paths
// relative to dir) over it. Directories for which skip reports true are left out; file modes
// and symlinks are kept. The caller removes filepath.Dir of the returned directory.
func copyBuild(dir string, overlay map[string][]byte, skip func(path string, d fs.DirEntry) bool) (string, error) {
	parent, err := os.MkdirTemp("", "deps-jvm-*")
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(parent, filepath.Base(dir))
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(tmp, rel)
		switch t := d.Type(); {
		case t.IsDir():
			if rel != "." && skip(p, d) {
				return filepath.SkipDir
			}
			return os.MkdirAll(dst, 0o755)
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		case t.IsRegular():
			return copyFile(p, dst)
		}
		return nil // sockets, devices
	})
	if err == nil {
		err = writeOverlay(tmp, overlay)
	}
	if err != nil {
		os.RemoveAll(parent)
		return "", err
	}
	return tmp, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writeOverlay writes files (slash paths relative to tmp) into tmp, keeping the mode of a
// file it replaces.
func writeOverlay(tmp string, files map[string][]byte) error {
	for rel, b := range files {
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// hasAny reports whether dir contains one of names.
func hasAny(dir string, names ...string) bool {
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// manifestsChanged reports whether any patched manifest differs from its file in dir.
func manifestsChanged(dir string, manifests map[string][]byte) bool {
	for p, b := range manifests {
		if old, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p))); err != nil || !bytes.Equal(old, b) {
			return true
		}
	}
	return false
}

// readBack returns the files names (slash paths relative to tmp) that exist in tmp and differ
// from their content in old, the siblings before the run.
func readBack(tmp string, old map[string][]byte, names ...string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(names))
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(tmp, filepath.FromSlash(n)))
		switch {
		case err == nil:
			if o, ok := old[n]; !ok || !bytes.Equal(o, b) {
				out[n] = b
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
	}
	return out, nil
}

// run executes argv in dir; a failure carries the last 20 lines of its output (Gradle and
// Maven report errors on stdout as well as stderr).
func run(ctx context.Context, dir string, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	out := &tailBuffer{max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "jvm build tool command", "argv", argv, "dir", dir, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if t := tail(out.b, 20); t != "" {
		return errors.New(t)
	}
	return err
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	b   []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		t.b = append(t.b[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	if over := len(t.b) + len(p) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	t.b = append(t.b, p...)
	return n, nil
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
