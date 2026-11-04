package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradle"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
)

const rangePkg = `{
  "dependencies": {"a": "^1.0.0", "b": "~2.0.0"},
  "devDependencies": {"c": "1.0.0"},
  "peerDependencies": {"p": "^1.0.0"}
}
`

const rangeLock = `{
  "lockfileVersion": 3,
  "packages": {
    "": {"dependencies": {"a": "^1.0.0", "b": "~2.0.0"}},
    "node_modules/a": {"version": "1.1.0"},
    "node_modules/b": {"version": "2.0.1"},
    "node_modules/c": {"version": "1.0.0"},
    "node_modules/p": {"version": "1.0.0"}
  }
}
`

var rangeReleases = fakeGo{
	"a": {"1.0.0", "1.1.0", "1.2.0", "2.0.0"},
	"b": {"2.0.0", "2.0.1", "2.1.0"},
	"c": {"1.0.0", "1.0.1"},
	"p": {"1.0.0", "1.1.0", "2.0.0"},
}

// peersOn opts peerDependencies in (they are ignored by default).
const peersOn = "rules:\n  - depTypes: [peerDependencies]\n    ignore: false\n"

func renderUpdates(ups []dep.Update) string {
	var got []string
	for _, u := range ups {
		from, to := u.FromTo()
		s := fmt.Sprintf("%s %s->%s %s", u.Dep.Name, from, to, u.Type)
		if u.Lockfile {
			s += " lockfile"
		}
		if len(u.Advisories) > 0 {
			s += " security"
		}
		got = append(got, s)
	}
	return strings.Join(got, "\n")
}

func TestRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		files     map[string]string // besides web/package.json
		want      []string
	}{
		{
			name: "auto with package-lock.json", cfg: peersOn,
			files: map[string]string{"web/package-lock.json": rangeLock},
			want: []string{
				"a 1.1.0->1.2.0 minor lockfile", // in range: only the lock moves, from the locked version
				"a 1.0.0->2.0.0 major",          // outside: the range is replaced
				"b 2.0.0->2.1.0 minor",
				"c 1.0.0->1.0.1 patch", // an exact version admits nothing newer
				"p 1.0.0->2.0.0 major", // peers: in-range versions are left alone
			},
		},
		{
			name: "update-lockfile without a lockfile", cfg: "rangeStrategy: update-lockfile\n" + peersOn,
			want: []string{"a 1.0.0->2.0.0 major", "b 2.0.0->2.1.0 minor", "c 1.0.0->1.0.1 patch", "p 1.0.0->2.0.0 major"},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n" + peersOn,
			files: map[string]string{"web/package-lock.json": rangeLock},
			want: []string{
				"a 1.0.0->1.2.0 minor", "a 1.0.0->2.0.0 major", "b 2.0.0->2.1.0 minor", "c 1.0.0->1.0.1 patch",
				"p 1.0.0->1.1.0 minor", "p 1.0.0->2.0.0 major",
			},
		},
		{
			name: "rule overrides", cfg: peersOn + "  - names: [a]\n    rangeStrategy: bump\n",
			files: map[string]string{"web/package-lock.json": rangeLock},
			want:  []string{"a 1.0.0->1.2.0 minor", "a 1.0.0->2.0.0 major", "b 2.0.0->2.1.0 minor", "c 1.0.0->1.0.1 patch", "p 1.0.0->2.0.0 major"},
		},
		{
			name: "pnpm lockfile is not read: bump", cfg: peersOn,
			files: map[string]string{"web/pnpm-lock.yaml": "lockfileVersion: '9.0'\n"},
			want: []string{
				"a 1.0.0->1.2.0 minor", "a 1.0.0->2.0.0 major", "b 2.0.0->2.1.0 minor", "c 1.0.0->1.0.1 patch",
				"p 1.0.0->2.0.0 major", // peers still keep in-range versions
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"web/package.json": rangePkg}
			maps.Copy(files, tt.files)
			root, cfg := setup(t, tt.cfg, files)
			o := Options{Managers: []manager.Manager{npm.New()}, Datasources: map[string]Datasource{"npm": rangeReleases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			if got := renderUpdates(res.Updates); got != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", got, strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestRangeStrategyTrace(t *testing.T) {
	root, cfg := setup(t, "", map[string]string{"web/package.json": `{"dependencies": {"a": "^1.0.0"}}`})
	var traces []Trace
	o := Options{Managers: []manager.Manager{npm.New()}, Datasources: map[string]Datasource{"npm": rangeReleases}, Trace: func(t Trace) { traces = append(traces, t) }}
	if _, err := Resolve(t.Context(), root, cfg, env, o); err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 || strings.Join(traces[0].Rejected["policy: admitted by range ^1.0.0 (no lockfile update)"], " ") != "1.1.0 1.2.0" {
		t.Errorf("rejected = %v", traces[0].Rejected)
	}
}

func TestRangeStrategyBrokenLockfile(t *testing.T) {
	root, cfg := setup(t, "", map[string]string{"web/package.json": rangePkg, "web/package-lock.json": "{"})
	o := Options{Managers: []manager.Manager{npm.New()}, Datasources: map[string]Datasource{"npm": rangeReleases}}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0].Error(), "web/package-lock.json: ") {
		t.Fatal(err, res.Errors)
	}
	// The lockfile was not read: the ranges are bumped.
	if got := renderUpdates(res.Updates); !strings.HasPrefix(got, "a 1.0.0->1.2.0 minor\n") {
		t.Errorf("updates =\n%s", got)
	}
}

// versionSec records the versions the advisories lookup was asked about.
type versionSec struct {
	vulns map[string][]Vuln
	mu    sync.Mutex
	asked []string
}

func (f *versionSec) Advisories(_ context.Context, deps []dep.Dep) ([][]Vuln, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]Vuln, len(deps))
	for i, d := range deps {
		f.asked = append(f.asked, d.Name+"@"+d.Version+" locked "+d.Locked)
		out[i] = f.vulns[d.Name]
	}
	return out, nil
}

