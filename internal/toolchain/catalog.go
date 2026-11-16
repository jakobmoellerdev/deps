package toolchain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// The tools of the catalog. A tool is here only when its publisher ships a checksum artifact
// deps can verify the download against. The ones that do not stay out, and the manager that
// needs them reports them as not found, exactly as on a machine without them: nix (which
// also needs a daemon or a root-owned store), the Ruby builds of ruby/ruby-builder, the PHP
// builds of static-php-cli and the jsonnet-bundler releases all ship their binaries without
// a digest.
const (
	Go        Tool = "go"
	Node      Tool = "node"
	Bun       Tool = "bun"
	Deno      Tool = "deno"
	UV        Tool = "uv"
	Rust      Tool = "rust"
	Java      Tool = "java"
	Gradle    Tool = "gradle"
	Maven     Tool = "maven"
	Terraform Tool = "terraform"
	OpenTofu  Tool = "opentofu"
	Julia     Tool = "julia"
	Dart      Tool = "dart"
	Flutter   Tool = "flutter"
	DotNet    Tool = "dotnet"
	Bazelisk  Tool = "bazelisk"
	Erlang    Tool = "erlang"
	Elixir    Tool = "elixir"
	Gleam     Tool = "gleam"
)

// Catalog is every installable tool by name.
var Catalog = map[Tool]Entry{
	Go:        goEntry,
	Node:      nodeEntry,
	Bun:       bunEntry,
	Deno:      denoEntry,
	UV:        uvEntry,
	Rust:      rustEntry,
	Java:      javaEntry,
	Gradle:    gradleEntry,
	Maven:     mavenEntry,
	Terraform: terraformEntry,
	OpenTofu:  tofuEntry,
	Julia:     juliaEntry,
	Dart:      dartEntry,
	Flutter:   flutterEntry,
	DotNet:    dotnetEntry,
	Bazelisk:  bazeliskEntry,
	Erlang:    erlangEntry,
	Elixir:    elixirEntry,
	Gleam:     gleamEntry,
}

// provider indexes the catalog by command name: which tool provides `npx`, `mvn`, `tofu`.
var providers = func() map[string]Tool {
	m := map[string]Tool{}
	for tool, e := range Catalog {
		for name := range e.Provides {
			// flutter bundles a dart, but a repository asking for dart alone gets the SDK.
			if _, ok := m[name]; !ok || tool == Tool(name) {
				m[name] = tool
			}
		}
	}
	return m
}()

// ToolOf returns the tool providing the command name, if any.
func ToolOf(name string) (Tool, bool) {
	t, ok := providers[name]
	return t, ok
}

// linux checks the platform a catalog entry supports: Linux on amd64 or arm64, which is what
// `deps serve` runs on.
func linux(goos, goarch string) error {
	if goos != "linux" || (goarch != "amd64" && goarch != "arm64") {
		return fmt.Errorf("%s/%s: %w", goos, goarch, ErrUnsupported)
	}
	return nil
}

// arch renders goarch the way a publisher names it.
func arch(goarch, amd64, arm64 string) string {
	if goarch == "arm64" {
		return arm64
	}
	return amd64
}

// doc fetches a small metadata document once per process; the catalog reads release indexes
// and checksum files through it.
func (m *Manager) doc(ctx context.Context, url string) ([]byte, error) {
	m.mu.Lock()
	b, ok := m.json[url]
	m.mu.Unlock()
	if ok {
		return b, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		httpx.DrainBody(resp)
		return nil, &httpx.StatusError{URL: httpx.Redact(url), Code: resp.StatusCode}
	}
	// A checksum list or release index; anything larger is not one.
	b, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.json == nil {
		m.json = map[string][]byte{}
	}
	m.json[url] = b
	m.mu.Unlock()
	return b, nil
}

