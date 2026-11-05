package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// fakeSec returns advisories per dep name and records what it was asked.
type fakeSec struct {
	vulns map[string][]Vuln
	err   error
	short bool // return one result too few

	mu    sync.Mutex
	calls int
	asked []string
}

func (f *fakeSec) Advisories(_ context.Context, deps []dep.Dep) ([][]Vuln, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	out := make([][]Vuln, len(deps))
	for i, d := range deps {
		f.asked = append(f.asked, d.Name)
		out[i] = f.vulns[d.Name]
	}
	if f.short {
		out = out[1:]
	}
	return out, f.err
}

func vuln(id string, fixed ...string) Vuln {
	return Vuln{Advisory: dep.Advisory{ID: id, Summary: "bad " + id, URL: "https://osv.dev/" + id}, Fixed: fixed}
}

func ids(u *dep.Update) []string {
	var out []string
	for _, a := range u.Advisories {
		out = append(out, a.ID)
	}
	return out
}

var goVersions = fakeGo{"example.com/a": {"v1.0.0", "v1.0.1", "v1.1.0", "v1.2.0", "v2.0.0"}}

const modA = "module example.com/x\n\nrequire example.com/a v1.0.0\n"

func TestSecurityUpdates(t *testing.T) {
	tests := []struct {
		name       string
		cfg        string
		vulns      []Vuln
		want       string // "" = no update
		wantType   dep.UpdateType
		wantIDs    []string
		wantGroup  string
		wantSec    string // dep.Update.SecurityGroup
		wantSkip   string
		wantNoAsks bool
	}{
		{name: "fix bypasses updates", cfg: "rules: [{updates: [patch], group: g, schedule: ['* 1 * * *']}]\n",
			vulns: []Vuln{vuln("A", "v1.1.0")}, want: "v1.1.0", wantType: dep.Minor, wantIDs: []string{"A"}},
		{name: "fix bypasses allow", cfg: "rules: [{allow: '< 1.1'}]\n",
			vulns: []Vuln{vuln("A", "v1.1.0")}, want: "v1.1.0", wantType: dep.Minor, wantIDs: []string{"A"}},
		{name: "normal update already fixes", cfg: "rules: [{group: g}]\n",
			vulns: []Vuln{vuln("A", "v1.1.0")}, want: "v1.2.0", wantType: dep.Minor, wantIDs: []string{"A"}},
		{name: "minimum fix is max over vulns", cfg: "rules: [{updates: [patch]}]\n",
			vulns: []Vuln{vuln("A", "v1.0.1", "v1.1.0"), vuln("B", "v1.1.0")}, want: "v1.1.0", wantType: dep.Minor, wantIDs: []string{"A", "B"}},
		{name: "only advisories the version fixes", cfg: "rules: [{updates: [patch]}]\n",
			vulns: []Vuln{vuln("A", "v1.0.1"), vuln("B", "v9.9.9")}, want: "v1.0.1", wantType: dep.Patch, wantIDs: []string{"A"}},
		{name: "no compatible fix keeps normal update", cfg: "rules: [{group: g}]\n",
			vulns: []Vuln{vuln("A", "v2.0.0")}, want: "v1.2.0", wantType: dep.Minor, wantGroup: "g"},
		{name: "fix not released keeps normal update", cfg: "rules: [{group: g}]\n",
			vulns: []Vuln{vuln("A", "v1.3.0")}, want: "v1.2.0", wantType: dep.Minor, wantGroup: "g"},
		{name: "fix not released and no normal update", cfg: "rules: [{allow: '< 1'}]\n",
			vulns: []Vuln{vuln("A", "v1.3.0")}},
		{name: "ignore still wins", cfg: "rules: [{ignore: true}]\n",
			vulns: []Vuln{vuln("A", "v1.1.0")}, wantSkip: "ignored by rule #0", wantNoAsks: true},
		{name: "kind ignore does not block a fix", cfg: "rules: [{updateTypes: [minor], ignore: true}]\n",
			vulns: []Vuln{vuln("A", "v1.1.0")}, want: "v1.1.0", wantType: dep.Minor, wantIDs: []string{"A"}},
		{name: "kind group of the normal update", cfg: "rules: [{updateTypes: [minor], group: minors}]\n",
			vulns: []Vuln{vuln("A", "v9.0.0")}, want: "v1.2.0", wantType: dep.Minor, wantGroup: "minors"},
		{name: "pre-policy ignore wins over kind opt-in", cfg: "rules: [{ignore: true}, {updateTypes: [patch], ignore: false}]\n",
			vulns: []Vuln{vuln("A", "v1.1.0")}, want: "v1.0.1", wantType: dep.Patch, wantNoAsks: true},
		{name: "security group opt-in", cfg: "rules: [{group: g, securityGroup: sec}]\n",
			vulns: []Vuln{vuln("A", "v1.1.0")}, want: "v1.2.0", wantType: dep.Minor, wantIDs: []string{"A"}, wantSec: "sec"},
		{name: "security group not on a normal update", cfg: "rules: [{group: g, securityGroup: sec}]\n",
			vulns: []Vuln{vuln("A", "v2.0.0")}, want: "v1.2.0", wantType: dep.Minor, wantGroup: "g"},
		{name: "kind security group", cfg: "rules: [{securityGroup: all}, {updateTypes: [minor], securityGroup: minors}]\n",
			vulns: []Vuln{vuln("A", "v1.1.0")}, want: "v1.2.0", wantType: dep.Minor, wantIDs: []string{"A"}, wantSec: "minors"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"go.mod": modA})
			sec := &fakeSec{vulns: map[string][]Vuln{"example.com/a": tt.vulns}}
			o := sources(nil, goVersions)
			o.Security = sec
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("err = %v, errors = %v", err, res.Errors)
			}
			if tt.wantNoAsks != (sec.calls == 0) {
				t.Errorf("security calls = %d, asked %v", sec.calls, sec.asked)
			}
			if tt.wantSkip != "" && (len(res.Skipped) != 1 || res.Skipped[0].Skip != tt.wantSkip) {
				t.Errorf("skipped = %+v", res.Skipped)
			}
			if tt.want == "" {
				if len(res.Updates) != 0 {
					t.Errorf("updates = %+v, want none", res.Updates)
				}
				return
			}
			if len(res.Updates) != 1 {
				t.Fatalf("updates = %+v", res.Updates)
			}
			u := &res.Updates[0]
			if u.NewVersion != tt.want || u.Type != tt.wantType || !slices.Equal(ids(u), tt.wantIDs) || u.Group != tt.wantGroup || u.SecurityGroup != tt.wantSec {
				t.Errorf("update = %s %s ids=%v group=%q securityGroup=%q, want %s %s ids=%v group=%q securityGroup=%q", u.NewVersion, u.Type, ids(u), u.Group, u.SecurityGroup, tt.want, tt.wantType, tt.wantIDs, tt.wantGroup, tt.wantSec)
			}
			if len(u.Advisories) > 0 && (u.Schedule != nil || u.Advisories[0].URL == "") {
				t.Errorf("security update: schedule %v, advisories %+v", u.Schedule, u.Advisories)
			}
		})
	}
}

