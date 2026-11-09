package engine

import (
	"context"
	"fmt"
	"slices"
	"testing"

	vcpkgds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/vcpkg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/vcpkg"
)

// fakePorts lists port versions by registry tree (dep.Dep.Source) and port name.
type fakePorts map[string]map[string][]string

func (f fakePorts) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	vs, ok := f[d.Source][d.Name]
	if !ok {
		return nil, fmt.Errorf("port %s not in %s", d.Name, d.Source)
	}
	rels := make([]Release, len(vs))
	for i, v := range vs {
		rels[i] = Release{Version: v}
	}
	return rels, nil
}

func (fakePorts) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

// A moving builtin-baseline unlocks port versions in the same run: ports are looked up at the
// new commit when their updates share the baseline update's group and bucket (fmt), else at
// the current baseline (curl, whose new major would get a PR of its own; every port with
// monorepos: false).
func TestVcpkgPortsAtMovedBaseline(t *testing.T) {
	const (
		old  = "0000000000000000000000000000000000000000"
		head = "1111111111111111111111111111111111111111"
	)
	manifest := `{
  "name": "app",
  "builtin-baseline": "` + old + `",
  "dependencies": [
    {"name": "fmt", "version>=": "11.0.2"},
    {"name": "curl", "version>=": "8.1.0"}
  ]
}
`
	oldBase, headBase := vcpkgds.RawBase(vcpkg.BuiltinRepo, old), vcpkgds.RawBase(vcpkg.BuiltinRepo, head)
	opts := Options{Managers: []manager.Manager{vcpkg.New()}, Datasources: map[string]Datasource{
		"git-refs": fakeBranches{vcpkg.BuiltinRepo + "#HEAD": head},
		"vcpkg": fakePorts{
			oldBase:  {"fmt": {"11.0.2", "11.1.0"}, "curl": {"8.1.0", "8.2.0"}},
			headBase: {"fmt": {"11.0.2", "11.1.0", "11.2.0#1"}, "curl": {"8.1.0", "8.2.0", "8.3.0", "9.0.0"}},
		},
	}}
	type up struct {
		name, version, source, group string
	}
	for _, tc := range []struct {
		cfg  string
		want []up
	}{
		{"", []up{
			{"microsoft/vcpkg", "HEAD", vcpkg.BuiltinRepo, "vcpkg"},
			{"fmt", "11.2.0#1", headBase, "vcpkg"},
			{"curl", "8.2.0", oldBase, "vcpkg"},
		}},
		{"monorepos: false\n", []up{
			{"microsoft/vcpkg", "HEAD", vcpkg.BuiltinRepo, ""},
			{"fmt", "11.1.0", oldBase, ""},
			{"curl", "8.2.0", oldBase, ""},
		}},
	} {
		root, cfg := setup(t, tc.cfg, map[string]string{"vcpkg.json": manifest})
		res, err := Resolve(t.Context(), root, cfg, env, opts)
		if err != nil || len(res.Errors) > 0 {
			t.Fatalf("%q: err = %v, errors = %v", tc.cfg, err, res.Errors)
		}
		var got []up
		for _, u := range res.Updates {
			got = append(got, up{u.Dep.Name, u.NewVersion, u.Dep.Source, u.Group})
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q: updates = %+v\nwant      %+v", tc.cfg, got, tc.want)
		}
	}
}
