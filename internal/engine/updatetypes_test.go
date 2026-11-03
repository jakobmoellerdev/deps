package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
)

// fakeDocker lists tags per image name; digests come from Digest only.
type fakeDocker map[string][]string

func (f fakeDocker) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	out := make([]Release, len(f[d.Name]))
	for i, v := range f[d.Name] {
		out[i] = Release{Version: v}
	}
	return out, nil
}

func (fakeDocker) Digest(_ context.Context, d dep.Dep, v string) (string, error) {
	return imageDigest(d.Name, v), nil
}

func imageDigest(name, v string) string {
	h := sha256.Sum256([]byte(name + ":" + v))
	return "sha256:" + hex.EncodeToString(h[:])
}

// ocmConfig mirrors open-component-model's Renovate packageRules: weekly Actions, a minor+patch
// Actions group, one "Digest pins" group for actions and images, indirect Go majors off and a
// path group for the controller chart.
const ocmConfig = `rules:
  - manager: github-actions
    schedule: ["* * * * 0"]
  - manager: github-actions
    updateTypes: [minor, patch]
    group: GitHub Actions
  - datasources: [github-tags, docker]
    updateTypes: [digest, pin]
    group: Digest pins
  - manager: gomod
    ignore: false
  - manager: gomod
    depTypes: [go]
    ignore: true
  - manager: gomod
    depTypes: [indirect]
    updateTypes: [major]
    ignore: true
  - paths: ["chart/**"]
    group: Kubernetes Controller chart
  - manager: docker
    names: [redis]
    pin: digest
`

