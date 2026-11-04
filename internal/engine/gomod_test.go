package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// fakeModules is a go datasource with heads (HeadSource), successor major paths
// (MajorPathSource) and release times by "path@version" (missing = unknown).
type fakeModules struct {
	rels    map[string][]string
	heads   map[string]datasource.Revision
	majors  map[string][]datasource.ModulePath
	times   map[string]time.Time
	headErr error
	majErr  error
	probes  atomic.Int32
}

func (f *fakeModules) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	var out []Release
	for _, v := range f.rels[d.Name] {
		out = append(out, Release{Version: v})
	}
	return out, nil
}

func (*fakeModules) Digest(context.Context, dep.Dep, string) (string, error) {
	return "", errors.New("no digests")
}

func (f *fakeModules) ReleaseTime(_ context.Context, d dep.Dep, v string) (time.Time, error) {
	return f.times[d.Name+"@"+v], nil
}

func (f *fakeModules) Head(_ context.Context, d dep.Dep, _ string) (datasource.Revision, error) {
	return f.heads[d.Name], f.headErr
}

func (f *fakeModules) MajorPaths(_ context.Context, d dep.Dep, _ string) ([]datasource.ModulePath, error) {
	f.probes.Add(1)
	return f.majors[d.Name], f.majErr
}

func resolveModules(t *testing.T, cfgYAML string, files map[string]string, ds *fakeModules) (string, *Result) {
	t.Helper()
	root, cfg := setup(t, cfgYAML, files)
	res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: managers, Datasources: map[string]Datasource{"go": ds}, Now: ageNow})
	if err != nil {
		t.Fatal(err)
	}
	return root, res
}

type wantUpdate struct {
	typ                      dep.UpdateType
	name, version, digest, b string
}

func gotUpdates(res *Result) []wantUpdate {
	var out []wantUpdate
	for _, u := range res.Updates {
		out = append(out, wantUpdate{u.Type, u.NewName, u.NewVersion, u.NewDigest, u.Bucket})
	}
	return out
}