func TestSecurityPinnedAction(t *testing.T) {
	wf := "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@" + sha(checkout+"@v4.1.0") + " # v4.1.0\n"
	root, cfg := setup(t, "rules: [{updates: [digest]}]\n", map[string]string{".github/workflows/ci.yml": wf})
	o := sources(tags, nil)
	o.Security = &fakeSec{vulns: map[string][]Vuln{"actions/checkout": {vuln("GHSA-1", "v4.1.1")}}}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 || len(res.Updates) != 1 {
		t.Fatalf("err = %v, res = %+v", err, res)
	}
	u := res.Updates[0]
	if u.NewVersion != "v4.1.1" || u.NewDigest != sha(checkout+"@v4.1.1") || u.Type != dep.Patch || len(u.Advisories) != 1 {
		t.Fatalf("update = %+v", u)
	}
	changes, errs := Build(t.Context(), root, managers, res.Updates, nil)
	if len(errs) > 0 || len(changes) != 1 {
		t.Fatalf("changes = %+v, errs = %v", changes, errs)
	}
	if want := "      - uses: actions/checkout@" + sha(checkout+"@v4.1.1") + " # v4.1.1"; line(4, string(changes[0].New)) != want {
		t.Errorf("line = %q", line(4, string(changes[0].New)))
	}
}

