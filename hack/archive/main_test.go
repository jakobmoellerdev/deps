package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

const epoch = 1767225600 // 2026-01-01T00:00:00Z

type member struct {
	name string
	mode fs.FileMode
	data string
}

// fixture writes binaries, completions and a license into a temp dir and returns their paths.
func fixture(t *testing.T, binaries ...string) (bin, completions, license string) {
	t.Helper()
	dir := t.TempDir()
	bin, completions, license = filepath.Join(dir, "bin"), filepath.Join(dir, "completions"), filepath.Join(dir, "LICENSE")
	for _, d := range []string{bin, completions} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range binaries {
		if err := os.WriteFile(filepath.Join(bin, b), []byte("binary "+b), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		if err := os.WriteFile(filepath.Join(completions, shell), []byte(shell+" script"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(license, []byte("license"), 0o600); err != nil {
		t.Fatal(err)
	}
	return bin, completions, license
}

func readTarXz(t *testing.T, path string) []member {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := xz.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var got []member
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return got
		}
		if err != nil {
			t.Fatal(err)
		}
		if !h.ModTime.Equal(time.Unix(epoch, 0)) || h.Uid != 0 || h.Gid != 0 || h.Uname != "root" || h.Gname != "root" {
			t.Errorf("%s: mtime %v, owner %d:%d %s:%s", h.Name, h.ModTime, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, member{h.Name, h.FileInfo().Mode(), string(data)})
	}
}

func readZip(t *testing.T, path string) []member {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var got []member
	for _, f := range zr.File {
		if !f.Modified.Equal(time.Unix(epoch, 0)) {
			t.Errorf("%s: mtime %v", f.Name, f.Modified)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, member{f.Name, f.Mode(), string(data)})
	}
	return got
}

func equal(t *testing.T, name string, got, want []member) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %+v, want %+v", name, i, got[i], want[i])
		}
	}
}

func TestRun(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1767225600")
	bin, completions, license := fixture(t, "deps_linux_amd64", "deps_darwin_arm64", "deps_windows_amd64.exe", "README")
	outs := [2]string{t.TempDir(), t.TempDir()}
	for _, out := range outs {
		if err := run(bin, completions, license, out); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"deps_linux_amd64.tar.xz", "deps_darwin_arm64.tar.xz", "deps_windows_amd64.zip"} {
		a, err := os.ReadFile(filepath.Join(outs[0], name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(outs[1], name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs between runs", name)
		}
	}
	entries, err := os.ReadDir(outs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("want 3 archives, got %v", entries)
	}
	equal(t, "linux", readTarXz(t, filepath.Join(outs[0], "deps_linux_amd64.tar.xz")), []member{
		{"LICENSE", 0o644, "license"},
		{"completions/", fs.ModeDir | 0o755, ""},
		{"completions/_deps", 0o644, "zsh script"},
		{"completions/deps.bash", 0o644, "bash script"},
		{"completions/deps.fish", 0o644, "fish script"},
		{"deps", 0o755, "binary deps_linux_amd64"},
	})
	equal(t, "windows", readZip(t, filepath.Join(outs[0], "deps_windows_amd64.zip")), []member{
		{"LICENSE", 0o644, "license"},
		{"completions/", fs.ModeDir | 0o755, ""},
		{"completions/deps.ps1", 0o644, "powershell script"},
		{"deps.exe", 0o755, "binary deps_windows_amd64.exe"},
	})
}

func TestRunErrors(t *testing.T) {
	for _, tt := range []struct {
		name, epoch, completions, want string
		binaries                       []string
	}{
		{"no completions", "1", "-", "-completions is required", []string{"deps_linux_amd64"}},
		{"no epoch", "", "", "SOURCE_DATE_EPOCH", []string{"deps_linux_amd64"}},
		{"no binaries", "1", "", "no deps_<os>_<arch> binaries", nil},
		{"windows without exe", "1", "", "windows binaries end in .exe", []string{"deps_windows_amd64"}},
		{"linux with exe", "1", "", "only windows binaries end in .exe", []string{"deps_linux_amd64.exe"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SOURCE_DATE_EPOCH", tt.epoch)
			bin, completions, license := fixture(t, tt.binaries...)
			if tt.completions == "-" {
				completions = ""
			}
			err := run(bin, completions, license, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
}
