package swift

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		"Package.swift":                   true,
		"apps/macos/Package.swift":        true,
		"Package@swift-5.9.swift":         false,
		"MyPackage.swift":                 false,
		"apps/Package.swift.bak":          false,
		"apps/macos/Package.resolved":     false,
		"Sources/Package/Package.swift/x": false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
}

const fixture = `// swift-tools-version: 5.9
import PackageDescription

let shared: [Package.Dependency] = [.package(url: "https://github.com/o/shared", from: "1.0.0")]

let package = Package(
    name: "Demo", // .package(url: "https://github.com/x/comment", from: "9.0.0")
    /* nested /* .package(url: "https://github.com/x/block", from: "9.0.0") */ */
    dependencies: [
        .package(url: "https://github.com/apple/swift-log.git", from: "1.15.1"),
        .package(url: "git@github.com:apple/swift-nio.git", .upToNextMajor(from: "2.60.0")),
        .package(url: "https://github.com/a/minor", .upToNextMinor(from: "0.4.2")),
        .package(url: "https://github.com/a/half", "1.2.3"..<"2.0.0"),
        .package(url: "https://github.com/a/closed", "1.2.3" ... "1.9.0"),
        .package(url: "https://github.com/a/exact", exact: "3.1.0"),
        .package(name: "Old", url: "https://github.com/a/old.git", .exact("1.39.0")),
        .package(
            url: "https://github.com/a/multi",
            exact: "0.1.6",
            traits: ["Talk", .defaults]),
        Package.Dependency.package(url: "https://gitlab.com/g/sub/proj", from: "4.0.0-beta.1"),
        .package(url: "https://github.com/a/branch", branch: "main"),
        .package(url: "https://github.com/a/oldbranch", .branch("develop")),
        .package(url: "https://github.com/a/rev", revision: "0123456789abcdef0123456789abcdef01234567"),
        .package(url: "https://github.com/a/oldrev", .revision("0123456789abcdef0123456789abcdef01234567")),
        .package(path: "../shared/Kit"),
        .package(id: "mona.LinkedList", from: "1.0.0"),
        .package(url: "file:///tmp/local", from: "1.0.0"),
        .package(url: repoURL, from: "1.0.0"),
        .package(url: "https://github.com/a/var", from: minimum),
        .package(url: "https://github.com/a/ctor", from: Version(1, 2, 3)),
        .package(url: "https://github.com/a/partial", from: "1.2"),
        .package(url: "https://github.com/a/interp", from: "\(major).0.0"),
        .package(url: "https://github.com/a/none"),
    ] + shared,
    targets: [.target(name: "Demo", dependencies: [.product(name: "Logging", package: "swift-log")])]
)
`

// want is the expected extraction of fixture: name, skip, version, range, constraint and the
// text at RefSpan.
type want struct {
	name, skip, version, rng, constraint, ref string
	line                                      int
}