func TestUpdateTypeRulesOCM(t *testing.T) {
	const (
		upload = "https://github.com/actions/upload-artifact"
		zero   = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	)
	files := map[string]string{
		".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n" +
			"      - uses: actions/checkout@v4.1.0\n" +
			"      - uses: actions/upload-artifact@v4.0.0\n" +
			"      - uses: actions/setup-go@v5.1.0\n" +
			"      - uses: actions/cache@" + sha("stale") + " # v4\n",
		"Dockerfile":                      "FROM alpine:3.20@" + zero + "\nFROM redis:7.4\n",
		"chart/templates/deployment.yaml": "spec:\n  containers:\n    - image: busybox:1.36.0\n    - image: nginx:1.27@" + zero + "\n",
		"go.mod":                          "module example.com/x\n\ngo 1.24\n\nrequire (\n\texample.com/direct v1.0.0\n\texample.com/ind v0.9.0 // indirect\n)\n",
	}
	root, cfg := setup(t, ocmConfig, files)
	o := Options{
		Managers: []manager.Manager{actions.New(), gomod.New(), docker.New()},
		Datasources: map[string]Datasource{
			"git-tags": fakeTags{
				checkout: {"v4.1.0", "v4.2.0", "v4.2.1"},
				upload:   {"v4.0.0", "v4.0.1"},
				setupGo:  {"v5.1.0", "v6.0.0"},
				cache:    {"v4"},
			},
			"go": fakeGo{
				"example.com/direct": {"v1.0.0", "v1.1.0"},
				"example.com/ind":    {"v0.9.0", "v0.9.5", "v0.10.0", "v1.0.0"},
			},
			"docker": fakeDocker{"alpine": {"3.20"}, "redis": {"7.4"}, "busybox": {"1.36.0", "1.36.1", "1.37.0"}, "nginx": {"1.27"}},
		},
	}
	traces := map[string]Trace{}
	o.Trace = func(t Trace) { traces[t.Dep.Name] = t }
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}

	type want struct {
		version string
		kind    dep.UpdateType
		group   string
		weekly  bool
	}
	wants := map[string]want{
		// (a) minor and patch bumps share the Actions group, the major gets its own PR.
		"actions/checkout":        {"v4.2.1", dep.Minor, "GitHub Actions", true},
		"actions/upload-artifact": {"v4.0.1", dep.Patch, "GitHub Actions", true},
		"actions/setup-go":        {"v6.0.0", dep.Major, "", true},
		// (b) digest refreshes and pins of actions and images share "Digest pins".
		"actions/cache": {"v4", dep.Digest, "Digest pins", true},
		"alpine":        {"3.20", dep.Digest, "Digest pins", false},
		"redis":         {"7.4", dep.Pin, "Digest pins", false},
		// (c) the indirect major is ignored, so the next minor is chosen.
		"example.com/ind":    {"v0.10.0", dep.Minor, "", false},
		"example.com/direct": {"v1.1.0", dep.Minor, "", false},
		// (d) the later path rule overrides the digest group for chart images.
		"busybox": {"1.37.0", dep.Minor, "Kubernetes Controller chart", false},
		"nginx":   {"1.27", dep.Digest, "Kubernetes Controller chart", false},
	}
	got := map[string]dep.Update{}
	for _, u := range res.Updates {
		got[u.Dep.Name] = u
	}
	if len(got) != len(wants) {
		t.Errorf("updates for %d deps, want %d: %+v", len(got), len(wants), res.Updates)
	}
	for name, w := range wants {
		u, ok := got[name]
		if !ok {
			t.Errorf("%s: no update", name)
			continue
		}
		if u.NewVersion != w.version || u.Type != w.kind || u.Group != w.group || (u.Schedule != nil) != w.weekly {
			t.Errorf("%s: %s %s group %q weekly %v, want %s %s %q %v", name, u.NewVersion, u.Type, u.Group, u.Schedule != nil, w.version, w.kind, w.group, w.weekly)
		}
	}
	if u := got["redis"]; u.NewDigest != imageDigest("redis", "7.4") {
		t.Errorf("redis digest = %q", u.NewDigest)
	}

	ind := traces["example.com/ind"]
	if r := ind.Rejected["policy: ignored by rule #5 for major"]; !slices.Equal(r, []string{"v1.0.0"}) {
		t.Errorf("ind rejected = %v", ind.Rejected)
	}
	if len(ind.Updates) != 1 || ind.Updates[0].Policy.Kind != dep.Minor || !slices.Equal(ind.Rules, []int{3}) {
		t.Errorf("ind trace: rules %v updates %+v", ind.Rules, ind.Updates)
	}
	if co := traces["actions/checkout"]; !slices.Equal(co.Rules, []int{0}) || len(co.Updates) != 1 || !slices.Equal(co.Updates[0].Policy.Rules, []int{0, 1}) {
		t.Errorf("checkout trace: rules %v updates %+v", co.Rules, co.Updates)
	}
	if goDir := traces["go"]; goDir.Dep.Skip != "ignored by rule #4" {
		t.Errorf("go directive: %+v", goDir.Dep)
	}

	type group struct{ branch, title string }
	var groups []group
	for _, g := range GroupUpdates(res.Updates, "") {
		groups = append(groups, group{g.Branch, g.Title})
	}
	wantGroups := []group{
		{"deps/github-actions/actions-setup-go-major", "chore(deps): update actions/setup-go to v6.0.0"},
		{"deps/gomod/example.com-direct", "fix(deps): update example.com/direct to v1.1.0"},
		{"deps/gomod/example.com-ind", "chore(deps): update example.com/ind to v0.10.0"},
		{"deps/group/digest-pins", "chore(deps): update Digest pins (3 dependencies)"},
		{"deps/group/github-actions", "chore(deps): update GitHub Actions (2 dependencies)"},
		{"deps/group/kubernetes-controller-chart", "chore(deps): update Kubernetes Controller chart (2 dependencies)"},
	}
	if !slices.Equal(groups, wantGroups) {
		t.Errorf("groups:\n%q\nwant:\n%q", groups, wantGroups)
	}
}

// TestUpdateTypeRuleOptsIn checks that an updateTypes rule with ignore: false resolves a dep
// the pre-policy ignores, for that update type only.
func TestUpdateTypeRuleOptsIn(t *testing.T) {
	mod := "module example.com/x\n\nrequire example.com/a v1.0.0 // indirect\n"
	root, cfg := setup(t, "rules: [{depTypes: [indirect], updateTypes: [patch], ignore: false}]\n", map[string]string{"go.mod": mod})
	var tr Trace
	o := sources(nil, goVersions)
	o.Trace = func(t Trace) { tr = t }
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 || len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.0.1" || res.Updates[0].Type != dep.Patch {
		t.Fatalf("err = %v, res = %+v", err, res)
	}
	const why = "policy: ignored by default (indirect; enable with a rule setting ignore: false)"
	if !slices.Equal(tr.Rejected[why], []string{"v1.1.0", "v1.2.0"}) || !tr.Policy.Ignore {
		t.Errorf("trace: policy %+v rejected %v", tr.Policy, tr.Rejected)
	}
}