func TestSecurityFailures(t *testing.T) {
	for name, sec := range map[string]*fakeSec{
		"error":       {err: errors.New("boom")},
		"wrong count": {short: true},
	} {
		t.Run(name, func(t *testing.T) {
			root, cfg := setup(t, "", map[string]string{"go.mod": modA})
			o := sources(nil, goVersions)
			o.Security = sec
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Errors) != 1 || !strings.HasPrefix(res.Errors[0].Error(), "security advisories: ") {
				t.Errorf("errors = %v", res.Errors)
			}
			if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.2.0" || len(res.Updates[0].Advisories) != 0 {
				t.Errorf("updates = %+v, want the normal update", res.Updates)
			}
		})
	}
	t.Run("no updatable deps", func(t *testing.T) {
		root, cfg := setup(t, "rules: [{ignore: true}]\n", map[string]string{"go.mod": modA})
		sec := &fakeSec{}
		o := sources(nil, goVersions)
		o.Security = sec
		if _, err := Resolve(t.Context(), root, cfg, env, o); err != nil || sec.calls != 0 {
			t.Errorf("err = %v, calls = %d", err, sec.calls)
		}
	})
}

func TestGroupAndScheduleFromPolicy(t *testing.T) {
	root, cfg := setup(t, "schedule: ['0 1 * * *']\nrules: [{group: all}]\n", map[string]string{"go.mod": modA})
	res, err := Resolve(t.Context(), root, cfg, env, sources(nil, goVersions))
	if err != nil || len(res.Updates) != 1 {
		t.Fatalf("err = %v, res = %+v", err, res)
	}
	if u := res.Updates[0]; u.Group != "all" || u.Schedule == nil || u.Schedule.String() != "0 1 * * *" {
		t.Errorf("update group %q schedule %v", u.Group, u.Schedule)
	}
}

func TestTrace(t *testing.T) {
	wf := "jobs:\n  a:\n    steps:\n" +
		"      - uses: ./local\n" +
		"      - uses: actions/checkout@v4.1.0\n" +
		"      - uses: actions/cache@v3\n" +
		"      - uses: owner/broken@v1\n"
	root, cfg := setup(t, "rules:\n  - names: ['actions/*']\n    allow: '< 5'\n  - names: [actions/cache]\n    ignore: true\n  - manager: gomod\n",
		map[string]string{".github/workflows/ci.yml": wf})
	var traces []Trace
	o := sources(tags, nil)
	o.Trace = func(t Trace) { traces = append(traces, t) }
	if _, err := Resolve(t.Context(), root, cfg, env, o); err != nil {
		t.Fatal(err)
	}
	byName := map[string]Trace{}
	for _, tr := range traces {
		byName[tr.Dep.Name] = tr
	}
	if len(traces) != 4 {
		t.Fatalf("traces = %+v", traces)
	}
	if tr := byName["./local"]; tr.Dep.Skip != "local action" || tr.Updates != nil {
		t.Errorf("local: %+v", tr)
	}
	if tr := byName["actions/cache"]; !tr.Policy.Ignore || !slices.Equal(tr.Rules, []int{0, 1}) || tr.Dep.Skip != "ignored by rule #1" {
		t.Errorf("cache: %+v", tr)
	}
	if tr := byName["owner/broken"]; tr.Err == nil || !strings.HasSuffix(tr.Err.Error(), "owner/broken: upstream unavailable") {
		t.Errorf("broken: %+v", tr)
	}
	co := byName["actions/checkout"]
	if len(co.Updates) != 1 || co.Updates[0].Update.NewVersion != "v4.2.0" || !slices.Equal(co.Rules, []int{0}) || co.Candidates != len(tags[checkout]) || co.Err != nil {
		t.Fatalf("checkout: %+v", co)
	}
	want := map[string][]string{
		"precision":                {"v4", "v5"},
		"not newer":                {"v4.1.0"},
		"policy: outside allow <5": {"v5.0.0", "v5.0.1"},
		"invalid":                  {"v6.0.0-beta.1"},
	}
	for reason, vs := range want {
		if !slices.Equal(co.Rejected[reason], vs) {
			t.Errorf("rejected[%q] = %v, want %v", reason, co.Rejected[reason], vs)
		}
	}
}

