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
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

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
	err = toolrun.CopyTree(dir, tmp, skip)
	if err == nil {
		err = toolrun.WriteFiles(tmp, overlay)
	}
	if err != nil {
		os.RemoveAll(parent)
		return "", err
	}
	return tmp, nil
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
	cmd, cancel := toolrun.Command(ctx, argv[0], argv[1:]...)
	defer cancel()
	cmd.Dir = dir
	cmd.Env = toolrun.Env()
	out := &toolrun.TailBuffer{Max: 32 << 10}
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
	if t := toolrun.Tail(out.Bytes(), 20); t != "" {
		return errors.New(t)
	}
	return err
}
