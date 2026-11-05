package swift

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

const resolvedV1 = `{
  "object": {
    "pins": [
      {
        "package": "Alamofire",
        "repositoryURL": "https://github.com/Alamofire/Alamofire.git",
        "state": {
          "branch": null,
          "revision": "f96b619bcb2383b43d898402283924b80e2c4bae",
          "version": "5.4.3"
        }
      },
      {
        "package": "Tip",
        "repositoryURL": "git@github.com:o/tip.git",
        "state": {
          "branch": "main",
          "revision": "1111111111111111111111111111111111111111",
          "version": null
        }
      }
    ]
  },
  "version": 1
}
`

const resolvedV2 = `{
  "pins" : [
    {
      "identity" : "swift-log",
      "kind" : "remoteSourceControl",
      "location" : "https://github.com/apple/swift-log.git",
      "state" : {
        "revision" : "c9c6bb9d275ec0d95c3ad16391f3f045dc623104",
        "version" : "1.16.2"
      }
    },
    {
      "identity" : "kit",
      "kind" : "localSourceControl",
      "location" : "/Users/x/Kit",
      "state" : {
        "revision" : "2222222222222222222222222222222222222222"
      }
    }
  ],
  "version" : 2
}
`

func TestParseResolved(t *testing.T) {
	r, err := parseResolved([]byte(resolvedV1))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.pins) != 2 || r.originHash != "" {
		t.Fatalf("v1: %+v", r)
	}
	p := r.pins[0]
	if p.key != "github.com/alamofire/alamofire" || p.version != "5.4.3" || resolvedV1[p.versionSpan.Start:p.versionSpan.End] != "5.4.3" ||
		resolvedV1[p.revisionSpan.Start:p.revisionSpan.End] != p.revision {
		t.Errorf("v1 pin = %+v", p)
	}
	if r.pins[1].key != "github.com/o/tip" || r.pins[1].version != "" {
		t.Errorf("v1 branch pin = %+v", r.pins[1])
	}

	r, err = parseResolved([]byte(resolvedV2))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.pins) != 1 || r.pins[0].key != "github.com/apple/swift-log" || r.find("github.com/apple/swift-log") != 0 || r.find("github.com/x/y") != -1 {
		t.Errorf("v2 = %+v", r)
	}

	src, err := os.ReadFile(filepath.Join("testdata", "openclaw", "apps", "macos", "Package.resolved"))
	if err != nil {
		t.Fatal(err)
	}
	r, err = parseResolved(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.pins) != 16 || r.originHash != "ab22122cebe413fffd4d8cc21ae08f0be752b29c9c1e9c5d60dd234f5fc22061" ||
		string(src[r.originSpan.Start:r.originSpan.End]) != r.originHash {
		t.Errorf("v3: %d pins, origin %q", len(r.pins), r.originHash)
	}

	for _, bad := range []string{``, `[]`, `{"pins": {}}`, `{"pins": [`} {
		if _, err := parseResolved([]byte(bad)); err == nil {
			t.Errorf("parseResolved(%q): no error", bad)
		}
	}
}