func TestPseudoVersionUpdates(t *testing.T) {
	const (
		cur  = "v0.0.0-20240101000000-aaaaaaaaaaaa"
		head = "v0.0.0-20261020000000-bbbbbbbbbbbb"
		hash = "bbbbbbbbbbbb0000000000000000000000000000"
	)
	headRev := datasource.Revision{Version: head, Digest: hash, Time: daysAgo(14)}
	tests := []struct {
		name, cfg, version string
		rels               []string
		head               datasource.Revision
		want               []wantUpdate
		skip, pending      string
	}{
		{name: "newer head", version: cur, head: headRev, want: []wantUpdate{{typ: dep.Digest, version: head, digest: hash}}},
		{name: "head not newer", version: head, head: headRev},
		{name: "head is a release", version: cur, head: datasource.Revision{Version: "v0.1.0"}},
		{name: "head of another major", version: cur, head: datasource.Revision{Version: "v1.0.0-20261020000000-bbbbbbbbbbbb"}},
		{name: "newer release wins", version: cur, rels: []string{"v0.1.0", "v0.2.0-rc.1"}, head: headRev, want: []wantUpdate{{typ: dep.Minor, version: "v0.1.0"}}},
		{
			name: "release above the base", version: "v1.2.4-0.20240101000000-aaaaaaaaaaaa", rels: []string{"v1.2.3", "v1.2.4", "v1.3.0-rc.1"}, head: headRev,
			want: []wantUpdate{{typ: dep.Patch, version: "v1.2.4"}},
		},
		{
			name: "prerelease base takes prereleases", version: "v1.3.0-rc.1.0.20240101000000-aaaaaaaaaaaa", rels: []string{"v1.3.0-rc.1", "v1.3.0-rc.2"},
			want: []wantUpdate{{typ: dep.Patch, version: "v1.3.0-rc.2"}},
		},
		{name: "digest updates off", cfg: "rules: [{updates: [major, minor, patch]}]\n", version: cur, head: headRev},
		{name: "ignored", cfg: "rules: [{names: [example.com/p], ignore: true}]\n", version: cur, head: headRev, skip: "ignored by rule #0"},
		{name: "too new", cfg: "minimumReleaseAge: 30 days\n", version: cur, head: headRev, pending: tooNew(14, "30 days")},
		{
			name: "digest age off", cfg: "minimumReleaseAge: 30 days\nrules: [{updateTypes: [digest], minimumReleaseAge: 0 days}]\n", version: cur, head: headRev,
			want: []wantUpdate{{typ: dep.Digest, version: head, digest: hash}},
		},
		{name: "unknown time", cfg: "minimumReleaseAge: 30 days\n", version: cur, head: datasource.Revision{Version: head, Digest: hash}, want: []wantUpdate{{typ: dep.Digest, version: head, digest: hash}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := "module example.com/x\n\nrequire example.com/p " + tt.version + "\n"
			ds := &fakeModules{rels: map[string][]string{"example.com/p": tt.rels}, heads: map[string]datasource.Revision{"example.com/p": tt.head}}
			root, res := resolveModules(t, tt.cfg, map[string]string{"go.mod": mod}, ds)
			if len(res.Errors) > 0 {
				t.Fatal(res.Errors)
			}
			if got := gotUpdates(res); !slices.Equal(got, tt.want) {
				t.Errorf("updates = %+v, want %+v", got, tt.want)
			}
			var skips []string
			for _, d := range res.Skipped {
				skips = append(skips, d.Skip)
			}
			if (tt.skip == "") != (len(skips) == 0) || tt.skip != "" && skips[0] != tt.skip {
				t.Errorf("skipped = %q, want %q", skips, tt.skip)
			}
			if tt.pending != "" && (len(res.Pending) != 1 || res.Pending[0].Version != head || res.Pending[0].Reason != tt.pending) {
				t.Errorf("pending = %+v, want %q", res.Pending, tt.pending)
			}
			if len(tt.want) == 0 || tt.want[0].typ != dep.Digest {
				return
			}
			u := res.Updates[0]
			if from, to := u.FromTo(); from != cur || to != head {
				t.Errorf("FromTo = %s -> %s", from, to)
			}
			changes, errs := Build(t.Context(), root, managers, res.Updates, nil)
			if len(errs) > 0 || len(changes) != 1 || string(changes[0].New) != strings.Replace(mod, cur, head, 1) {
				t.Errorf("changes = %+v, errors %v", changes, errs)
			}
			if g := GroupUpdates(res.Updates, ""); len(g) != 1 || g[0].Title != "fix(deps): update example.com/p digest to bbbbbbb" {
				t.Errorf("groups = %+v", g)
			}
		})
	}

	ds := &fakeModules{headErr: errors.New("boom")}
	_, res := resolveModules(t, "", map[string]string{"go.mod": "module example.com/x\n\nrequire example.com/p " + cur + "\n"}, ds)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "example.com/p: latest commit: boom") {
		t.Errorf("errors = %v", res.Errors)
	}
}