func TestRangeStrategySecurity(t *testing.T) {
	pkg := `{"dependencies": {"a": "^1.0.0", "s": "^1.0.0", "x": "^1.0.0"}}`
	lock := `{"lockfileVersion": 3, "packages": {
  "node_modules/a": {"version": "1.1.0"}, "node_modules/s": {"version": "1.0.0"}, "node_modules/x": {"version": "1.0.0"}}}`
	releases := fakeGo{"a": {"1.0.0", "1.1.0"}, "s": {"1.0.0", "1.0.1", "1.0.2"}, "x": {"1.0.0", "1.0.1", "2.0.0"}}
	sec := &versionSec{vulns: map[string][]Vuln{
		"a": {vuln("GHSA-a", "1.1.0")}, // already locked at the fix
		"s": {vuln("GHSA-s", "1.0.1")}, // the range admits the fix: lockfile-only
		"x": {vuln("GHSA-x", "2.0.0")}, // the fix needs a new range
	}}
	// Only majors are allowed, so the security updates come from the advisories alone.
	root, cfg := setup(t, "rules:\n  - updates: [major]\n", map[string]string{"package.json": pkg, "package-lock.json": lock})
	o := Options{Managers: []manager.Manager{npm.New()}, Datasources: map[string]Datasource{"npm": releases}, Security: sec}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	want := "s 1.0.0->1.0.1 patch lockfile security\nx 1.0.0->2.0.0 major security"
	if got := renderUpdates(res.Updates); got != want {
		t.Errorf("updates =\n%s\nwant\n%s", got, want)
	}
	if got := strings.Join(sec.asked, ", "); got != "a@1.0.0 locked 1.1.0, s@1.0.0 locked 1.0.0, x@1.0.0 locked 1.0.0" {
		t.Errorf("asked = %s", got)
	}
}

// lockPost is an npm post-processor that applies lockfile-only updates.
type lockPost struct {
	postFunc
	locked func(dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error)
}

func (p lockPost) ProcessLocked(_ context.Context, dir string, manifests, files map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
	return p.locked(dir, manifests, files, locks)
}

// pinPost is a post-processor that implements UpdatePinner.
type pinPost struct {
	postFunc
	pinned func(ups, locks map[string][]dep.Update) (map[string][]byte, error)
}

func (p pinPost) ProcessUpdates(_ context.Context, _ string, _, _ map[string][]byte, ups, locks map[string][]dep.Update) (map[string][]byte, error) {
	return p.pinned(ups, locks)
}