func TestRepoKey(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Apple/Swift-Log.git":   "github.com/apple/swift-log",
		"https://github.com/apple/swift-log/":      "github.com/apple/swift-log",
		"git@github.com:apple/swift-log.git":       "github.com/apple/swift-log",
		"ssh://git@github.com/apple/swift-log":     "github.com/apple/swift-log",
		"https://token@github.com/apple/swift-log": "github.com/apple/swift-log",
		"http://127.0.0.1:8080/o/r":                "127.0.0.1:8080/o/r",
	} {
		if got := repoKey(in); got != want {
			t.Errorf("repoKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// extractAll extracts every Package.swift below top, as discovery would hand them to ReadLocks.
func extractAll(t *testing.T, top string) [][]dep.Dep {
	t.Helper()
	var files [][]dep.Dep
	err := filepath.WalkDir(top, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != manifestFile {
			return err
		}
		rel, _ := filepath.Rel(top, p)
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		deps, err := New().Extract(filepath.ToSlash(rel), src, manager.Env{})
		files = append(files, deps)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestReadLocksOpenClaw(t *testing.T) {
	top := filepath.Join("testdata", "openclaw")
	files := extractAll(t, top)
	if errs := New().(manager.LockReader).ReadLocks(top, files); len(errs) > 0 {
		t.Fatal(errs)
	}
	got := map[string]string{}
	for _, f := range files {
		for _, d := range f {
			if d.Range != "" {
				got[d.Name] = d.Locked
			}
		}
	}
	want := map[string]string{"swiftlang/swift-subprocess": "1.0.2", "apple/swift-log": "1.16.2", "pointfreeco/swift-concurrency-extras": "1.4.3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Locked = %v, want %v", got, want)
	}
}

// tree writes files (slash path -> content) below a new temporary directory.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	top := t.TempDir()
	for p, s := range files {
		abs := filepath.Join(top, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return top
}

func pinJSON(location, version, revision string) string {
	return fmt.Sprintf(`    {
      "identity" : %q,
      "kind" : "remoteSourceControl",
      "location" : %q,
      "state" : {
        "revision" : %q,
        "version" : %q
      }
    }`, path.Base(location), location, revision, version)
}

func resolvedJSON(origin string, pins ...string) string {
	s := "{\n"
	if origin != "" {
		s += fmt.Sprintf("  \"originHash\" : %q,\n", origin)
	}
	return s + "  \"pins\" : [\n" + strings.Join(pins, ",\n") + "\n  ],\n  \"version\" : 3\n}\n"
}

func TestReadLocksLocalLibrary(t *testing.T) {
	top := tree(t, map[string]string{
		"app/Package.swift":      `let package = Package(dependencies: [.package(path: "../Lib"), .package(url: "https://github.com/o/b", from: "1.0.0")])`,
		"app/Package.resolved":   resolvedJSON("", pinJSON("https://github.com/o/a.git", "1.4.0", strings.Repeat("a", 40)), pinJSON("https://github.com/o/b", "2.1.0", strings.Repeat("b", 40))),
		"other/Package.swift":    `let package = Package(dependencies: [.package(path: "../Lib")])`,
		"other/Package.resolved": resolvedJSON("", pinJSON("https://github.com/O/A", "1.2.0", strings.Repeat("c", 40))),
		"Lib/Package.swift":      `let package = Package(dependencies: [.package(url: "https://github.com/o/a", from: "1.0.0")])`,
		"bad/Package.swift":      `let package = Package(dependencies: [.package(url: "https://github.com/o/a", from: "1.0.0")])`,
		"bad/Package.resolved":   `{"pins": [`,
	})
	files := extractAll(t, top)
	errs := New().(manager.LockReader).ReadLocks(top, files)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "bad/Package.resolved") {
		t.Errorf("errs = %v", errs)
	}
	got := map[string]string{}
	for _, f := range files {
		for _, d := range f {
			if d.Range != "" {
				got[d.File+" "+d.Name] = fmt.Sprintf("%s %v", d.Locked, d.LockUnread)
			}
		}
	}
	want := map[string]string{
		"Lib/Package.swift o/a": "1.2.0 false", // the lowest pin among app and other
		"app/Package.swift o/b": " false",      // 2.1.0 is outside from: "1.0.0"
		"bad/Package.swift o/a": " true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Locked = %v, want %v", got, want)
	}
}

// gitServer serves the v0 ref advertisement of the repositories in tags (URL path -> tag ->
// commit), as the git-tags datasource reads it.
func gitServer(t *testing.T, tags map[string]map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo, ok := strings.CutSuffix(r.URL.Path, "/info/refs")
		if !ok || r.Method != http.MethodGet || tags[repo] == nil {
			http.NotFound(w, r)
			return
		}
		pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
		out := pkt("# service=git-upload-pack\n") + "0000"
		first := true
		for name, commit := range tags[repo] {
			line := commit + " refs/tags/" + name
			if first {
				line += "\x00agent=git/2.40\n"
				first = false
			} else {
				line += "\n"
			}
			out += pkt(line)
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		fmt.Fprint(w, out+"0000")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLocker(t *testing.T) {
	srv := gitServer(t, map[string]map[string]string{
		"/o/a": {"1.0.0": strings.Repeat("1", 40), "v1.1.0": strings.Repeat("2", 40)},
		"/o/b": {"1.2.0": strings.Repeat("3", 40), "1.3.0": strings.Repeat("4", 40)},
		"/o/c": {"2.0.0": strings.Repeat("5", 40), "2.1.0": strings.Repeat("6", 40)},
	})
	url := func(p string) string { return srv.URL + p }
	appManifest := `// swift-tools-version: 6.0
import PackageDescription
let package = Package(
    name: "App",
    dependencies: [
        .package(url: "` + url("/o/a") + `.git", exact: "1.0.0"),
        .package(url: "` + url("/o/b") + `", from: "1.0.0"),
        .package(path: "../Lib"),
    ]
)
`
	libManifest := `let package = Package(dependencies: [.package(url: "` + url("/o/c") + `", exact: "2.0.0")])`
	appResolved := resolvedJSON(sha256Hex([]byte(appManifest)),
		pinJSON(url("/o/a.git"), "1.0.0", strings.Repeat("1", 40)),
		pinJSON(url("/o/b"), "1.2.0", strings.Repeat("3", 40)),
		pinJSON(url("/o/c"), "2.0.0", strings.Repeat("5", 40)))
	otherResolved := resolvedJSON("", pinJSON(url("/o/c"), "2.0.0", strings.Repeat("5", 40)))
	top := tree(t, map[string]string{
		"app/Package.swift":                   appManifest,
		"app/Package.resolved":                appResolved,
		"Lib/Package.swift":                   libManifest,
		"other/Package.swift":                 `let package = Package(dependencies: [.package(url: "` + url("/o/c") + `", exact: "2.0.0")])`,
		"other/Package.resolved":              otherResolved,
		"loose/Package.swift":                 `let package = Package()`,
		".build/checkouts/x/Package.swift":    `let package = Package()`,
		".build/checkouts/x/Package.resolved": `{}`,
	})
	ctx := context.Background()
	l := NewLocker(gittags.New(srv.Client(), nil, make(chan struct{}, 4), nil))

	for manifest, want := range map[string]bool{"app/Package.swift": true, "Lib/Package.swift": true, "other/Package.swift": true, "loose/Package.swift": false} {
		if dir, ok := l.Scope(ctx, top, manifest); ok != want || ok && dir != "." {
			t.Errorf("Scope(%s) = %q, %v", manifest, dir, ok)
		}
	}
	if got, want := l.ScopeSiblings(top), []string{"app/Package.resolved", "other/Package.resolved"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ScopeSiblings = %v, want %v", got, want)
	}
	if got, _, _ := l.ScopeInputs(ctx, top, ".", []string{"Lib/Package.swift"}); !reflect.DeepEqual(got, []string{"Lib/Package.swift", "app/Package.swift"}) {
		t.Errorf("ScopeInputs = %v", got)
	}

	files := extractAll(t, top)
	byName := map[string]dep.Dep{}
	for _, f := range files {
		for _, d := range f {
			byName[d.File+" "+d.Name] = d
		}
	}
	a, b, c := byName["app/Package.swift o/a"], byName["app/Package.swift o/b"], byName["Lib/Package.swift o/c"]
	b.Locked = "1.2.0"
	ua := dep.Update{Dep: a, NewVersion: "1.1.0"}
	edits, err := New().Edits(ua)
	if err != nil {
		t.Fatal(err)
	}
	patchedApp := strings.Replace(appManifest, `exact: "1.0.0"`, `exact: "1.1.0"`, 1)
	if len(edits) != 1 || appManifest[:edits[0].Start]+edits[0].New+appManifest[edits[0].End:] != patchedApp {
		t.Fatalf("edits = %+v", edits)
	}
	manifests := map[string][]byte{"app/Package.swift": []byte(patchedApp), "Lib/Package.swift": []byte(libManifest)}
	sibs := map[string][]byte{"app/Package.resolved": []byte(appResolved), "other/Package.resolved": []byte(otherResolved)}
	ups := map[string][]dep.Update{"app/Package.swift": {ua}, "Lib/Package.swift": {{Dep: c, NewVersion: "2.1.0"}}}
	locks := map[string][]dep.Update{"app/Package.swift": {{Dep: b, NewVersion: "1.3.0", Lockfile: true}}}
	out, err := l.ProcessUpdates(ctx, top, manifests, sibs, ups, locks)
	if err != nil {
		t.Fatal(err)
	}
	want := resolvedJSON(sha256Hex([]byte(patchedApp)),
		pinJSON(url("/o/a.git"), "1.1.0", strings.Repeat("2", 40)),
		pinJSON(url("/o/b"), "1.3.0", strings.Repeat("4", 40)),
		pinJSON(url("/o/c"), "2.1.0", strings.Repeat("6", 40)))
	if len(out) != 1 || string(out["app/Package.resolved"]) != want {
		t.Errorf("ProcessUpdates = %v\n%s\nwant\n%s", len(out), out["app/Package.resolved"], want)
	}

	// A pin at or above the version the new requirement admits stays; an unchanged root
	// manifest keeps its origin hash; an unknown tag fails.
	ub := dep.Update{Dep: b, NewVersion: "1.1.0", Lockfile: true}
	out, err = l.ProcessUpdates(ctx, top, map[string][]byte{"app/Package.swift": []byte(appManifest)}, sibs, nil, map[string][]dep.Update{"app/Package.swift": {ub}})
	if err != nil || len(out) != 0 {
		t.Errorf("no-downgrade: %v, %v", out, err)
	}
	_, err = l.ProcessUpdates(ctx, top, manifests, sibs, map[string][]dep.Update{"Lib/Package.swift": {{Dep: c, NewVersion: "2.2.0"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "app/Package.resolved: commit of o/c 2.2.0") {
		t.Errorf("unknown tag: %v", err)
	}

	// A stale origin hash (it does not match the manifest on disk) is left alone.
	stale := strings.Replace(appResolved, sha256Hex([]byte(appManifest)), strings.Repeat("0", 64), 1)
	out, err = l.ProcessUpdates(ctx, top, manifests, map[string][]byte{"app/Package.resolved": []byte(stale)}, map[string][]dep.Update{"app/Package.swift": {ua}}, nil)
	if err != nil || !strings.Contains(string(out["app/Package.resolved"]), `"originHash" : "`+strings.Repeat("0", 64)) || !strings.Contains(string(out["app/Package.resolved"]), `"version" : "1.1.0"`) {
		t.Errorf("stale origin: %s, %v", out["app/Package.resolved"], err)
	}
}

func TestLockerV1(t *testing.T) {
	srv := gitServer(t, map[string]map[string]string{"/Alamofire/Alamofire": {"5.5.0": strings.Repeat("7", 40)}})
	src := strings.ReplaceAll(resolvedV1, "https://github.com/Alamofire/Alamofire.git", srv.URL+"/Alamofire/Alamofire.git")
	manifest := `let package = Package(dependencies: [.package(url: "` + srv.URL + `/Alamofire/Alamofire.git", from: "5.4.0")])`
	top := tree(t, map[string]string{"Package.swift": manifest, "Package.resolved": src})
	deps, err := New().Extract("Package.swift", []byte(manifest), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	l := NewLocker(gittags.New(srv.Client(), nil, make(chan struct{}, 1), nil))
	out, err := l.ProcessUpdates(context.Background(), top, map[string][]byte{"Package.swift": []byte(manifest)}, map[string][]byte{"Package.resolved": []byte(src)},
		nil, map[string][]dep.Update{"Package.swift": {{Dep: deps[0], NewVersion: "5.5.0", Lockfile: true}}})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(src, `"5.4.3"`, `"5.5.0"`, 1), "f96b619bcb2383b43d898402283924b80e2c4bae", strings.Repeat("7", 40), 1)
	if string(out["Package.resolved"]) != want {
		t.Errorf("v1 = %s", out["Package.resolved"])
	}
}