// countingTags counts Releases calls per source.
type countingTags struct {
	fakeTags
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingTags) Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error) {
	c.mu.Lock()
	c.calls[d.Source]++
	c.mu.Unlock()
	return c.fakeTags.Releases(ctx, d, dir)
}

func TestFilter(t *testing.T) {
	root, cfg := setup(t, "", map[string]string{".github/workflows/ci.yml": workflow})
	ct := &countingTags{fakeTags: tags, calls: map[string]int{}}
	sec := &fakeSec{}
	o := Options{
		Managers:    managers,
		Datasources: map[string]Datasource{"git-tags": ct},
		Security:    sec,
		Filter:      func(d dep.Dep) bool { return d.Name == "actions/checkout" },
	}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct.calls) != 1 || ct.calls[checkout] != 1 {
		t.Errorf("datasource calls = %v, want only checkout", ct.calls)
	}
	if !slices.Equal(sec.asked, []string{"actions/checkout"}) {
		t.Errorf("security asked %v", sec.asked)
	}
	if len(res.Updates) != 2 || res.Updates[0].Dep.Name != "actions/checkout" || res.Updates[1].Bucket != "major" || len(res.Errors) != 0 || len(res.Skipped) != 0 {
		t.Errorf("res = %+v", res)
	}
}

// stubManager emits fixed deps for stub.txt; Edits replaces RefSpan unless editErr is set.
type stubManager struct {
	name    string
	deps    []dep.Dep
	editErr error
}

func (s stubManager) Name() string         { return s.name }
func (stubManager) Match(path string) bool { return path == "stub.txt" }
func (s stubManager) Extract(file string, _ []byte, _ manager.Env) ([]dep.Dep, error) {
	out := slices.Clone(s.deps)
	for i := range out {
		out[i].Manager, out[i].File = s.name, file
	}
	return out, nil
}

func (s stubManager) Edits(u dep.Update) ([]patch.Edit, error) {
	if s.editErr != nil {
		return nil, s.editErr
	}
	n := u.NewVersion
	if u.NewDigest != "" {
		n += "@" + u.NewDigest
	}
	return []patch.Edit{{Start: u.Dep.RefSpan.Start, End: u.Dep.RefSpan.End, Old: u.Dep.Version, New: n}}, nil
}

// dockerish has no digests in its releases, so digests come from Digest.
type dockerish struct{}

func (dockerish) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return []Release{{Version: "1.25"}, {Version: "1.26"}}, nil
}

func (dockerish) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	if v == "1.99" {
		return "", errors.New("manifest unknown")
	}
	return "sha256:" + v, nil
}

func TestResolveGenericManagers(t *testing.T) {
	const src = "1.25 1.99 3 x\n"
	d := func(name, ds, vs, ver string, start int) dep.Dep {
		return dep.Dep{Datasource: ds, Versioning: vs, Name: name, Version: ver, Line: 1, RefSpan: dep.Span{Start: start, End: start + len(ver)}}
	}
	docker := stubManager{name: "docker", deps: []dep.Dep{
		d("img", "docker", "docker", "1.25", 0),
		d("gone", "docker", "docker", "1.99", 5),
	}}
	custom := stubManager{name: "custom", deps: []dep.Dep{
		d("cal", "docker", "calver", "3", 10),
		d("nods", "npm", "semver", "1.0.0", 12),
	}}
	root, cfg := setup(t, "pin: digest\n", map[string]string{"stub.txt": src})
	o := Options{Managers: []manager.Manager{docker, custom}, Datasources: map[string]Datasource{"docker": dockerish{}}}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil {
		t.Fatal(err)
	}
	wantErrs := []string{
		`stub.txt:1: gone: manifest unknown`,
		`stub.txt:1: cal: unknown versioning "calver"`,
		`stub.txt:1: nods: no datasource "npm"`,
	}
	if len(res.Errors) != len(wantErrs) {
		t.Fatalf("errors = %v", res.Errors)
	}
	for i, w := range wantErrs {
		if res.Errors[i].Error() != w {
			t.Errorf("error %d = %q, want %q", i, res.Errors[i], w)
		}
	}
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "1.26" || res.Updates[0].NewDigest != "sha256:1.26" {
		t.Fatalf("updates = %+v", res.Updates)
	}
	changes, errs := Build(t.Context(), root, o.Managers, res.Updates, nil)
	if len(errs) > 0 || len(changes) != 1 || string(changes[0].New) != "1.26@sha256:1.26 1.99 3 x\n" {
		t.Fatalf("changes = %+v, errs = %v", changes, errs)
	}

	// Build failures: unknown manager and a manager edit error drop the file.
	up := res.Updates[0]
	if _, errs := Build(t.Context(), root, nil, []dep.Update{up}, nil); len(errs) != 1 || errs[0].Error() != `stub.txt: unknown manager "docker"` {
		t.Errorf("unknown manager errs = %v", errs)
	}
	bad := stubManager{name: "docker", editErr: errors.New("no")}
	if _, errs := Build(t.Context(), root, []manager.Manager{bad}, []dep.Update{up}, nil); len(errs) != 1 || errs[0].Error() != "stub.txt: img: no" {
		t.Errorf("edit errs = %v", errs)
	}
}

