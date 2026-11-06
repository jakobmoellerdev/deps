package fvm

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
		".fvmrc": true, "app/.fvmrc": true, ".fvm/fvm_config.json": true, "bindings/flutter/.fvm/fvm_config.json": true,
		"fvm_config.json": false, ".fvm/flutter_sdk": false, "x.fvmrc": false, "a/fvm/fvm_config.json": false,
	} {
		if m.Match(p) != want {
			t.Errorf("Match(%q) = %v", p, !want)
		}
	}
}

// TestExtractReal reads the FVM configs of localsend/localsend, fzyzcjy/flutter_rust_bridge,
// FriesI23/mhabit and RunanywhereAI/runanywhere-sdks (fetched 2026-11-06).
func TestExtractReal(t *testing.T) {
	for file, want := range map[string]string{
		"localsend/.fvmrc":                 "3.41.9",
		"flutter_rust_bridge/.fvmrc":       "3.41.2",
		"mhabit/.fvm/fvm_config.json":      "3.44.9",
		"runanywhere/.fvm/fvm_config.json": "3.44.6",
		"runanywhere/.fvmrc":               "3.44.6",
	} {
		src, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		ds, err := New().Extract(file, src, manager.Env{})
		if err != nil || len(ds) != 1 {
			t.Fatalf("%s: %+v, %v", file, ds, err)
		}
		d := ds[0]
		if d.Name != DepName || d.Version != want || d.Skip != "" || d.Datasource != "flutter-version" || d.DepType != DepType || d.Line != 2 || string(src[d.RefSpan.Start:d.RefSpan.End]) != want {
			t.Errorf("%s: %+v", file, d)
		}
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: "3.47.9"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply(src, edits)
		if err != nil {
			t.Fatal(err)
		}
		if ds, _ := New().Extract(file, out, manager.Env{}); len(ds) != 1 || ds[0].Version != "3.47.9" || len(out) != len(src) {
			t.Errorf("%s: after edit %s", file, out)
		}
	}
}

func TestExtract(t *testing.T) {
	for src, want := range map[string]struct{ version, skip string }{
		`{"flutterSdkVersion": "3.19.0", "flutter": "3.24.3"}`: {"3.24.3", ""},
		`{"flutter": {"x": 1}, "flutterSdkVersion": "3.19.0"}`: {"3.19.0", ""},
		`{"flutter": "stable"}`:                                {"stable", skipChannel},
		`{"flutter": "3.24.3@beta"}`:                           {"3.24.3@beta", skipUnsupported},
		`{"flutter": "3.26.0-0.1.pre"}`:                        {"3.26.0-0.1.pre", ""},
		`{"flutter": "3.24.\u0033"}`:                           {"3.24.3", skipEscaped},
	} {
		ds, err := New().Extract(".fvmrc", []byte(src), manager.Env{})
		if err != nil || len(ds) != 1 || ds[0].Version != want.version || ds[0].Skip != want.skip {
			t.Errorf("%s: %+v, %v", src, ds, err)
		}
	}
	for _, src := range []string{`{}`, `{"flavors": {"a": "3.0.0"}}`, `{"flutter": 3}`} {
		if ds, err := New().Extract(".fvmrc", []byte(src), manager.Env{}); err != nil || len(ds) != 0 {
			t.Errorf("%s: %+v, %v", src, ds, err)
		}
	}
	for _, src := range []string{``, `[]`, `{"flutter": "3.0.0"`, `{"flutter": `, `{"a": [}`} {
		if _, err := New().Extract(".fvmrc", []byte(src), manager.Env{}); err == nil {
			t.Errorf("%q: no error", src)
		}
	}
	if New().Name() != Name {
		t.Error("Name")
	}
}