func TestExtract(t *testing.T) {
	for name, src := range map[string]string{"lf": fixture, "crlf": strings.ReplaceAll(fixture, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) {
			deps, err := New().Extract("Package.swift", []byte(src), manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			wants := []want{
				{name: "o/shared", version: "1.0.0", rng: `from: "1.0.0"`, constraint: ">=1.0.0 <2.0.0", ref: `from: "1.0.0"`, line: 4},
				{name: "apple/swift-log", version: "1.15.1", rng: `from: "1.15.1"`, constraint: ">=1.15.1 <2.0.0", ref: `from: "1.15.1"`, line: 10},
				{name: "apple/swift-nio", version: "2.60.0", rng: `.upToNextMajor(from: "2.60.0")`, constraint: ">=2.60.0 <3.0.0", ref: `.upToNextMajor(from: "2.60.0")`, line: 11},
				{name: "a/minor", version: "0.4.2", rng: `.upToNextMinor(from: "0.4.2")`, constraint: ">=0.4.2 <0.5.0", ref: `.upToNextMinor(from: "0.4.2")`, line: 12},
				{name: "a/half", version: "1.2.3", rng: `"1.2.3"..<"2.0.0"`, constraint: ">=1.2.3 <2.0.0", ref: `"1.2.3"..<"2.0.0"`, line: 13},
				{name: "a/closed", version: "1.2.3", rng: `"1.2.3" ... "1.9.0"`, constraint: ">=1.2.3 <=1.9.0", ref: `"1.2.3" ... "1.9.0"`, line: 14},
				{name: "a/exact", version: "3.1.0", ref: "3.1.0", line: 15},
				{name: "a/old", version: "1.39.0", ref: "1.39.0", line: 16},
				{name: "a/multi", version: "0.1.6", ref: "0.1.6", line: 19},
				{name: "g/sub/proj", version: "4.0.0-beta.1", rng: `from: "4.0.0-beta.1"`, constraint: ">=4.0.0-beta.1 <5.0.0", ref: `from: "4.0.0-beta.1"`, line: 21},
				{name: "a/branch", skip: skipBranch, line: 22},
				{name: "a/oldbranch", skip: skipBranch, line: 23},
				{name: "a/rev", skip: skipRevision, line: 24},
				{name: "a/oldrev", skip: skipRevision, line: 25},
				{name: "../shared/Kit", skip: skipLocal, line: 26},
				{name: "mona.LinkedList", skip: skipRegistry, line: 27},
				{name: "file:///tmp/local", skip: skipURL, line: 28},
				{name: "url: repoURL", skip: skipURL, line: 29},
				{name: "a/var", skip: skipRequirement, line: 30},
				{name: "a/ctor", skip: skipRequirement, line: 31},
				{name: "a/partial", skip: skipRequirement, line: 32},
				{name: "a/interp", skip: skipRequirement, line: 33},
				{name: "a/none", skip: skipRequirement, line: 34},
			}
			if len(deps) != len(wants) {
				for _, d := range deps {
					t.Logf("%+v", d)
				}
				t.Fatalf("got %d deps, want %d", len(deps), len(wants))
			}
			for i, w := range wants {
				d := deps[i]
				ref := ""
				if d.RefSpan != (dep.Span{}) {
					ref = src[d.RefSpan.Start:d.RefSpan.End]
				}
				got := want{d.Name, d.Skip, d.Version, d.Range, d.Constraint, ref, d.Line}
				if got != w {
					t.Errorf("dep %d = %+v, want %+v", i, got, w)
				}
				if d.Manager != Name || d.DepType != DepTypeDependencies || d.Datasource != Datasource || d.Versioning != Versioning {
					t.Errorf("dep %d: manager/type/datasource/versioning = %s/%s/%s/%s", i, d.Manager, d.DepType, d.Datasource, d.Versioning)
				}
				if d.Skip == "" && (d.ExtractVersion != extractVersion || !strings.HasPrefix(d.Source, "https://")) {
					t.Errorf("dep %d: source %q, extractVersion %q", i, d.Source, d.ExtractVersion)
				}
			}
			if deps[1].Source != "https://github.com/apple/swift-log" || deps[2].Source != "https://github.com/apple/swift-nio" {
				t.Errorf("sources = %q, %q", deps[1].Source, deps[2].Source)
			}
		})
	}
}

