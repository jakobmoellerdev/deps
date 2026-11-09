// Command archive writes the release archives from the release binaries:
//
//	go run ./hack/archive -bin dist/bin -completions <dir> -license LICENSE -out dist
//
// For every bin/deps_<os>_<arch> it writes out/deps_<os>_<arch>.tar.xz holding deps, LICENSE and
// completions/{deps.bash,_deps,deps.fish}; for every bin/deps_windows_<arch>.exe it writes
// out/deps_windows_<arch>.zip holding deps.exe, LICENSE and completions/deps.ps1. <dir> holds the
// output of `deps completion <shell>` in the files bash, zsh, fish and powershell.
//
// The archives are reproducible: sorted entries, fixed modes, owner root (uid and gid 0), every
// mtime SOURCE_DATE_EPOCH, and the compressors are the pinned Go ones (ulikunitz/xz and
// compress/flate), so the bytes do not depend on the host's tar, xz or zip.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// entry is one archive member; a nil data is a directory.
type entry struct {
	name string
	mode fs.FileMode
	data []byte
}

var binary = regexp.MustCompile(`^deps_([a-z0-9]+)_([a-z0-9]+)(\.exe)?$`)

func main() {
	bin := flag.String("bin", "dist/bin", "directory with the release binaries deps_<os>_<arch>[.exe]")
	completions := flag.String("completions", "", "directory with the completion scripts bash, zsh, fish and powershell")
	license := flag.String("license", "LICENSE", "license file")
	out := flag.String("out", "dist", "directory the archives are written to")
	flag.Parse()
	if err := run(*bin, *completions, *license, *out); err != nil {
		fmt.Fprintln(os.Stderr, "archive:", err)
		os.Exit(1)
	}
}

func run(bin, completions, license, out string) error {
	if completions == "" {
		return errors.New("-completions is required")
	}
	epoch, err := strconv.ParseInt(os.Getenv("SOURCE_DATE_EPOCH"), 10, 64)
	if err != nil {
		return fmt.Errorf("SOURCE_DATE_EPOCH: %w", err)
	}
	mtime := time.Unix(epoch, 0).UTC()
	read := func(dir, name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) }
	lic, err := os.ReadFile(license)
	if err != nil {
		return err
	}
	scripts := map[string][]byte{}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		if scripts[shell], err = read(completions, shell); err != nil {
			return err
		}
	}
	files, err := os.ReadDir(bin)
	if err != nil {
		return err
	}
	n := 0
	for _, f := range files {
		m := binary.FindStringSubmatch(f.Name())
		if m == nil || !f.Type().IsRegular() {
			continue
		}
		exe, err := read(bin, f.Name())
		if err != nil {
			return err
		}
		base := filepath.Join(out, "deps_"+m[1]+"_"+m[2])
		if m[1] == "windows" {
			if m[3] == "" {
				return fmt.Errorf("%s: windows binaries end in .exe", f.Name())
			}
			err = writeZip(base+".zip", mtime, []entry{
				{"deps.exe", 0o755, exe},
				{"LICENSE", 0o644, lic},
				{"completions/", fs.ModeDir | 0o755, nil},
				{"completions/deps.ps1", 0o644, scripts["powershell"]},
			})
		} else {
			if m[3] != "" {
				return fmt.Errorf("%s: only windows binaries end in .exe", f.Name())
			}
			err = writeTarXz(base+".tar.xz", mtime, []entry{
				{"deps", 0o755, exe},
				{"LICENSE", 0o644, lic},
				{"completions/", fs.ModeDir | 0o755, nil},
				{"completions/deps.bash", 0o644, scripts["bash"]},
				{"completions/_deps", 0o644, scripts["zsh"]},
				{"completions/deps.fish", 0o644, scripts["fish"]},
			})
		}
		if err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return fmt.Errorf("no deps_<os>_<arch> binaries in %s", bin)
	}
	return nil
}

func sorted(entries []entry) []entry {
	return slices.SortedFunc(slices.Values(entries), func(a, b entry) int { return strings.Compare(a.name, b.name) })
}

func writeTarXz(path string, mtime time.Time, entries []entry) error {
	var buf bytes.Buffer
	zw, err := xz.WriterConfig{DictCap: 32 << 20, CheckSum: xz.CRC64}.NewWriter(&buf)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	for _, e := range sorted(entries) {
		h := &tar.Header{
			Name:    e.name,
			Mode:    int64(e.mode.Perm()),
			ModTime: mtime,
			Uname:   "root",
			Gname:   "root",
			Format:  tar.FormatUSTAR,
		}
		if e.mode.IsDir() {
			h.Typeflag = tar.TypeDir
		} else {
			h.Typeflag = tar.TypeReg
			h.Size = int64(len(e.data))
		}
		if err := tw.WriteHeader(h); err != nil {
			return fmt.Errorf("%s: %s: %w", path, e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeZip(path string, mtime time.Time, entries []entry) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range sorted(entries) {
		h := &zip.FileHeader{Name: e.name, Modified: mtime, Method: zip.Deflate}
		if e.mode.IsDir() {
			h.Method = zip.Store
		}
		h.SetMode(e.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return fmt.Errorf("%s: %s: %w", path, e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
