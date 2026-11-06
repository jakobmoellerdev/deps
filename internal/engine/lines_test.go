package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// datedReleases lists release names newest first, each released a day before the previous
// one, and records the names of the release time lookups.
type datedReleases struct {
	names []string
	asked []string
}

func (f *datedReleases) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	rels := make([]Release, len(f.names))
	for i, n := range f.names {
		rels[i] = Release{Version: n}
	}
	return rels, nil
}

func (*datedReleases) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

func (f *datedReleases) ReleaseTime(_ context.Context, _ dep.Dep, v string) (time.Time, error) {
	f.asked = append(f.asked, v)
	i := slices.Index(f.names, v)
	if i < 0 {
		return time.Time{}, nil
	}
	return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i), nil
}

// releasesByName is a datasource with the releases of each dep name.
type releasesByName map[string]*datedReleases

func (f releasesByName) Releases(ctx context.Context, d dep.Dep, dir string) ([]Release, error) {
	return f[d.Name].Releases(ctx, d, dir)
}

func (f releasesByName) Digest(ctx context.Context, d dep.Dep, v string) (string, error) {
	return f[d.Name].Digest(ctx, d, v)
}

func (f releasesByName) ReleaseTime(ctx context.Context, d dep.Dep, v string) (time.Time, error) {
	return f[d.Name].ReleaseTime(ctx, d, v)
}

// Partial asdf/mise versions of tools whose datasource lists only full versions keep their
// precision: the release lines are derived from the stable X.Y.Z releases (and the bare form
// of `vX.Y` releases), release times are those of a line's first release, and versions of
// another precision or prefix are no candidates.
func TestResolvePartialToolVersions(t *testing.T) {
	root, cfg := setup(t, "minimumReleaseAge: 1 day\n", map[string]string{
		".tool-versions": "golangci-lint 2.5\nkubectl v1.33\nprotoc 29\nhelm 3.16.0\n",
		"mise.toml":      "[tools]\n\"go:golang.org/x/tools/gopls\" = \"0.19\"\npnpm = \"9\"\n",
	})
	ms, err := Managers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gh := releasesByName{
		"golangci/golangci-lint":   {names: []string{"v2.7.0-rc.1", "v2.6.1", "v2.6.0", "v2.5.0", "v1.64.8"}},
		"kubernetes/kubernetes":    {names: []string{"v1.35.0-alpha.1", "v1.34.1", "v1.34.0", "v1.33.5"}},
		"protocolbuffers/protobuf": {names: []string{"v31.1", "v31.0", "v30.2", "v29.3"}},
		"helm/helm":                {names: []string{"v3.17.0", "v3.16.0"}},
	}
	gomod := &datedReleases{names: []string{"v0.20.1", "v0.20.0", "v0.19.0"}}
	npm := &datedReleases{names: []string{"10.1.0", "10.0.0", "9.15.0"}}
	o := Options{Managers: ms, Datasources: map[string]Datasource{"github-releases": gh, "go": gomod, "npm": npm}}
	res, err := Resolve(t.Context(), root, cfg, env, o)
	if err != nil || len(res.Errors) != 0 {
		t.Fatal(err, res.Errors)
	}
	var got []string
	for _, u := range res.Updates {
		got = append(got, u.Dep.Name+" "+u.Dep.Version+" "+u.NewVersion)
	}
	slices.Sort(got)
	want := []string{
		"golang.org/x/tools/gopls 0.19 0.20",
		"golangci/golangci-lint 2.5 2.6",
		"helm/helm 3.16.0 3.17.0",
		"kubernetes/kubernetes 1.33 1.34",
		"pnpm 9 10",
		"protocolbuffers/protobuf 29 31",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %q, want %q", got, want)
	}
	for name, w := range map[string]string{"golangci/golangci-lint": "v2.6.0", "kubernetes/kubernetes": "v1.34.0", "protocolbuffers/protobuf": "v31.0"} {
		if !slices.Contains(gh[name].asked, w) {
			t.Errorf("%s: %s not asked: %q", name, w, gh[name].asked)
		}
	}
	if !slices.Contains(gomod.asked, "v0.20.0") || !slices.Contains(npm.asked, "10.0.0") {
		t.Errorf("first release times not asked: go %q, npm %q", gomod.asked, npm.asked)
	}
	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	if len(errs) != 0 || len(changes) != 2 {
		t.Fatalf("Build = %v, %v", changes, errs)
	}
	for _, c := range changes {
		var want string
		switch c.Path {
		case ".tool-versions":
			want = "golangci-lint 2.6\nkubectl v1.34\nprotoc 31\nhelm 3.17.0\n"
		case "mise.toml":
			want = "[tools]\n\"go:golang.org/x/tools/gopls\" = \"0.20\"\npnpm = \"10\"\n"
		}
		if string(c.New) != want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.Path, c.New, want)
		}
	}
}