// TestExtractOpenClaw extracts the manifests of openclaw/openclaw (MIT): exact pins with and
// without name:, ranges, traits, multi-line calls and local path packages.
func TestExtractOpenClaw(t *testing.T) {
	tests := map[string][]string{
		"apps/macos/Package.swift": {
			"sindresorhus/KeyboardShortcuts 3.1.0", `swiftlang/swift-subprocess 1.0.0 from: "1.0.0"`, `apple/swift-log 1.15.1 from: "1.15.1"`,
			"sparkle-project/Sparkle 2.10.1", "openclaw/Peekaboo 4.9.6", `pointfreeco/swift-concurrency-extras 1.4.1 from: "1.4.1"`,
			"../shared/OpenClawKit (local package)", "../shared/OpenClawMLXTTSProtocol (local package)", "../swabble (local package)",
		},
		"apps/macos-mlx-tts/Package.swift":                         {"Blaizzy/mlx-audio-swift 0.1.6", "../shared/OpenClawMLXTTSProtocol (local package)"},
		"apps/shared/OpenClawKit/Package.swift":                    {"steipete/ElevenLabsKit 0.1.3", "groue/GRDB.swift 7.11.1", "mgriebling/SwiftMath 1.7.3", "swiftlang/swift-markdown 0.9.0"},
		"apps/shared/OpenClawMLXTTSProtocol/Package.swift":         nil,
		"apps/swabble/Package.swift":                               {"steipete/Commander 0.3.0"},
		"apps/ios/ThirdParty/ESPProvision/Package.swift":           {"apple/swift-protobuf 1.39.0"},
		"apps/macos/Tests/Fixtures/SidebarAttention/Package.swift": {"../../../../shared/OpenClawKit (local package)"},
	}
	for file, wants := range tests {
		src, err := os.ReadFile(filepath.Join("testdata", "openclaw", filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		deps, err := New().Extract(file, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, d := range deps {
			s := d.Name
			switch {
			case d.Skip != "":
				s += " (" + d.Skip + ")"
			case d.Range != "":
				s += " " + d.Version + " " + d.Range
			default:
				s += " " + d.Version
				if string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
					t.Errorf("%s: %s RefSpan %q", file, d.Name, src[d.RefSpan.Start:d.RefSpan.End])
				}
			}
			got = append(got, s)
		}
		if strings.Join(got, "\n") != strings.Join(wants, "\n") {
			t.Errorf("%s:\n%s\nwant\n%s", file, strings.Join(got, "\n"), strings.Join(wants, "\n"))
		}
	}
}

func TestEdits(t *testing.T) {
	deps, err := New().Extract("Package.swift", []byte(fixture), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]dep.Dep{}
	for _, d := range deps {
		byName[d.Name] = d
	}
	tests := []struct {
		name, version, strategy string
		want                    string // the requirement line after the edit
	}{
		{"apple/swift-log", "2.0.0", "", `        .package(url: "https://github.com/apple/swift-log.git", from: "2.0.0"),`},
		{"apple/swift-nio", "3.1.0", "", `        .package(url: "git@github.com:apple/swift-nio.git", .upToNextMajor(from: "3.1.0")),`},
		{"a/minor", "0.5.1", "widen", `        .package(url: "https://github.com/a/minor", .upToNextMinor(from: "0.5.1")),`},
		{"a/half", "3.1.0", "", `        .package(url: "https://github.com/a/half", "3.1.0"..<"4.0.0"),`},
		{"a/half", "3.1.0", "widen", `        .package(url: "https://github.com/a/half", "1.2.3"..<"4.0.0"),`},
		{"a/half", "1.5.0", "bump", `        .package(url: "https://github.com/a/half", "1.5.0"..<"2.0.0"),`},
		{"a/closed", "2.0.0", "", `        .package(url: "https://github.com/a/closed", "2.0.0" ... "2.0.0"),`},
		{"a/closed", "2.0.0", "widen", `        .package(url: "https://github.com/a/closed", "1.2.3" ... "2.0.0"),`},
		{"a/exact", "4.0.0", "", `        .package(url: "https://github.com/a/exact", exact: "4.0.0"),`},
		{"a/old", "1.40.0", "", `        .package(name: "Old", url: "https://github.com/a/old.git", .exact("1.40.0")),`},
		{"a/multi", "0.1.8", "", `            exact: "0.1.8",`},
		{"apple/swift-log", "1.16.0", dep.RangePin, `        .package(url: "https://github.com/apple/swift-log.git", exact: "1.16.0"),`},
		{"apple/swift-log", "1.15.1", dep.RangePin, `        .package(url: "https://github.com/apple/swift-log.git", exact: "1.15.1"),`},
		{"apple/swift-nio", "2.61.0", dep.RangePin, `        .package(url: "git@github.com:apple/swift-nio.git", exact: "2.61.0"),`},
		{"a/minor", "0.4.5", dep.RangePin, `        .package(url: "https://github.com/a/minor", exact: "0.4.5"),`},
		{"a/half", "1.4.0", dep.RangePin, `        .package(url: "https://github.com/a/half", exact: "1.4.0"),`},
		{"a/closed", "1.5.0", dep.RangePin, `        .package(url: "https://github.com/a/closed", exact: "1.5.0"),`},
		{"g/sub/proj", "4.0.0", dep.RangePin, `        Package.Dependency.package(url: "https://gitlab.com/g/sub/proj", exact: "4.0.0"),`},
	}
	for _, tt := range tests {
		d := byName[tt.name]
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: tt.version, RangeStrategy: tt.strategy})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		out, err := patch.Apply([]byte(fixture), edits)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if !strings.Contains(string(out), "\n"+tt.want+"\n") {
			t.Errorf("%s %s %s: line not found: %s", tt.name, tt.version, tt.strategy, tt.want)
		}
		if strings.Count(string(out), "\n") != strings.Count(fixture, "\n") || len(edits) > 2 {
			t.Errorf("%s: %d edits changed more than the versions", tt.name, len(edits))
		}
	}
}

