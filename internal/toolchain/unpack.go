package toolchain

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// unpack writes the verified artifact at src into dir. Tar archives go through the image's
// tar, which also handles the xz streams several publishers ship; zip archives and single
// binaries are handled in process. strip is how many leading path components the archive's
// entries carry, name the file name of a Raw download.
func unpack(ctx context.Context, src, dir string, a Archive, strip int, name string) error {
	switch a {
	case TarGz, TarXz:
		return untar(ctx, src, dir, strip)
	case Zip:
		return unzip(src, dir, strip)
	case Raw:
		return rawCopy(src, filepath.Join(dir, name))
	}
	return fmt.Errorf("unknown archive format %q", a)
}

// untar extracts src into dir with the image's tar, which detects the compression itself.
func untar(ctx context.Context, src, dir string, strip int) error {
	args := []string{"-x", "-f", src, "-C", dir}
	if strip > 0 {
		args = append(args, "--strip-components="+strconv.Itoa(strip))
	}
	cmd := exec.CommandContext(ctx, "tar", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unzip extracts src into dir, keeping the executable bit, which the tools' launchers need.
func unzip(src, dir string, strip int) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		rel, ok := stripPrefix(f.Name, strip)
		if !ok {
			continue
		}
		target, err := safeJoin(dir, rel)
		if err != nil {
			return err
		}
		switch {
		case f.FileInfo().IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		case f.Mode()&os.ModeSymlink != 0:
			// Archives deps installs from carry none; a symlink could point out of dir.
			return fmt.Errorf("%s: symlinks are not extracted", f.Name)
		}
		if err := writeZipFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeZipFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := f.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, rc)
	return errors.Join(copyErr, out.Close())
}

// rawCopy installs a single downloaded executable as target.
func rawCopy(src, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

// stripPrefix drops n leading slash path components of name; ok is false when the entry is
// the stripped directory itself or lies above it.
func stripPrefix(name string, n int) (string, bool) {
	p := strings.Trim(filepath.ToSlash(name), "/")
	for range n {
		_, rest, ok := strings.Cut(p, "/")
		if !ok {
			return "", false
		}
		p = rest
	}
	return p, p != ""
}

// safeJoin resolves rel below dir, refusing an entry that would escape it.
func safeJoin(dir, rel string) (string, error) {
	target := filepath.Join(dir, filepath.FromSlash(rel))
	if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: archive entry outside the install directory", rel)
	}
	return target, nil
}