// A current value the scheme cannot parse (`latest`) is a skip, not an error: nothing can be
// compared against it.
func TestResolveUnparsableVersion(t *testing.T) {
	m := stubManager{name: "custom", deps: []dep.Dep{{Datasource: "docker", Versioning: "docker", Name: "x", Version: "latest", Line: 1}}}
	root, cfg := setup(t, "", map[string]string{"stub.txt": "latest\n"})
	res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: []manager.Manager{m}, Datasources: map[string]Datasource{"docker": dockerish{}}})
	if err != nil || len(res.Errors) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Skip != `non-version value "latest"` {
		t.Fatalf("err = %v, errors = %v, skipped = %+v", err, res.Errors, res.Skipped)
	}
}

// noDigests is a datasource without digest support (github-releases): Digest returns "".
type noDigests struct{}

func (noDigests) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return []Release{{Version: "v1.0.0"}}, nil
}

func (noDigests) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

// A custom dep that captured a digest (Renovate's currentDigest of a github-release-attachments
// marker) gets no digest update when its datasource resolves none: there is nothing to write.
func TestResolveNoDigestNoUpdate(t *testing.T) {
	m := stubManager{name: "custom", deps: []dep.Dep{{Datasource: "github-releases", Versioning: "semver", Name: "o/r", Version: "v1.0.0", Digest: strings.Repeat("a", 64), Line: 1}}}
	root, cfg := setup(t, "", map[string]string{"stub.txt": "v1.0.0\n"})
	res, err := Resolve(t.Context(), root, cfg, env, Options{Managers: []manager.Manager{m}, Datasources: map[string]Datasource{"github-releases": noDigests{}}})
	if err != nil || len(res.Errors) != 0 || len(res.Updates) != 0 {
		t.Fatalf("err = %v, errors = %v, updates = %+v", err, res.Errors, res.Updates)
	}
}

func TestManagersRegistry(t *testing.T) {
	names := func(ms []manager.Manager) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Name())
		}
		return out
	}
	ms, err := Managers(config.Default())
	builtin := []string{"github-actions", "gomod", "docker", "npm", "helm", "kustomize", "pre-commit", "gitlabci", "nvm", "nodenv", "asdf", "mise", "cargo",
		"pip_requirements", "pep621", "poetry", "pipenv", "terraform", "terraform-version", "tflint-plugin", "terragrunt",
		"maven", "gradle", "gradle-wrapper", "maven-wrapper", "swift"}
	if err != nil || !slices.Equal(names(ms), builtin) {
		t.Fatalf("default managers = %v, %v", names(ms), err)
	}
	cfg := config.Default()
	cfg.Custom = []config.Custom{{Files: []string{"Taskfile.yml"}}, {Files: []string{"Makefile"}, Match: `V=(?P<version>\S+)`, Datasource: "go", Name: "example.com/x"}}
	ms, err = Managers(cfg)
	if err != nil || !slices.Equal(names(ms), append(slices.Clone(builtin), "custom", "custom")) {
		t.Fatalf("custom managers = %v, %v", names(ms), err)
	}
	cfg.Custom = []config.Custom{{}}
	if _, err := Managers(cfg); err == nil {
		t.Fatal("invalid custom entry accepted")
	}
}