func TestNewRange(t *testing.T) {
	rr := New().(manager.RangeRewriter)
	tests := []struct {
		rng, v, strategy, want string
		ok                     bool
	}{
		{`from: "1.15.1"`, "2.0.0", "", `from: "2.0.0"`, true},
		{`from: "1.15.1"`, "1.16.0", "bump", `from: "1.16.0"`, true},
		{`from: "0.3.0"`, "0.9.0", "bump", `from: "0.9.0"`, true},
		{`from: "1.15.1"`, "1.15.1", "", "", false},
		{`from: "1.15.1"`, "2.0.0", dep.RangePin, `.exact("2.0.0")`, true},
		{`"1.2.3"..<"2.0.0"`, "1.4.0", dep.RangePin, `.exact("1.4.0")`, true},
		{`from: "1.15.1"`, "2.0.0", dep.RangeWiden, `from: "2.0.0"`, true},
		{`.upToNextMinor(from: "1.2.0")`, "1.3.4", "", `.upToNextMinor(from: "1.3.4")`, true},
		{`"1.2.3"..<"1.5.0"`, "1.7.2", "", `"1.7.2"..<"1.8.0"`, true},
		{`"1.2.3"..<"1.5.4"`, "1.7.2", "", `"1.7.2"..<"1.7.3"`, true},
		{`"1.2.3"..<"2.0.0"`, "1.4.0", dep.RangeWiden, "", false},
		{`"1.2.3"..<"2.0.0"`, "2.0.0", dep.RangeWiden, `"1.2.3"..<"3.0.0"`, true},
		{`"1.2.3"..."1.9.0"`, "1.9.0", dep.RangeWiden, "", false},
		{`"1.2.3"..."1.9.0"`, "1.9.1", dep.RangeWiden, `"1.2.3"..."1.9.1"`, true},
	}
	for _, tt := range tests {
		got, ok := rr.NewRange(dep.Dep{Range: tt.rng}, tt.v, tt.strategy)
		if got != tt.want || ok != tt.ok {
			t.Errorf("NewRange(%s, %s, %q) = %q, %v; want %q, %v", tt.rng, tt.v, tt.strategy, got, ok, tt.want, tt.ok)
		}
	}
}

