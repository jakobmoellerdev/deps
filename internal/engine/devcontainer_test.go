package engine

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/devcontainer"
)

// TestDevcontainerDigests checks that with pin: digest the devcontainer image is pinned while
// features are not (Renovate's pinDigests: false for features), a digest already written after
// a feature tag is refreshed, and a feature version option is updated from its tool datasource.
func TestDevcontainerDigests(t *testing.T) {
	const zero = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	const (
		base = "mcr.microsoft.com/devcontainers/base"
		node = "ghcr.io/devcontainers/features/node"
		sshd = "ghcr.io/devcontainers/features/sshd"
	)
	files := map[string]string{
		".devcontainer/devcontainer.json": `{
	// comment
	"image": "` + base + `:1.0",
	"features": {
		"` + node + `:1": {"version": "20.1.0"},
		"` + sshd + `:1@` + zero + `": {},
	},
}
`,
	}
	root, cfg := setup(t, "pin: digest\n", files)
	o := Options{
		Managers: []manager.Manager{devcontainer.New()},
		Datasources: map[string]Datasource{
			"docker":       fakeDocker{base: {"1.0", "1.1"}, node: {"1", "2"}, sshd: {"1"}},
			"node-version": fakeReleases{"20.1.0", "20.2.0"},
		},
	}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	type want struct {
		version, digest string
		kind            dep.UpdateType
	}
	wants := map[string]want{
		base:   {"1.1", imageDigest(base, "1.1"), dep.Minor},
		node:   {"2", "", dep.Major},
		sshd:   {"1", imageDigest(sshd, "1"), dep.Digest},
		"node": {"20.2.0", "", dep.Minor},
	}
	got := map[string]dep.Update{}
	for _, u := range res.Updates {
		got[u.Dep.Name] = u
	}
	if len(got) != len(wants) {
		t.Errorf("updates for %d deps, want %d: %+v", len(got), len(wants), res.Updates)
	}
	for name, w := range wants {
		if u := got[name]; u.NewVersion != w.version || u.NewDigest != w.digest || u.Type != w.kind {
			t.Errorf("%s: %s %q %s, want %s %q %s", name, u.NewVersion, u.NewDigest, u.Type, w.version, w.digest, w.kind)
		}
	}
}
