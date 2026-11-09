package engine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/depsconfig"
)

// The extends entries of .deps.yaml: a version tag moves to the newest tag, a commit to the
// default branch head (digest-only), and Build rewrites both refs.
func TestDepsConfigExtends(t *testing.T) {
	const (
		forge = "https://git.example.com"
		repo  = forge + "/org/deps-config"
		old   = "0000000000000000000000000000000000000000"
		head  = "1111111111111111111111111111111111111111"
	)
	root, cfg := setup(t, "", map[string]string{})
	// Written after the config is loaded: config.Load would fetch the extended files.
	src := "extends:\n  - org/deps-config#v1.0.0\n  - org/deps-config//go.yaml#" + old + "\n"
	if err := os.WriteFile(filepath.Join(root, ".deps.yaml"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ms := []manager.Manager{depsconfig.New()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{
		"git-tags": fakeTags{repo: {"v1.0.0", "v1.1.0"}},
		"git-refs": fakeBranches{repo + "#HEAD": head},
	}}
	e := env
	e.ForgeURL = forge
	res, err := Resolve(t.Context(), root, cfg, e, opts)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	type up struct {
		version, digest string
		kind            dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.NewVersion, u.NewDigest, u.Type})
	}
	want := []up{{"v1.1.0", "", dep.Minor}, {"HEAD", head, dep.Digest}}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}
	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	if len(errs) > 0 || len(changes) != 1 {
		t.Fatalf("changes = %+v, errs = %v", changes, errs)
	}
	if w := "extends:\n  - org/deps-config#v1.1.0\n  - org/deps-config//go.yaml#" + head + "\n"; string(changes[0].New) != w {
		t.Errorf(".deps.yaml = %q, want %q", changes[0].New, w)
	}
}
