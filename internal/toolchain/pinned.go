package toolchain

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Some publishers ship Linux builds with no digest at all: ruby/ruby-builder (which
// `ruby/setup-ruby` itself consumes unverified) and static-php-cli. Those tools would be
// unavailable under the catalog's rule that nothing is installed unverified.
//
// The way out is to be the publisher of the digest: digests.json records the sha256 of each
// artifact deps is willing to install, checked into this repository and reviewed like code,
// and Ensure verifies against it exactly as it verifies a publisher checksum. That moves the
// trust anchor rather than removing it — a digest here was computed by whoever ran
// `task toolchain:digests` and accepted in review — so it is deliberately limited to the
// versions listed: a repository asking for a version that is not in the file gets the same
// "<tool> not found" path as a machine without the tool, never an unverified download.
//
//go:embed digests.json
var digestsJSON []byte

// pinnedDigests maps tool -> version -> "<goos>-<goarch>" -> sha256.
var pinnedDigests = func() map[Tool]map[string]map[string]string {
	var m map[Tool]map[string]map[string]string
	if err := json.Unmarshal(digestsJSON, &m); err != nil {
		panic("toolchain: digests.json: " + err.Error())
	}
	return m
}()

// PinnedVersions lists the versions of a tool digests.json covers, newest-last.
func PinnedVersions(tool Tool) []string {
	out := make([]string, 0, len(pinnedDigests[tool]))
	for v := range pinnedDigests[tool] {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// PinnedURL is the artifact of a self-digested tool. It is shared with the digest generator,
// so the file can never describe a URL the catalog would not fetch.
func PinnedURL(tool Tool, version, goos, goarch string) (string, bool) {
	if goos != "linux" || (goarch != "amd64" && goarch != "arm64") {
		return "", false
	}
	switch tool {
	case Ruby:
		// The ubuntu-24.04 build: its glibc is older than the image's, so it runs there.
		return "https://github.com/ruby/ruby-builder/releases/download/ruby-" + version +
			"/ruby-" + version + "-ubuntu-24.04-" + arch(goarch, "x64", "arm64") + ".tar.gz", true
	case PHP:
		return "https://dl.static-php.dev/static-php-cli/common/php-" + version +
			"-cli-linux-" + arch(goarch, "x86_64", "aarch64") + ".tar.gz", true
	}
	return "", false
}

// pinnedDownload builds the Download of a self-digested tool.
func pinnedDownload(tool Tool, version, goos, goarch string) (Download, error) {
	url, ok := PinnedURL(tool, version, goos, goarch)
	if !ok {
		return Download{}, fmt.Errorf("%s/%s: %w", goos, goarch, ErrUnsupported)
	}
	sum := pinnedDigests[tool][version][goos+"-"+goarch]
	if sum == "" {
		return Download{}, fmt.Errorf("%s %s is not in digests.json (have %s; add it with `task toolchain:digests`): %w",
			tool, version, strings.Join(PinnedVersions(tool), ", "), exec.ErrNotFound)
	}
	return Download{URL: url, Sum: sum, Hash: SHA256, Name: string(tool)}, nil
}

var rubyEntry = Entry{
	Tool: Ruby,
	Download: func(_ context.Context, _ *Manager, version, goos, goarch string) (Download, error) {
		return pinnedDownload(Ruby, version, goos, goarch)
	},
	Archive: TarGz,
	Strip:   1, // the tarball carries an x64/ or arm64/ directory
	// The build is made for a fixed path: every launcher's shebang names
	// /opt/hostedtoolcache/Ruby/<v>/<arch>/bin/ruby, which is where the GitHub action
	// extracts it. Point them at this installation instead; the interpreter also needs its
	// shared library and standard library through the environment below.
	Setup: func(_ context.Context, _ *Manager, dir, _ string, _ []Installed) error {
		bins, err := os.ReadDir(filepath.Join(dir, "bin"))
		if err != nil {
			return err
		}
		for _, e := range bins {
			p := filepath.Join(dir, "bin", e.Name())
			b, err := os.ReadFile(p)
			if err != nil || !strings.HasPrefix(string(b), "#!") {
				continue
			}
			line, rest, _ := strings.Cut(string(b), "\n")
			if !strings.HasSuffix(line, "/bin/ruby") {
				continue
			}
			st, err := os.Stat(p)
			if err != nil {
				return err
			}
			fixed := "#!" + filepath.Join(dir, "bin", "ruby") + "\n" + rest
			if err := os.WriteFile(p, []byte(fixed), st.Mode().Perm()); err != nil {
				return err
			}
		}
		return nil
	},
	Provides: map[string]string{"ruby": "bin/ruby", "gem": "bin/gem", "bundle": "bin/bundle", "bundler": "bin/bundler", "rake": "bin/rake"},
	Env: func(m *Manager, dir string) []string {
		lib := filepath.Join(dir, "lib")
		// The build's rubylibdir is baked to the hosted-toolcache path, so the standard
		// library is reached through RUBYLIB and libruby through LD_LIBRARY_PATH.
		std := filepath.Join(lib, "ruby", rubyAPIVersion(dir))
		return []string{
			pathEnv(filepath.Join(dir, "bin"), filepath.Join(m.state("gem"), "bin")),
			"LD_LIBRARY_PATH=" + lib,
			"RUBYLIB=" + std + string(os.PathListSeparator) + filepath.Join(std, rubyPlatform(m)),
			"GEM_HOME=" + m.state("gem"),
			"GEM_PATH=" + m.state("gem"),
		}
	},
}

// rubyAPIVersion is the lib/ruby/<x.y.0> directory of an installation; "" when it cannot be
// read, which leaves RUBYLIB pointing at a path that does not exist and makes the failure
// visible rather than silent.
func rubyAPIVersion(dir string) string {
	entries, err := os.ReadDir(filepath.Join(dir, "lib", "ruby"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() && startsWithDigit(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

// rubyPlatform is the architecture directory inside the standard library.
func rubyPlatform(m *Manager) string {
	if m.goarch() == "arm64" {
		return "aarch64-linux"
	}
	return "x86_64-linux"
}

var phpEntry = Entry{
	Tool: PHP,
	Download: func(_ context.Context, _ *Manager, version, goos, goarch string) (Download, error) {
		return pinnedDownload(PHP, version, goos, goarch)
	},
	// A single statically linked binary at the root of the archive.
	Archive:  TarGz,
	Provides: map[string]string{"php": "php"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(dir), "COMPOSER_HOME=" + m.state("composer")}
	},
}
