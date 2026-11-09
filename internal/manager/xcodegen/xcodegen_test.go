package xcodegen

import (
	"os"
	"path/filepath"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/swift"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"project.yml": true, "apps/ios/project.yml": true, "project.yaml": false, "myproject.yml": false, "project.json": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
}

type want struct {
	name, depType, version, constraint, skip string
	line                                     int
}

// check compares ds with wants: names, dep types, versions (also as found at RefSpan),
// constraints (a range dep's Range is its version), skips and lines.
func check(t *testing.T, file string, src []byte, ds []dep.Dep, wants []want) {
	t.Helper()
	if len(ds) != len(wants) {
		t.Fatalf("%s: %d deps, want %d: %+v", file, len(ds), len(wants), ds)
	}
	for i, w := range wants {
		d := ds[i]
		rng := ""
		if w.constraint != "" {
			rng = w.version
		}
		if d.Name != w.name || d.DepType != w.depType || d.Version != w.version || d.Constraint != w.constraint || d.Range != rng ||
			d.Skip != w.skip || d.Line != w.line || string(src[d.RefSpan.Start:d.RefSpan.End]) != w.version ||
			d.Manager != Name || d.Datasource != "git-tags" || d.Versioning != "semver" || d.ExtractVersion != swift.ExtractVersion {
			t.Errorf("%s dep %d: %+v, want %+v", file, i, d, w)
		}
		if w.skip == "" && d.Source != "https://github.com/"+w.name {
			t.Errorf("%s dep %d: source %q", file, i, d.Source)
		}
	}
}

// TestExtractReal reads the project specs of element-hq/element-ios, instructure/canvas-ios
// (Core) and openclaw/openclaw (apps/ios), fetched 2026-11-09.
func TestExtractReal(t *testing.T) {
	for file, wants := range map[string][]want{
		"element-ios.project.yml": {
			{"matrix-org/matrix-analytics-events", DepTypeExactVersion, "0.23.1", "", "", 52},
			{"maplibre/maplibre-gl-native-distribution", "", "", "", skipRange, 53},
			{"apple/swift-collections", "", "", "", skipRange, 57},
			{"element-hq/swift-ogg", "", "", "", skipBranch, 61},
			{"element-hq/matrix-rich-text-editor-swift", DepTypeExactVersion, "2.37.12", "", "", 66},
			{"devicekit/DeviceKit", DepTypeMajorVersion, "4.7.0", ">=4.7.0 <5.0.0", "", 69},
			{"Cocoanetics/DTCoreText", DepTypeVersion, "1.6.26", "", "", 72},
			{"PostHog/posthog-ios", DepTypeMinorVersion, "3.2.5", ">=3.2.5 <3.3.0", "", 75},
		},
		"canvas-core.project.yml": {
			{"pointfreeco/combine-schedulers", DepTypeExactVersion, "1.1.0", "", "", 0},
			{"CombineCommunity/CombineExt", DepTypeExactVersion, "1.9.0", "", "", 0},
			{"airbnb/lottie-ios", DepTypeExactVersion, "4.6.0", "", "", 0},
			{"pendo-io/pendo-mobile-sdk", DepTypeExactVersion, "3.12.0", "", "", 0},
			{"PSPDFKit/PSPDFKit-SP", DepTypeExactVersion, "26.6.0", "", "", 0},
			{"SDWebImage/SDWebImageSwiftUI", DepTypeVersion, "3.1.4", "", "", 0},
		},
		"openclaw.project.yml": {
			{"ESPProvision", "", "", "", skipLocal, 0},
			{"OpenClawKit", "", "", "", skipLocal, 0},
			{"Swabble", "", "", "", skipLocal, 0},
			{"stasel/WebRTC", DepTypeExactVersion, "154.0.0", "", "", 0},
			{"jedisct1/swift-sodium", "", "", "", skipRevision, 0},
		},
	} {
		src, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		ds, err := New().Extract(file, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		for i := range wants {
			if wants[i].line == 0 && i < len(ds) {
				wants[i].line = ds[i].Line // lines are checked for element-ios
			}
		}
		check(t, file, src, ds, wants)
		for i, d := range ds {
			if d.Skip != "" {
				continue
			}
			edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "10.0.0"})
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply(src, edits)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := New().Extract(file, out, manager.Env{}); got[i].Version != "10.0.0" || got[i].DepType != d.DepType || len(out) != len(src)+len("10.0.0")-len(d.Version) {
				t.Errorf("%s dep %d: after edit %+v", file, i, got[i])
			}
		}
	}
}

func TestExtract(t *testing.T) {
	src := []byte(`name: App
packages:
  Both:
    github: owner/both
    url: https://example.com/other/repo
    exactVersion: "1.0.0"
  Quoted:
    url: git@github.com:owner/quoted.git
    from: '0.3.0'
  Order:
    url: https://github.com/owner/order
    from: 1.0.0
    exactVersion: 2.0.0 # exact wins, as in XcodeGen
  MinOnly:
    url: https://github.com/owner/minonly
    minVersion: 1.0.0
    majorVersion: 1.2.0
  Number:
    url: https://github.com/owner/number
    exactVersion: 1.2
  Partial:
    url: https://github.com/owner/partial
    from: "1"
  NoReq:
    url: https://github.com/owner/noreq
  NoURL:
    exactVersion: 1.0.0
  BadURL:
    url: file:///tmp/pkg
    exactVersion: 1.0.0
  Folded:
    url: https://github.com/owner/folded
    exactVersion: >-
      1.0.0
  Escaped:
    url: https://github.com/owner/escaped
    exactVersion: "1.0.\x30"
  NotAMap: https://github.com/owner/notamap
targets: {}
`)
	ds, err := New().Extract("project.yml", src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	check(t, "project.yml", src, ds, []want{
		{"owner/both", DepTypeExactVersion, "1.0.0", "", "", 6},
		{"owner/quoted", DepTypeFrom, "0.3.0", ">=0.3.0 <1.0.0", "", 9},
		{"owner/order", DepTypeExactVersion, "2.0.0", "", "", 13},
		{"owner/minonly", DepTypeMajorVersion, "1.2.0", ">=1.2.0 <2.0.0", "", 17},
		{"owner/number", DepTypeExactVersion, "", "", skipRequirement, 20},
		{"owner/partial", DepTypeFrom, "", "", skipRequirement, 23},
		{"owner/noreq", "", "", "", skipRequirement, 24},
		{"NoURL", "", "", "", skipURL, 26},
		{"file:///tmp/pkg", "", "", "", skipURL, 28},
		{"owner/folded", DepTypeExactVersion, "", "", skipLocate, 33},
		{"owner/escaped", DepTypeExactVersion, "", "", skipLocate, 37},
	})
	for _, src := range []string{"", "name: App\n", "packages: [a, b]\n", "packages:\n"} {
		if ds, err := New().Extract("project.yml", []byte(src), manager.Env{}); err != nil || len(ds) != 0 {
			t.Errorf("%q: %+v, %v", src, ds, err)
		}
	}
	if _, err := New().Extract("project.yml", []byte("packages: [\n"), manager.Env{}); err == nil {
		t.Error("invalid YAML: no error")
	}
}