// sumOf finds the digest of file in a checksum list (`<hex>  <name>` lines). A document
// holding a bare digest, as the `.sha256` siblings of several publishers do, answers for any
// name.
func sumOf(doc []byte, file string) (string, error) {
	text := strings.TrimSpace(string(doc))
	if fields := strings.Fields(text); len(fields) == 1 && isHex(fields[0]) {
		return strings.ToLower(fields[0]), nil
	}
	for line := range strings.Lines(text) {
		f := strings.Fields(line)
		// coreutils marks a binary read with '*' and rustup writes the name as './<file>'.
		if len(f) >= 2 && isHex(f[0]) && strings.TrimPrefix(strings.TrimPrefix(f[len(f)-1], "*"), "./") == file {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", file)
}

func isHex(s string) bool {
	if len(s) < 32 {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F')
	})
}

// sibling fetches `<url><ext>`, the checksum file publishers put next to an artifact.
func (m *Manager) sibling(ctx context.Context, url, ext, file string) (string, error) {
	doc, err := m.doc(ctx, url+ext)
	if err != nil {
		return "", err
	}
	return sumOf(doc, file)
}

// pathEnv puts dirs in front of the inherited PATH.
func pathEnv(dirs ...string) string {
	return "PATH=" + strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")
}

// --- go ---------------------------------------------------------------------

// goRelease is one entry of https://go.dev/dl/?mode=json.
type goRelease struct {
	Version string `json:"version"` // "go1.27.4"
	Stable  bool   `json:"stable"`
	Files   []struct {
		Filename string `json:"filename"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Kind     string `json:"kind"`
		SHA256   string `json:"sha256"`
	} `json:"files"`
}

const goIndex = "https://go.dev/dl/?mode=json&include=all"

func goReleases(ctx context.Context, m *Manager) ([]goRelease, error) {
	doc, err := m.doc(ctx, goIndex)
	if err != nil {
		return nil, err
	}
	var rs []goRelease
	if err := json.Unmarshal(doc, &rs); err != nil {
		return nil, fmt.Errorf("go release index: %w", err)
	}
	return rs, nil
}

var goEntry = Entry{
	Tool: Go,
	// go.mod writes "1.24.3" and the toolchain line "go1.24.3"; a line without a patch
	// ("1.24") resolves to its newest stable release.
	Resolve: func(ctx context.Context, m *Manager, version string) (string, error) {
		v := strings.TrimPrefix(version, "go")
		if strings.Count(v, ".") >= 2 {
			return v, nil
		}
		rs, err := goReleases(ctx, m)
		if err != nil {
			return "", err
		}
		// The index lists releases newest first, so the first stable match is the newest.
		for _, r := range rs {
			if got := strings.TrimPrefix(r.Version, "go"); r.Stable && strings.HasPrefix(got, v+".") {
				return got, nil
			}
		}
		return "", fmt.Errorf("no stable release of Go %s", v)
	},
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		rs, err := goReleases(ctx, m)
		if err != nil {
			return Download{}, err
		}
		for _, r := range rs {
			if strings.TrimPrefix(r.Version, "go") != version {
				continue
			}
			for _, f := range r.Files {
				if f.OS == goos && f.Arch == goarch && f.Kind == "archive" {
					return Download{URL: "https://go.dev/dl/" + f.Filename, Sum: f.SHA256, Hash: SHA256}, nil
				}
			}
		}
		return Download{}, fmt.Errorf("no %s/%s archive of Go %s", goos, goarch, version)
	},
	Archive:  TarGz,
	Strip:    1, // the tarball carries a go/ directory
	Provides: map[string]string{"go": "bin/go", "gofmt": "bin/gofmt"},
	Env: func(m *Manager, dir string) []string {
		return []string{
			pathEnv(filepath.Join(dir, "bin")),
			"GOROOT=" + dir,
			"GOPATH=" + m.state("go"),
			"GOMODCACHE=" + filepath.Join(m.state("go"), "pkg", "mod"),
			"GOCACHE=" + m.state("go-build"),
		}
	},
}

// --- node -------------------------------------------------------------------

const nodeDist = "https://nodejs.org/dist/"

type nodeRelease struct {
	Version string          `json:"version"` // "v24.11.0"
	LTS     json.RawMessage `json:"lts"`     // false, or the line name
}

var nodeEntry = Entry{
	Tool: Node,
	// .nvmrc and engines.node name a line ("24", "lts/*", "v24.11.0"); only an exact
	// version can be downloaded, so anything else is resolved against the release index.
	Resolve: func(ctx context.Context, m *Manager, version string) (string, error) {
		v := strings.TrimPrefix(strings.TrimSpace(version), "v")
		if strings.Count(v, ".") >= 2 {
			return v, nil
		}
		doc, err := m.doc(ctx, nodeDist+"index.json")
		if err != nil {
			return "", err
		}
		var rs []nodeRelease
		if err := json.Unmarshal(doc, &rs); err != nil {
			return "", fmt.Errorf("node release index: %w", err)
		}
		lts, line := strings.HasPrefix(v, "lts/"), strings.TrimPrefix(v, "lts/")
		for _, r := range rs { // newest first
			got := strings.TrimPrefix(r.Version, "v")
			switch {
			case lts:
				var name string
				if json.Unmarshal(r.LTS, &name) != nil || name == "" {
					continue
				}
				if line != "*" && !strings.EqualFold(name, line) {
					continue
				}
			case !strings.HasPrefix(got, v+"."):
				continue
			}
			return got, nil
		}
		return "", fmt.Errorf("no Node.js release for %q", version)
	},
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		file := "node-v" + version + "-linux-" + arch(goarch, "x64", "arm64") + ".tar.gz"
		base := nodeDist + "v" + version + "/"
		doc, err := m.doc(ctx, base+"SHASUMS256.txt")
		if err != nil {
			return Download{}, err
		}
		sum, err := sumOf(doc, file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: base + file, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  TarGz,
	Strip:    1,
	Provides: map[string]string{"node": "bin/node", "npm": "bin/npm", "npx": "bin/npx", "corepack": "bin/corepack"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(filepath.Join(dir, "bin")), "npm_config_cache=" + m.state("npm")}
	},
}

// --- bun --------------------------------------------------------------------

var bunEntry = Entry{
	Tool: Bun,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		file := "bun-linux-" + arch(goarch, "x64", "aarch64") + ".zip"
		base := "https://github.com/oven-sh/bun/releases/download/bun-v" + strings.TrimPrefix(version, "v") + "/"
		doc, err := m.doc(ctx, base+"SHASUMS256.txt")
		if err != nil {
			return Download{}, err
		}
		sum, err := sumOf(doc, file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: base + file, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  Zip,
	Strip:    1, // bun-linux-<arch>/bun
	Provides: map[string]string{"bun": "bun", "bunx": "bun"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(dir), "BUN_INSTALL_CACHE_DIR=" + m.state("bun")}
	},
}

// --- deno -------------------------------------------------------------------

var denoEntry = Entry{
	Tool: Deno,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		file := "deno-" + arch(goarch, "x86_64", "aarch64") + "-unknown-linux-gnu.zip"
		url := "https://github.com/denoland/deno/releases/download/v" + strings.TrimPrefix(version, "v") + "/" + file
		sum, err := m.sibling(ctx, url, ".sha256sum", file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  Zip,
	Provides: map[string]string{"deno": "deno"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(dir), "DENO_DIR=" + m.state("deno")}
	},
}

// --- uv ---------------------------------------------------------------------

var uvEntry = Entry{
	Tool: UV,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		triple := arch(goarch, "x86_64", "aarch64") + "-unknown-linux-gnu"
		file := "uv-" + triple + ".tar.gz"
		url := "https://github.com/astral-sh/uv/releases/download/" + strings.TrimPrefix(version, "v") + "/" + file
		sum, err := m.sibling(ctx, url, ".sha256", file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  TarGz,
	Strip:    1, // uv-<triple>/uv, uvx
	Provides: map[string]string{"uv": "uv", "uvx": "uvx"},
	Env: func(m *Manager, dir string) []string {
		return []string{
			pathEnv(dir),
			"UV_CACHE_DIR=" + m.state("uv"),
			"UV_PYTHON_INSTALL_DIR=" + m.state("uv-python"),
		}
	},
}

// --- rust -------------------------------------------------------------------

// rustEntry installs through rustup-init, the only standalone Rust artifact with a published
// checksum; rustup then fetches the toolchain itself, verifying it against the signed
// channel manifests.
var rustEntry = Entry{
	Tool: Rust,
	Download: func(ctx context.Context, m *Manager, _, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		triple := arch(goarch, "x86_64", "aarch64") + "-unknown-linux-gnu"
		url := "https://static.rust-lang.org/rustup/dist/" + triple + "/rustup-init"
		sum, err := m.sibling(ctx, url, ".sha256", "rustup-init")
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA256, Name: "rustup-init"}, nil
	},
	Archive: Raw,
	Setup: func(ctx context.Context, m *Manager, dir, version string) error {
		cmd, cancel := toolrun.Command(ctx, filepath.Join(dir, "rustup-init"),
			"-y", "--no-modify-path", "--profile", "minimal", "--default-toolchain", version)
		defer cancel()
		cmd.Env = append(os.Environ(),
			"RUSTUP_HOME="+filepath.Join(dir, "rustup"), "CARGO_HOME="+filepath.Join(dir, "cargo"))
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("rustup-init: %w: %s", err, toolrun.Tail(out, 10))
		}
		return nil
	},
	Provides: map[string]string{"cargo": "cargo/bin/cargo", "rustc": "cargo/bin/rustc", "rustup": "cargo/bin/rustup"},
	Env: func(_ *Manager, dir string) []string {
		return []string{
			pathEnv(filepath.Join(dir, "cargo", "bin")),
			"CARGO_HOME=" + filepath.Join(dir, "cargo"),
			"RUSTUP_HOME=" + filepath.Join(dir, "rustup"),
		}
	},
}

// --- java -------------------------------------------------------------------

// adoptiumAsset is one entry of the Adoptium assets API; it names the download and its
// checksum in the same response, so no second request is needed.
type adoptiumAsset struct {
	Binary struct {
		Package struct {
			Link     string `json:"link"`
			Checksum string `json:"checksum"`
		} `json:"package"`
	} `json:"binary"`
	ReleaseName string `json:"release_name"`
}

var javaEntry = Entry{
	Tool: Java,
	// .java-version and the toolchain blocks name a feature version ("21", "21.0.5",
	// "temurin-21"); Adoptium is asked for the newest release of that feature.
	Resolve: func(_ context.Context, _ *Manager, version string) (string, error) {
		v := strings.TrimSpace(version)
		if _, rest, ok := strings.Cut(v, "-"); ok { // temurin-21, jdk-21
			v = rest
		}
		v = strings.TrimPrefix(v, "1.") // 1.8 is Java 8
		feature, _, _ := strings.Cut(v, ".")
		if _, err := strconv.Atoi(feature); err != nil {
			return "", fmt.Errorf("%q is no Java feature version", version)
		}
		return feature, nil
	},
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		url := "https://api.adoptium.net/v3/assets/latest/" + version + "/hotspot?architecture=" +
			arch(goarch, "x64", "aarch64") + "&image_type=jdk&os=linux&vendor=eclipse"
		doc, err := m.doc(ctx, url)
		if err != nil {
			return Download{}, err
		}
		var as []adoptiumAsset
		if err := json.Unmarshal(doc, &as); err != nil {
			return Download{}, fmt.Errorf("adoptium assets: %w", err)
		}
		for _, a := range as {
			if a.Binary.Package.Link != "" && a.Binary.Package.Checksum != "" {
				return Download{URL: a.Binary.Package.Link, Sum: a.Binary.Package.Checksum, Hash: SHA256}, nil
			}
		}
		return Download{}, fmt.Errorf("no Temurin JDK %s for linux/%s", version, goarch)
	},
	Archive:  TarGz,
	Strip:    1,
	Provides: map[string]string{"java": "bin/java", "javac": "bin/javac", "keytool": "bin/keytool"},
	Env: func(_ *Manager, dir string) []string {
		return []string{pathEnv(filepath.Join(dir, "bin")), "JAVA_HOME=" + dir}
	},
}

// --- gradle and maven -------------------------------------------------------

var gradleEntry = Entry{
	Tool: Gradle,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		url := "https://services.gradle.org/distributions/gradle-" + version + "-bin.zip"
		sum, err := m.sibling(ctx, url, ".sha256", "gradle-"+version+"-bin.zip")
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  Zip,
	Strip:    1,
	Provides: map[string]string{"gradle": "bin/gradle"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(filepath.Join(dir, "bin")), "GRADLE_USER_HOME=" + m.state("gradle")}
	},
}

var mavenEntry = Entry{
	Tool: Maven,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		major, _, _ := strings.Cut(version, ".")
		file := "apache-maven-" + version + "-bin.tar.gz"
		// archive.apache.org, not the dlcdn mirror: dlcdn carries only the current release
		// of each line and drops the tarball of older ones (while keeping their checksum
		// files, so a checksum-only check would not notice).
		url := "https://archive.apache.org/dist/maven/maven-" + major + "/" + version + "/binaries/" + file
		sum, err := m.sibling(ctx, url, ".sha512", file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA512}, nil
	},
	Archive:  TarGz,
	Strip:    1,
	Provides: map[string]string{"mvn": "bin/mvn"},
	Env: func(m *Manager, dir string) []string {
		return []string{
			pathEnv(filepath.Join(dir, "bin")),
			"MAVEN_OPTS=-Dmaven.repo.local=" + m.state("m2"),
		}
	},
}

// --- terraform and opentofu -------------------------------------------------

var terraformEntry = Entry{
	Tool: Terraform,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		v := strings.TrimPrefix(version, "v")
		base := "https://releases.hashicorp.com/terraform/" + v + "/"
		file := "terraform_" + v + "_linux_" + goarch + ".zip"
		doc, err := m.doc(ctx, base+"terraform_"+v+"_SHA256SUMS")
		if err != nil {
			return Download{}, err
		}
		sum, err := sumOf(doc, file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: base + file, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  Zip,
	Provides: map[string]string{"terraform": "terraform"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(dir), "TF_PLUGIN_CACHE_DIR=" + m.state("terraform-plugins")}
	},
}

var tofuEntry = Entry{
	Tool: OpenTofu,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		v := strings.TrimPrefix(version, "v")
		base := "https://github.com/opentofu/opentofu/releases/download/v" + v + "/"
		file := "tofu_" + v + "_linux_" + goarch + ".zip"
		doc, err := m.doc(ctx, base+"tofu_"+v+"_SHA256SUMS")
		if err != nil {
			return Download{}, err
		}
		sum, err := sumOf(doc, file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: base + file, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  Zip,
	Provides: map[string]string{"tofu": "tofu"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(dir), "TF_PLUGIN_CACHE_DIR=" + m.state("terraform-plugins")}
	},
}

// --- julia, dart, flutter ---------------------------------------------------

var juliaEntry = Entry{
	Tool: Julia,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		v := strings.TrimPrefix(version, "v")
		line := v
		if i := strings.Index(v, "."); i >= 0 {
			if j := strings.Index(v[i+1:], "."); j >= 0 {
				line = v[:i+1+j]
			}
		}
		file := "julia-" + v + "-linux-" + arch(goarch, "x86_64", "aarch64") + ".tar.gz"
		url := "https://julialang-s3.julialang.org/bin/linux/" + arch(goarch, "x64", "aarch64") + "/" + line + "/" + file
		doc, err := m.doc(ctx, "https://julialang-s3.julialang.org/bin/checksums/julia-"+v+".sha256")
		if err != nil {
			return Download{}, err
		}
		sum, err := sumOf(doc, file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  TarGz,
	Strip:    1,
	Provides: map[string]string{"julia": "bin/julia"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(filepath.Join(dir, "bin")), "JULIA_DEPOT_PATH=" + m.state("julia")}
	},
}

var dartEntry = Entry{
	Tool: Dart,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		file := "dartsdk-linux-" + arch(goarch, "x64", "arm64") + "-release.zip"
		url := "https://storage.googleapis.com/dart-archive/channels/stable/release/" +
			strings.TrimPrefix(version, "v") + "/sdk/" + file
		sum, err := m.sibling(ctx, url, ".sha256sum", file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  Zip,
	Strip:    1, // dart-sdk/
	Provides: map[string]string{"dart": "bin/dart"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(filepath.Join(dir, "bin")), "PUB_CACHE=" + m.state("pub")}
	},
}

// flutterRelease is one entry of the Flutter release index.
type flutterRelease struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	Archive string `json:"archive"`
	SHA256  string `json:"sha256"`
}

var flutterEntry = Entry{
	Tool: Flutter,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		const base = "https://storage.googleapis.com/flutter_infra_release/releases/"
		doc, err := m.doc(ctx, base+"releases_linux.json")
		if err != nil {
			return Download{}, err
		}
		var index struct {
			Releases []flutterRelease `json:"releases"`
		}
		if err := json.Unmarshal(doc, &index); err != nil {
			return Download{}, fmt.Errorf("flutter release index: %w", err)
		}
		v := strings.TrimPrefix(version, "v")
		for _, r := range index.Releases {
			if r.Version == v && r.SHA256 != "" {
				return Download{URL: base + r.Archive, Sum: r.SHA256, Hash: SHA256}, nil
			}
		}
		return Download{}, fmt.Errorf("no Flutter %s for linux", v)
	},
	Archive:  TarXz,
	Strip:    1, // flutter/
	Provides: map[string]string{"flutter": "bin/flutter"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(filepath.Join(dir, "bin")), "PUB_CACHE=" + m.state("pub"), "FLUTTER_ROOT=" + dir}
	},
}

// --- dotnet -----------------------------------------------------------------

// dotnetReleases is the part of a .NET channel's releases.json deps reads.
type dotnetReleases struct {
	Releases []struct {
		SDK struct {
			Version string `json:"version"`
			Files   []struct {
				RID  string `json:"rid"`
				Name string `json:"name"`
				URL  string `json:"url"`
				Hash string `json:"hash"`
			} `json:"files"`
		} `json:"sdk"`
	} `json:"releases"`
}

var dotnetEntry = Entry{
	Tool: DotNet,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		v := strings.TrimPrefix(version, "v")
		parts := strings.SplitN(v, ".", 3)
		if len(parts) < 2 {
			return Download{}, fmt.Errorf("%q is no .NET SDK version", version)
		}
		channel := parts[0] + "." + parts[1]
		doc, err := m.doc(ctx, "https://builds.dotnet.microsoft.com/dotnet/release-metadata/"+channel+"/releases.json")
		if err != nil {
			return Download{}, err
		}
		var rs dotnetReleases
		if err := json.Unmarshal(doc, &rs); err != nil {
			return Download{}, fmt.Errorf(".NET release index: %w", err)
		}
		rid := "linux-" + arch(goarch, "x64", "arm64")
		for _, r := range rs.Releases {
			// global.json pins a full SDK version; a channel alone takes its newest, which
			// the index lists first.
			if len(parts) == 3 && r.SDK.Version != v {
				continue
			}
			for _, f := range r.SDK.Files {
				if f.RID == rid && strings.HasSuffix(f.Name, ".tar.gz") && f.Hash != "" {
					return Download{URL: f.URL, Sum: f.Hash, Hash: SHA512}, nil
				}
			}
		}
		return Download{}, fmt.Errorf("no .NET SDK %s for %s", v, rid)
	},
	Archive:  TarGz, // the SDK tarball has no top-level directory
	Provides: map[string]string{"dotnet": "dotnet"},
	Env: func(m *Manager, dir string) []string {
		return []string{
			pathEnv(dir),
			"DOTNET_ROOT=" + dir,
			"NUGET_PACKAGES=" + m.state("nuget"),
			"DOTNET_CLI_HOME=" + m.state("dotnet-cli"),
		}
	},
}

// --- bazelisk ---------------------------------------------------------------

var bazeliskEntry = Entry{
	Tool: Bazelisk,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		file := "bazelisk-linux-" + goarch
		url := "https://github.com/bazelbuild/bazelisk/releases/download/v" + strings.TrimPrefix(version, "v") + "/" + file
		sum, err := m.sibling(ctx, url, ".sha256", file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA256, Name: "bazel"}, nil
	},
	Archive: Raw,
	// Bazelisk picks the Bazel version of .bazelversion, so it answers for `bazel` too.
	Provides: map[string]string{"bazel": "bazel", "bazelisk": "bazel"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(dir), "BAZELISK_HOME=" + m.state("bazelisk")}
	},
}

// --- erlang, elixir and gleam ------------------------------------------------

// hexBuilds is the build index of builds.hex.pm: one `<ref> <git-sha> <timestamp> <sha256>`
// line per build, newest first. The digest in it is the publisher's own, which is what makes
// the BEAM toolchain installable at all: the Erlang and Elixir releases themselves ship
// source, not verified Linux binaries.
func (m *Manager) hexBuilds(ctx context.Context, path string) (map[string]string, []string, error) {
	doc, err := m.doc(ctx, "https://builds.hex.pm/builds/"+path+"/builds.txt")
	if err != nil {
		return nil, nil, err
	}
	sums := map[string]string{}
	var refs []string
	for line := range strings.Lines(string(doc)) {
		f := strings.Fields(line)
		if len(f) < 4 || !isHex(f[3]) {
			continue
		}
		if _, dup := sums[f[0]]; !dup {
			sums[f[0]] = strings.ToLower(f[3])
			refs = append(refs, f[0])
		}
	}
	if len(refs) == 0 {
		return nil, nil, fmt.Errorf("builds.hex.pm/%s: no builds listed", path)
	}
	return sums, refs, nil
}

// hexOTPPath is where the OTP builds of a platform live. The builds are made on Ubuntu
// 24.04, whose glibc is older than the image's, so they run on it; there is no build for a
// Debian release.
func hexOTPPath(goarch string) string {
	return "otp/" + arch(goarch, "amd64", "arm64") + "/ubuntu-24.04"
}

var erlangEntry = Entry{
	Tool: Erlang,
	// .tool-versions names a release ("27.3.4.9") or a major ("27"), and Elixir asks for the
	// major it was built against; both resolve to the newest matching build.
	Resolve: func(ctx context.Context, m *Manager, version string) (string, error) {
		v := strings.TrimPrefix(strings.TrimPrefix(version, "OTP-"), "v")
		_, refs, err := m.hexBuilds(ctx, hexOTPPath(m.goarch()))
		if err != nil {
			return "", err
		}
		want := "OTP-" + v
		for _, ref := range refs { // newest first
			if ref == want || strings.HasPrefix(ref, want+".") {
				return ref, nil
			}
		}
		return "", fmt.Errorf("no Erlang/OTP build for %q", version)
	},
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		path := hexOTPPath(goarch)
		sums, _, err := m.hexBuilds(ctx, path)
		if err != nil {
			return Download{}, err
		}
		sum, ok := sums[version]
		if !ok {
			return Download{}, fmt.Errorf("no checksum for Erlang/OTP %s", version)
		}
		return Download{URL: "https://builds.hex.pm/builds/" + path + "/" + version + ".tar.gz", Sum: sum, Hash: SHA256}, nil
	},
	Archive: TarGz,
	Strip:   1,
	// An OTP binary distribution carries no bin/erl until its Install script writes the
	// launchers with the absolute paths of this installation.
	Setup: func(ctx context.Context, _ *Manager, dir, _ string) error {
		cmd, cancel := toolrun.Command(ctx, filepath.Join(dir, "Install"), "-minimal", dir)
		defer cancel()
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("OTP Install: %w: %s", err, toolrun.Tail(out, 10))
		}
		return nil
	},
	Provides: map[string]string{"erl": "bin/erl", "erlc": "bin/erlc", "escript": "bin/escript"},
	Env: func(_ *Manager, dir string) []string {
		return []string{pathEnv(filepath.Join(dir, "bin")), "ERLANG_HOME=" + dir}
	},
}

var elixirEntry = Entry{
	Tool: Elixir,
	// A build is named v<elixir>-otp-<otp major>, so resolving the Elixir version also
	// decides which Erlang it pairs with.
	Resolve: func(ctx context.Context, m *Manager, version string) (string, error) {
		v := strings.TrimPrefix(version, "v")
		if strings.Contains(v, "-otp-") {
			return "v" + v, nil
		}
		_, refs, err := m.hexBuilds(ctx, "elixir")
		if err != nil {
			return "", err
		}
		best := ""
		for _, ref := range refs {
			name, _, ok := strings.Cut(strings.TrimPrefix(ref, "v"), "-otp-")
			if !ok || (name != v && !strings.HasPrefix(name, v+".")) {
				continue
			}
			// Among the builds of one Elixir version, take the newest OTP it was built for.
			if best == "" || ref > best {
				best = ref
			}
		}
		if best == "" {
			return "", fmt.Errorf("no Elixir build for %q", version)
		}
		return best, nil
	},
	Requires: func(version string) []Need {
		if _, otp, ok := strings.Cut(version, "-otp-"); ok {
			return []Need{{Tool: Erlang, Version: otp}}
		}
		return []Need{{Tool: Erlang}}
	},
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		sums, _, err := m.hexBuilds(ctx, "elixir")
		if err != nil {
			return Download{}, err
		}
		sum, ok := sums[version]
		if !ok {
			return Download{}, fmt.Errorf("no checksum for Elixir %s", version)
		}
		return Download{URL: "https://builds.hex.pm/builds/elixir/" + version + ".zip", Sum: sum, Hash: SHA256}, nil
	},
	Archive:  Zip,
	Provides: map[string]string{"elixir": "bin/elixir", "elixirc": "bin/elixirc", "mix": "bin/mix", "iex": "bin/iex"},
	Env: func(m *Manager, dir string) []string {
		return []string{
			pathEnv(filepath.Join(dir, "bin")),
			"MIX_HOME=" + m.state("mix"),
			"HEX_HOME=" + m.state("hex"),
			// A slim image carries no locale, and Elixir then warns on every invocation
			// that the VM runs with latin1 name encoding; +fnu is the documented fix.
			"ELIXIR_ERL_OPTIONS=+fnu",
			"LANG=C.UTF-8",
		}
	},
}

var gleamEntry = Entry{
	Tool: Gleam,
	Download: func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error) {
		if err := linux(goos, goarch); err != nil {
			return Download{}, err
		}
		v := strings.TrimPrefix(version, "v")
		// The musl build is static, so it does not care which libc the image has.
		file := "gleam-v" + v + "-" + arch(goarch, "x86_64", "aarch64") + "-unknown-linux-musl.tar.gz"
		url := "https://github.com/gleam-lang/gleam/releases/download/v" + v + "/" + file
		sum, err := m.sibling(ctx, url, ".sha256", file)
		if err != nil {
			return Download{}, err
		}
		return Download{URL: url, Sum: sum, Hash: SHA256}, nil
	},
	Archive:  TarGz,
	Provides: map[string]string{"gleam": "gleam"},
	Env: func(m *Manager, dir string) []string {
		return []string{pathEnv(dir), "HEX_HOME=" + m.state("hex")}
	},
}

// Tools lists the catalog in a stable order, for help output and tests.
func Tools() []Tool {
	out := make([]Tool, 0, len(Catalog))
	for t := range Catalog {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}