// timedGo lists Go versions and reports release times; v1.5.2 is a legacy tag older than v0.37.1.
type timedGo struct{ legacyNewer bool }

func (timedGo) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return []Release{{Version: "v0.37.1"}, {Version: "v0.38.0"}, {Version: "v1.5.2"}}, nil
}

func (timedGo) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

func (g timedGo) ReleaseTime(_ context.Context, _ dep.Dep, v string) (time.Time, error) {
	switch v {
	case "v1.5.2":
		if g.legacyNewer {
			return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), nil
		}
		return time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC), nil
	case "v0.37.1":
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, nil
}

func TestResolveSkipsMajorsOlderThanCurrent(t *testing.T) {
	for _, tt := range []struct {
		legacyNewer bool
		want        []string // the minor bucket, then the major bucket unless it is stale
	}{{false, []string{"v0.38.0"}}, {true, []string{"v0.38.0", "v1.5.2"}}} {
		m := stubManager{name: "gomod", deps: []dep.Dep{{Datasource: "go", Versioning: "go", Name: "k8s.io/client-go", Version: "v0.37.1", Line: 1}}}
		root, cfg := setup(t, "", map[string]string{"stub.txt": "v0.37.1\n"})
		var tr Trace
		res, err := Resolve(t.Context(), root, cfg, env, Options{
			Managers:    []manager.Manager{m},
			Datasources: map[string]Datasource{"go": timedGo{legacyNewer: tt.legacyNewer}},
			Trace:       func(x Trace) { tr = x },
		})
		var got []string
		for _, u := range res.Updates {
			got = append(got, u.NewVersion)
		}
		if err != nil || !slices.Equal(got, tt.want) {
			t.Fatalf("legacyNewer=%v: updates = %v, err = %v", tt.legacyNewer, got, err)
		}
		if !tt.legacyNewer && !slices.Equal(tr.Rejected["older than current"], []string{"v1.5.2"}) {
			t.Errorf("rejected = %v", tr.Rejected)
		}
	}
}

// movingMajors lists two floating majors whose times are those of their current content, the
// older major last (rebuilt docker images; git tags moved by a backport release), and counts
// the release-time lookups.
type movingMajors struct {
	older, newer string
	lookups      *int
}

func (m movingMajors) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return []Release{{Version: m.older}, {Version: m.newer}}, nil
}

func (movingMajors) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

func (m movingMajors) ReleaseTime(_ context.Context, _ dep.Dep, v string) (time.Time, error) {
	*m.lookups++
	if v == m.older {
		return time.Date(2026, 11, 1, 1, 31, 46, 0, time.UTC), nil
	}
	return time.Date(2026, 11, 1, 1, 30, 24, 0, time.UTC), nil
}

func TestResolveMovingMajorsNotOlder(t *testing.T) {
	for _, tt := range []struct {
		manager, datasource, versioning, older, newer string
	}{
		{"docker", "docker", "docker", "14", "18"},
		{"github-actions", "git-tags", "tag", "v5", "v7"},
	} {
		var lookups int
		m := stubManager{name: tt.manager, deps: []dep.Dep{{Datasource: tt.datasource, Versioning: tt.versioning, Name: "x", Version: tt.older, Line: 1}}}
		root, cfg := setup(t, "", map[string]string{"stub.txt": tt.older + "\n"})
		res, err := Resolve(t.Context(), root, cfg, env, Options{
			Managers:    []manager.Manager{m},
			Datasources: map[string]Datasource{tt.datasource: movingMajors{older: tt.older, newer: tt.newer, lookups: &lookups}},
		})
		if err != nil || len(res.Updates) != 1 || res.Updates[0].NewVersion != tt.newer {
			t.Fatalf("%s: updates = %v, err = %v", tt.datasource, res.Updates, err)
		}
		if lookups != 0 {
			t.Errorf("%s: release times looked up %d times without a minimum release age", tt.datasource, lookups)
		}
	}
}