func TestMajorPathUpdates(t *testing.T) {
	const name = "example.com/m/v2"
	majors := []datasource.ModulePath{
		{Name: "example.com/m/v3", Releases: []Release{{Version: "v3.0.0"}, {Version: "v3.1.0-rc.1"}, {Version: "bogus"}}},
		{Name: "example.com/m/v4", Releases: []Release{{Version: "v4.0.0"}}},
	}
	minor := wantUpdate{typ: dep.Minor, version: "v2.2.0"}
	v3 := wantUpdate{typ: dep.Major, name: "example.com/m/v3", version: "v3.0.0"}
	v4 := wantUpdate{typ: dep.Major, name: "example.com/m/v4", version: "v4.0.0"}
	tests := []struct {
		name, cfg  string
		times      map[string]time.Time
		want       []wantUpdate
		wantProbes int32
	}{
		{name: "newest major path", want: []wantUpdate{minor, withBucket(v4, "major")}, wantProbes: 1},
		{name: "one per major", cfg: "separateMultipleMajor: true\n", want: []wantUpdate{minor, withBucket(v3, "v3"), withBucket(v4, "v4")}, wantProbes: 1},
		{name: "single bucket", cfg: "separateMajorMinor: false\n", want: []wantUpdate{v4}, wantProbes: 1},
		{name: "switched off", cfg: "gomodUpdateImportPaths: false\n", want: []wantUpdate{minor}},
		{name: "majors not allowed", cfg: "rules: [{updates: [minor, patch]}]\n", want: []wantUpdate{minor}},
		{name: "majors ignored", cfg: "rules: [{updateTypes: [major], ignore: true}]\n", want: []wantUpdate{minor}},
		{name: "allow", cfg: "rules: [{allow: '< 4'}]\n", want: []wantUpdate{minor, withBucket(v3, "major")}, wantProbes: 1},
		{
			name: "too new", cfg: "minimumReleaseAge: 7 days\n",
			times: map[string]time.Time{"example.com/m/v4@v4.0.0": daysAgo(2), "example.com/m/v3@v3.0.0": daysAgo(20)},
			want:  []wantUpdate{minor, withBucket(v3, "major")}, wantProbes: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := &fakeModules{rels: map[string][]string{name: {"v2.1.0", "v2.2.0"}}, majors: map[string][]datasource.ModulePath{name: majors}, times: tt.times}
			_, res := resolveModules(t, tt.cfg, map[string]string{"go.mod": "module example.com/x\n\nrequire " + name + " v2.1.0\n"}, ds)
			if len(res.Errors) > 0 {
				t.Fatal(res.Errors)
			}
			if got := gotUpdates(res); !slices.Equal(got, tt.want) {
				t.Errorf("updates = %+v, want %+v", got, tt.want)
			}
			if got := ds.probes.Load(); got != tt.wantProbes {
				t.Errorf("probes = %d, want %d", got, tt.wantProbes)
			}
			if tt.name == "too new" && (len(res.Pending) != 1 || res.Pending[0].Version != "example.com/m/v4@v4.0.0") {
				t.Errorf("pending = %+v", res.Pending)
			}
		})
	}

	ds := &fakeModules{majErr: errors.New("boom")}
	_, res := resolveModules(t, "", map[string]string{"go.mod": "module example.com/x\n\nrequire " + name + " v2.1.0\n"}, ds)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "major module paths: boom") {
		t.Errorf("errors = %v", res.Errors)
	}
}

func withBucket(u wantUpdate, b string) wantUpdate {
	u.b = b
	return u
}

// TestMajorPathSecurityKept: a security update in the single bucket is not replaced by a
// major module path update.
func TestMajorPathSecurityKept(t *testing.T) {
	const name = "example.com/m/v2"
	ds := &fakeModules{
		rels:   map[string][]string{name: {"v2.1.0", "v2.1.1", "v2.2.0"}},
		majors: map[string][]datasource.ModulePath{name: {{Name: "example.com/m/v3", Releases: []Release{{Version: "v3.0.0"}}}}},
	}
	root, cfg := setup(t, "separateMajorMinor: false\nrules: [{updates: [major]}]\n", map[string]string{"go.mod": "module example.com/x\n\nrequire " + name + " v2.1.0\n"})
	sec := &fakeSec{vulns: map[string][]Vuln{name: {{Advisory: dep.Advisory{ID: "GO-1"}, Fixed: []string{"v2.1.1"}}}}}
	res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: managers, Datasources: map[string]Datasource{"go": ds}, Security: sec, Now: ageNow})
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v2.1.1" || res.Updates[0].NewName != "" || len(res.Updates[0].Advisories) != 1 {
		t.Errorf("updates = %+v", res.Updates)
	}
}

