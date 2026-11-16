package toolchain

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Detect reads the toolchain versions a repository declares at its root. The files are read
// in the order of their authority: .tool-versions (asdf and mise, which covers every
// ecosystem at once) first, then the ecosystem's own files, then the manifest fields.
// Values deps cannot use (a git ref, a range, "system") are left out, so the pin applies.
func Detect(root string) map[Tool]string {
	want := map[Tool]string{}
	set := func(t Tool, v string) {
		if v = cleanVersion(v); v != "" {
			if _, ok := want[t]; !ok {
				want[t] = v
			}
		}
	}
	for tool, v := range toolVersions(root) {
		set(tool, v)
	}
	// Go: the toolchain line wins over the language version, as the go command itself does.
	gomod := readFile(root, "go.mod")
	set(Go, strings.TrimPrefix(directive(gomod, "toolchain"), "go"))
	set(Go, directive(gomod, "go"))

	set(Node, readFile(root, ".nvmrc"))
	set(Node, readFile(root, ".node-version"))
	pkg := packageJSON(root)
	set(Node, pkg.Engines.Node)
	if name, version, ok := strings.Cut(pkg.PackageManager, "@"); ok {
		// packageManager pins the package manager, not the runtime; only the ones deps can
		// install are tools of their own.
		switch name {
		case "bun":
			set(Bun, version)
		}
	}
	set(Bun, readFile(root, ".bun-version"))
	set(Deno, readFile(root, ".deno-version"))

	set(UV, readFile(root, ".uv-version"))
	set(Rust, rustToolchain(root))
	set(Java, readFile(root, ".java-version"))
	set(Java, readFile(root, ".sdkmanrc-java"))
	set(Terraform, readFile(root, ".terraform-version"))
	set(OpenTofu, readFile(root, ".opentofu-version"))
	set(Julia, readFile(root, ".julia-version"))
	set(Dart, readFile(root, ".dart-version"))
	set(Flutter, readFile(root, ".flutter-version"))
	set(Bazelisk, readFile(root, ".bazeliskversion"))
	set(DotNet, globalJSON(root))
	return want
}

// toolVersionNames maps the names .tool-versions uses to the catalog.
var toolVersionNames = map[string]Tool{
	"golang": Go, "go": Go,
	"nodejs": Node, "node": Node,
	"bun": Bun, "deno": Deno, "uv": UV,
	"rust": Rust, "java": Java, "gradle": Gradle, "maven": Maven,
	"terraform": Terraform, "opentofu": OpenTofu, "julia": Julia,
	"dart": Dart, "flutter": Flutter, "dotnet": DotNet, "dotnet-core": DotNet,
	"bazelisk": Bazelisk, "bazel": Bazelisk,
}

// toolVersions reads .tool-versions (asdf, mise): one `<tool> <version>` line each, the
// first version of a line winning.
func toolVersions(root string) map[Tool]string {
	out := map[Tool]string{}
	for line := range strings.Lines(readFile(root, ".tool-versions")) {
		line, _, _ = strings.Cut(line, "#")
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if t, ok := toolVersionNames[strings.ToLower(f[0])]; ok {
			if _, dup := out[t]; !dup {
				out[t] = f[1]
			}
		}
	}
	return out
}

// cleanVersion drops the decorations version files carry and rejects what cannot name one
// release: a range, a channel, a git ref, "system".
func cleanVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, " \t#"); i >= 0 {
		v = v[:i]
	}
	// temurin-21.0.5, graalvm-22: the distribution is not deps' to choose, the feature
	// version is.
	if _, rest, ok := strings.Cut(v, "-"); ok && !strings.HasPrefix(v, "lts/") {
		if _, err := firstDigit(rest); err == nil {
			v = rest
		}
	}
	switch {
	case v == "" || v == "system" || v == "latest" || v == "stable" || v == "nightly" || v == "beta":
		return ""
	// An LTS line is not a version but names one release; the catalog resolves it.
	case strings.HasPrefix(v, "lts/"):
		return v
	case strings.ContainsAny(v, "^~*<>=|,"):
		return ""
	}
	if _, err := firstDigit(v); err != nil {
		return ""
	}
	return v
}

// firstDigit reports whether s starts with a digit.
func firstDigit(s string) (byte, error) {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return 0, fs.ErrInvalid
	}
	return s[0], nil
}

// readFile reads a small declaration file at the repository root; "" when it is missing or
// too large to be one.
func readFile(root, name string) string {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, 64<<10)
	n, _ := f.Read(b)
	return string(b[:n])
}

// directive returns the value of a go.mod line like `go 1.24.3` or `toolchain go1.24.3`.
func directive(gomod, name string) string {
	for line := range strings.Lines(gomod) {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == name {
			return f[1]
		}
	}
	return ""
}

// packageJSON is the part of package.json that names toolchain versions.
type packageJSONFile struct {
	PackageManager string `json:"packageManager"`
	Engines        struct {
		Node string `json:"node"`
	} `json:"engines"`
}

func packageJSON(root string) packageJSONFile {
	var p packageJSONFile
	_ = json.Unmarshal([]byte(readFile(root, "package.json")), &p)
	return p
}

// globalJSON returns the SDK version global.json pins.
func globalJSON(root string) string {
	var g struct {
		SDK struct {
			Version string `json:"version"`
		} `json:"sdk"`
	}
	_ = json.Unmarshal([]byte(readFile(root, "global.json")), &g)
	return g.SDK.Version
}

// rustToolchain reads rust-toolchain.toml ([toolchain] channel = "1.83.0") or the plain
// rust-toolchain file.
func rustToolchain(root string) string {
	for _, name := range []string{"rust-toolchain.toml", "rust-toolchain"} {
		content := readFile(root, name)
		if content == "" {
			continue
		}
		for line := range strings.Lines(content) {
			line = strings.TrimSpace(line)
			rest, ok := strings.CutPrefix(line, "channel")
			if !ok {
				if name == "rust-toolchain" && !strings.Contains(content, "[toolchain]") {
					return line // the plain file holds the channel itself
				}
				continue
			}
			rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "="))
			return strings.Trim(rest, `"'`)
		}
	}
	return ""
}
