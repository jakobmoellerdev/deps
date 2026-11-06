package engine

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubrunners"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
)

// TestGitHubRunners checks that runner labels update within their suffix, are never
// digest-pinned under pin: digest (github-runners has no digests), and that a deprecated
// image is reported.
func TestGitHubRunners(t *testing.T) {
	files := map[string]string{".github/workflows/ci.yml": `on: push
jobs:
  a:
    runs-on: ubuntu-20.04
  b:
    runs-on: [self-hosted, ubuntu-24.04-arm]
  c:
    runs-on: macos-26
`}
	root, cfg := setup(t, "pin: digest\n", files)
	o := Options{
		Managers:    []manager.Manager{actions.New()},
		Datasources: map[string]Datasource{githubrunners.Name: githubrunners.New()},
	}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	got := map[string]dep.Update{}
	for _, u := range res.Updates {
		got[u.Dep.Name+"-"+u.Dep.Version] = u
	}
	wants := map[string]string{"ubuntu-20.04": "26.04", "ubuntu-24.04-arm": "26.04-arm"}
	if len(got) != len(wants) {
		t.Errorf("updates = %+v, want %v", res.Updates, wants)
	}
	for label, v := range wants {
		if u := got[label]; u.NewVersion != v || u.NewDigest != "" || u.Type != dep.Major {
			t.Errorf("%s: %s %q %s, want %s major without digest", label, u.NewVersion, u.NewDigest, u.Type, v)
		}
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Kind != WarnDeprecated || res.Warnings[0].Reason != "runner image ubuntu-20.04 deprecated" {
		t.Errorf("warnings = %+v, want ubuntu-20.04 deprecated", res.Warnings)
	}
}
