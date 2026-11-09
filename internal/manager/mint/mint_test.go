package mint

import (
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"Mintfile": true, "ios/Mintfile": true, "Mintfile.lock": false, "aMintfile": false, "mintfile": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
}

// TestExtractReal reads the Mintfiles of bitwarden/ios, ProtonMail/ios-mail, Shopify/tophat,
// HeroTransitions/Hero, firebase/quickstart-ios and DroidKaigi/conference-app-2020
// (fetched 2026-11-09).
func TestExtractReal(t *testing.T) {
	type want struct{ name, version, skip string }
	for file, wants := range map[string][]want{
		"ios.Mintfile": {
			{"mono0926/LicensePlist", "3.27.2", ""}, {"nicklockwood/SwiftFormat", "0.59.0", ""}, {"SwiftGen/SwiftGen", "6.6.3", ""},
			{"realm/SwiftLint", "0.63.1", ""}, {"yonaskolb/xcodegen", "2.44.1", ""}, {"krzysztofzablocki/Sourcery", "2.3.0", ""},
		},
		"ios-mail.Mintfile": {
			{"krzysztofzablocki/Sourcery", "2.3.0", ""}, {"cpisciotta/xcbeautify", "3.1.2", ""},
			{"yonaskolb/xcodegen", "2.44.1", ""}, {"mono0926/LicensePlist", "3.27.2", ""},
		},
		"tophat.Mintfile": {{"realm/SwiftLint", "0.54.0", ""}},
		"Hero.Mintfile": {
			{"realm/SwiftLint", "", skipUnspecified}, {"danger/swift", "", skipUnspecified}, {"nicklockwood/SwiftFormat", "", skipUnspecified},
		},
		"quickstart-ios.Mintfile": {{"nicklockwood/SwiftFormat", "0.45.5", ""}},
		"conference-app-2020.Mintfile": {
			{"yonaskolb/xcodegen", "2.11.0", ""}, {"realm/SwiftLint", "0.38.2", ""}, {"nicklockwood/SwiftFormat", "0.44.0", ""},
			{"SwiftGen/SwiftGen", "6.1.0", ""}, {"mono0926/LicensePlist", "2.13.0", ""},
		},
	} {
		src, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		ds, err := New().Extract(file, src, manager.Env{})
		if err != nil || len(ds) != len(wants) {
			t.Fatalf("%s: %+v, %v", file, ds, err)
		}
		for i, w := range wants {
			d := ds[i]
			if d.Name != w.name || d.Version != w.version || d.Skip != w.skip || d.Source != "https://github.com/"+w.name ||
				d.Datasource != "git-tags" || d.Versioning != "tag" || d.DepType != DepType || string(src[d.RefSpan.Start:d.RefSpan.End]) != w.version {
				t.Errorf("%s dep %d: %+v", file, i, d)
			}
		}
		edits, err := New().Edits(dep.Update{Dep: ds[0], NewVersion: "9.9.9"})
		if err != nil || ds[0].Skip != "" {
			continue
		}
		out, err := patch.Apply(src, edits)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := New().Extract(file, out, manager.Env{}); got[0].Version != "9.9.9" || got[0].Line != ds[0].Line {
			t.Errorf("%s: after edit %s", file, out)
		}
	}
}

func TestExtract(t *testing.T) {
	type want struct {
		name, source, version, skip string
		line                        int
	}
	src := "# tools\n" +
		"\n" +
		"  realm/SwiftLint @ 0.50.3   # lint\r\n" +
		"yonaskolb/XcodeGen@v2.38.0\n" +
		"github.com/apple/swift-format.git@509.0.0\n" +
		"gitlab.com/group/tool@1.0.0\n" +
		"https://git.example.com:8443/team/tool@2.0.0\n" +
		"git@github.com:owner/repo.git@1.2.3\n" +
		"git@github.com:owner/repo.git\n" +
		"ssh://git@github.com/owner/repo.git@3.0.0\n" +
		"ssh://git@github.com/owner/repo.git\n" +
		"owner/repo@\n" +
		"owner/repo@main\n" +
		"solo@1.0.0\n" +
		"a@b@c@d\n" +
		"#owner/commented@1.0.0\n" +
		"owner/last@4.0.0"
	wants := []want{
		{"realm/SwiftLint", "https://github.com/realm/SwiftLint", "0.50.3", "", 3},
		{"yonaskolb/XcodeGen", "https://github.com/yonaskolb/XcodeGen", "v2.38.0", "", 4},
		{"apple/swift-format", "https://github.com/apple/swift-format", "509.0.0", "", 5},
		{"group/tool", "https://gitlab.com/group/tool", "1.0.0", "", 6},
		{"team/tool", "https://git.example.com:8443/team/tool", "2.0.0", "", 7},
		{"owner/repo", "https://github.com/owner/repo", "1.2.3", "", 8},
		{"owner/repo", "https://github.com/owner/repo", "", skipUnspecified, 9},
		{"owner/repo", "https://github.com/owner/repo", "3.0.0", "", 10},
		{"owner/repo", "https://github.com/owner/repo", "", skipUnspecified, 11},
		{"owner/repo", "https://github.com/owner/repo", "", skipUnspecified, 12},
		{"owner/repo", "https://github.com/owner/repo", "main", "", 13},
		{"solo", "", "", skipRepo, 14},
		{"a@b@c@d", "", "", skipRepo, 15},
		{"owner/last", "https://github.com/owner/last", "4.0.0", "", 17},
	}
	ds, err := New().Extract("Mintfile", []byte(src), manager.Env{})
	if err != nil || len(ds) != len(wants) {
		t.Fatalf("%+v, %v", ds, err)
	}
	for i, w := range wants {
		d := ds[i]
		if d.Name != w.name || d.Source != w.source || d.Version != w.version || d.Skip != w.skip || d.Line != w.line ||
			src[d.RefSpan.Start:d.RefSpan.End] != w.version {
			t.Errorf("dep %d: %+v, want %+v", i, d, w)
		}
	}
	for _, src := range []string{"", "\n\n", "# only comments\n  # indented\n"} {
		if ds, err := New().Extract("Mintfile", []byte(src), manager.Env{}); err != nil || len(ds) != 0 {
			t.Errorf("%q: %+v, %v", src, ds, err)
		}
	}
}