func TestBuildMajorPathRewritesImports(t *testing.T) {
	const (
		mod = "module example.com/x\n\nrequire example.com/m/v2 v2.1.0\n"
		src = "package x\n\nimport \"example.com/m/v2/pkg\"\n\nvar _ = pkg.X\n"
	)
	files := map[string]string{"go.mod": mod, "a.go": src, "sub/b.go": "package sub\n", "go.sum": "old\n"}
	ds := &fakeModules{
		rels:   map[string][]string{"example.com/m/v2": {"v2.1.0"}},
		majors: map[string][]datasource.ModulePath{"example.com/m/v2": {{Name: "example.com/m/v3", Releases: []Release{{Version: "v3.0.0"}}}}},
	}
	root, res := resolveModules(t, "", files, ds)
	if len(res.Errors) > 0 || len(res.Updates) != 1 {
		t.Fatalf("updates %+v, errors %v", res.Updates, res.Errors)
	}
	if from, to := res.Updates[0].FromTo(); from != "v2.1.0" || to != "example.com/m/v3@v3.0.0" {
		t.Errorf("FromTo = %s -> %s", from, to)
	}
	var seen map[string][]byte
	post := map[string]PostProcessor{"gomod": postFunc{
		siblings: []string{"go.sum"},
		scope:    func(string, string) (string, bool) { return ".", true },
		f: func(_ context.Context, _ string, manifests, _ map[string][]byte) (map[string][]byte, error) {
			seen = manifests
			return map[string][]byte{"go.mod": append(manifests["go.mod"], "// tidied\n"...), "go.sum": []byte("new\n")}, nil
		},
	}}
	changes, errs := Build(t.Context(), root, managers, res.Updates, post)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	want := map[string]string{
		"a.go":   strings.Replace(src, "m/v2/pkg", "m/v3/pkg", 1),
		"go.mod": strings.Replace(mod, "m/v2 v2.1.0", "m/v3 v3.0.0", 1) + "// tidied\n",
		"go.sum": "new\n",
	}
	if got := paths(changes); !slices.Equal(got, []string{"a.go", "go.mod", "go.sum"}) {
		t.Fatalf("changes = %v", got)
	}
	for _, c := range changes {
		if string(c.New) != want[c.Path] {
			t.Errorf("%s = %q, want %q", c.Path, c.New, want[c.Path])
		}
	}
	if string(seen["a.go"]) != want["a.go"] || len(seen) != 2 {
		t.Errorf("post-processor manifests = %q", seen)
	}

	// A failing scope drops the go.mod and the rewritten source together.
	post["gomod"] = postFunc{siblings: []string{"go.sum"}, scope: func(string, string) (string, bool) { return ".", true }, f: func(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
		return nil, errors.New("tidy failed")
	}}
	if changes, errs := Build(t.Context(), root, managers, res.Updates, post); len(changes) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "a.go, go.mod: tidy failed") {
		t.Errorf("changes = %v, errors = %v", paths(changes), errs)
	}

	// The update's go.mod failing drops the rewritten source too.
	u := res.Updates[0]
	u.Dep.Version = "v9.9.9"
	if changes, errs := Build(t.Context(), root, managers, []dep.Update{u}, nil); len(changes) != 0 || len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), "go.mod: ") {
		t.Errorf("changes = %v, errors = %v", paths(changes), errs)
	}

	// A source that does not parse fails the update.
	writeFile(t, root, "bad.go", "package\nimport \"example.com/m/v2\"\n")
	if changes, errs := Build(t.Context(), root, managers, res.Updates, nil); len(changes) != 0 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "go.mod: example.com/m/v2: bad.go") {
		t.Errorf("changes = %v, errors = %v", paths(changes), errs)
	}
}