func TestBuildLockfileOnly(t *testing.T) {
	root, cfg := setup(t, "", map[string]string{"web/package.json": rangePkg, "web/package-lock.json": rangeLock})
	o := Options{Managers: []manager.Manager{npm.New()}, Datasources: map[string]Datasource{"npm": rangeReleases}}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatal(err, res.Errors)
	}
	var lockOnly, bumped dep.Update
	for _, u := range res.Updates {
		switch {
		case u.Lockfile:
			lockOnly = u
		case u.Dep.Name == "b":
			bumped = u
		}
	}
	if lockOnly.Dep.Name != "a" || bumped.NewVersion != "2.1.0" {
		t.Fatalf("updates =\n%s", renderUpdates(res.Updates))
	}

	var gotLocks map[string][]dep.Update
	var gotManifest string
	post := map[string]PostProcessor{npm.Name: lockPost{
		postFunc: postFunc{siblings: []string{"package-lock.json"}},
		locked: func(_ string, manifests, _ map[string][]byte, locks map[string][]dep.Update) (map[string][]byte, error) {
			gotLocks, gotManifest = locks, string(manifests["package.json"])
			return map[string][]byte{"package-lock.json": []byte("new lock\n")}, nil
		},
	}}

	// Lockfile-only alone: package.json is unchanged, so only the lockfile changes.
	changes, errs := Build(t.Context(), root, o.Managers, []dep.Update{lockOnly}, post)
	if len(errs) > 0 || len(changes) != 1 || changes[0].Path != "web/package-lock.json" || string(changes[0].New) != "new lock\n" {
		t.Fatalf("changes = %+v, %v", changes, errs)
	}
	if len(gotLocks) != 1 || len(gotLocks["package.json"]) != 1 || gotLocks["package.json"][0].NewVersion != "1.2.0" || gotManifest != rangePkg {
		t.Errorf("locks = %+v, manifest = %q", gotLocks, gotManifest)
	}

	// Together with a range update: the manifest is patched for b only.
	changes, errs = Build(t.Context(), root, o.Managers, []dep.Update{lockOnly, bumped}, post)
	if len(errs) > 0 || len(changes) != 2 || changes[1].Path != "web/package.json" ||
		string(changes[1].New) != strings.Replace(rangePkg, `"~2.0.0"`, `"~2.1.0"`, 1) || gotManifest != string(changes[1].New) {
		t.Errorf("changes = %+v, %v", changes, errs)
	}

	// plan (no post-processors): nothing to show for the lockfile-only update.
	if changes, errs = Build(t.Context(), root, o.Managers, []dep.Update{lockOnly}, nil); len(changes) != 0 || len(errs) != 0 {
		t.Errorf("plan: changes = %+v, %v", changes, errs)
	}

	// An UpdatePinner also gets the manifest updates, and is called instead of ProcessLocked.
	var gotUps map[string][]dep.Update
	pinner := map[string]PostProcessor{npm.Name: pinPost{
		postFunc: postFunc{siblings: []string{"package-lock.json"}},
		pinned: func(ups, locks map[string][]dep.Update) (map[string][]byte, error) {
			gotUps, gotLocks = ups, locks
			return nil, nil
		},
	}}
	if _, errs = Build(t.Context(), root, o.Managers, []dep.Update{lockOnly, bumped}, pinner); len(errs) != 0 ||
		len(gotUps["package.json"]) != 1 || gotUps["package.json"][0].Dep.Name != "b" || len(gotLocks["package.json"]) != 1 || gotLocks["package.json"][0].Dep.Name != "a" {
		t.Errorf("pinner: ups = %+v, locks = %+v, %v", gotUps, gotLocks, errs)
	}

	// A post-processor without lockfile-only support, and a manifest without a lock root.
	plain := map[string]PostProcessor{npm.Name: postFunc{f: func(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
		return nil, errors.New("not called")
	}}}
	if _, errs = Build(t.Context(), root, o.Managers, []dep.Update{lockOnly}, plain); len(errs) != 1 ||
		errs[0].Error() != "web/package.json: the npm post-processor cannot apply lockfile-only updates" {
		t.Errorf("plain post-processor: errs = %v", errs)
	}
	noScope := map[string]PostProcessor{npm.Name: lockPost{postFunc: postFunc{scope: func(string, string) (string, bool) { return "", false }}}}
	if _, errs = Build(t.Context(), root, o.Managers, []dep.Update{lockOnly}, noScope); len(errs) != 1 ||
		errs[0].Error() != "web/package.json: lockfile-only update of a without a lockfile" {
		t.Errorf("no scope: errs = %v", errs)
	}
}

// A Gradle dynamic version keeps its floor, so rangeStrategy bump acts like update-lockfile:
// in-range candidates move only gradle.lockfile instead of producing an edit that changes
// nothing.
func TestRangeStrategyGradleBump(t *testing.T) {
	files := map[string]string{
		"build.gradle":    "dependencies {\n    implementation 'org.example:lib:1.+'\n}\n",
		"gradle.lockfile": "org.example:lib:1.1.0=compileClasspath,runtimeClasspath\nempty=\n",
	}
	for _, cfgText := range []string{"", "rangeStrategy: bump\n"} {
		root, cfg := setup(t, cfgText, files)
		o := Options{Managers: []manager.Manager{gradle.New()}, Datasources: map[string]Datasource{"maven": fakeGo{"org.example:lib": {"1.0.0", "1.1.0", "1.2.0", "2.0.0"}}}}
		res, err := Resolve(t.Context(), root, cfg, env, o)
		if err != nil || len(res.Errors) > 0 {
			t.Fatal(err, res.Errors)
		}
		want := "org.example:lib 1.1.0->1.2.0 minor lockfile\norg.example:lib 1.1.0->2.0.0 major"
		if got := renderUpdates(res.Updates); got != want {
			t.Errorf("%q: updates =\n%s\nwant\n%s", cfgText, got, want)
		}
	}
}