// TestPinForms pins range requirements to 1.4.0 in the form the call compiles with (checked
// against SwiftPM 6.1's PackageDescription): `exact:` from swift-tools-version 5.6 on, which
// also combines with `traits:`; `.exact(…)` before 5.6, with `name:` and without a tools
// version. Only the requirement tokens change.
func TestPinForms(t *testing.T) {
	const url = `url: "https://github.com/apple/swift-log.git"`
	for _, tt := range []struct{ tools, args, want string }{
		{"5.9", url + `, from: "1.2.3"`, url + `, exact: "1.4.0"`},
		{"5.9", url + `,  from :  /* min */ "1.2.3" `, url + `,  exact :  /* min */ "1.4.0" `},
		{"5.6", url + `, .upToNextMajor(from: "1.2.3")`, url + `, exact: "1.4.0"`},
		{"6.1", url + `, .upToNextMinor( from: "1.4.0" ), traits: ["A"]`, url + `, exact: "1.4.0", traits: ["A"]`},
		{"6.0", url + ",\n            \"1.0.0\"..<\"2.0.0\"\n", url + ",\n            exact: \"1.4.0\"\n"},
		{"5.10", url + `, "1.0.0" ... "1.9.0"`, url + `, exact: "1.4.0"`},
		{"5.5", url + `, from: "1.2.3"`, url + `, .exact("1.4.0")`},
		{"5.5", url + `, .upToNextMajor(from: "1.2.3")`, url + `, .exact("1.4.0")`},
		{"5.0", url + `, "1.0.0"..<"2.0.0"`, url + `, .exact("1.4.0")`},
		{"5.9", `name: "swift-log", ` + url + `, from: "1.2.3"`, `name: "swift-log", ` + url + `, .exact("1.4.0")`},
		{"", url + `, from: "1.2.3"`, url + `, .exact("1.4.0")`},
	} {
		head := ""
		if tt.tools != "" {
			head = "// swift-tools-version:" + tt.tools + "\n"
		}
		src := head + "let package = Package(dependencies: [.package(" + tt.args + ")])\n"
		deps, err := New().Extract("Package.swift", []byte(src), manager.Env{})
		if err != nil || len(deps) != 1 || deps[0].Range == "" {
			t.Fatalf("%s: %+v, %v", tt.args, deps, err)
		}
		edits, err := New().Edits(dep.Update{Dep: deps[0], NewVersion: "1.4.0", RangeStrategy: dep.RangePin})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if want := head + "let package = Package(dependencies: [.package(" + tt.want + ")])\n"; err != nil || string(out) != want {
			t.Errorf("%s %q:\n%s\nwant\n%s", tt.tools, tt.args, out, want)
		}
	}
	for src, want := range map[string]bool{
		"// swift-tools-version:5.6": true, "\ufeff\n//swift-tools-version: 6.0.1;(swiftlang-5.0)": true, "// Swift-Tools-Version:10.0": true,
		"// swift-tools-version:5.5": false, "// swift-tools-version:4.2": false, "import PackageDescription": false, "": false,
	} {
		if got := exactLabelTools([]byte(src)); got != want {
			t.Errorf("exactLabelTools(%q) = %v", src, got)
		}
	}
}

func TestParseRepo(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://github.com/apple/swift-log.git": {"apple/swift-log", "https://github.com/apple/swift-log"},
		"https://github.com/apple/swift-log/":    {"apple/swift-log", "https://github.com/apple/swift-log"},
		"git@github.com:apple/swift-nio.git":     {"apple/swift-nio", "https://github.com/apple/swift-nio"},
		"ssh://git@gitlab.com/g/sub/proj.git":    {"g/sub/proj", "https://gitlab.com/g/sub/proj"},
		"http://git.example:8080/o/r":            {"o/r", "http://git.example:8080/o/r"},
		"ssh://git@git.example:2222/o/r":         {"o/r", "https://git.example/o/r"},
		"file:///tmp/x":                          {"", ""},
		"https://github.com/single":              {"", ""},
		"../relative/path":                       {"", ""},
	} {
		if n, s := parseRepo(in); n != want[0] || s != want[1] {
			t.Errorf("parseRepo(%q) = %q, %q; want %q, %q", in, n, s, want[0], want[1])
		}
	}
}
