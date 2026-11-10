package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bazel"
)

// Bazel module deps: bazel_dep versions, a git_override commit tracking the default branch
// (digest-only), a git_repository tag with its commit, and an oci.pull tag with its digest.
func TestBazelModule(t *testing.T) {
	const (
		remote = "https://github.com/acme/rules_x"
		old    = "0000000000000000000000000000000000000000"
		head   = "1111111111111111111111111111111111111111"
	)
	nginxOld := imageDigest("nginx", "1.27.1")
	module := `bazel_dep(name = "rules_go", version = "0.62.0")
bazel_dep(name = "rules_x")
git_override(module_name = "rules_x", remote = "` + remote + `.git", commit = "` + old + `")
git = use_repo_rule("@bazel_tools//tools/build_defs/repo:git.bzl", "git_repository")
git(name = "lib", remote = "https://github.com/acme/lib", tag = "v1.0.0", commit = "` + sha("https://github.com/acme/lib@v1.0.0") + `")
oci = use_extension("@rules_oci//oci:extensions.bzl", "oci")
oci.pull(name = "nginx", image = "nginx", tag = "1.27.1", digest = "` + nginxOld + `")
`
	ms := []manager.Manager{bazel.New(), bazel.NewBazelisk()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{
		"bazel":           fakeReleases{"0.61.0", "0.62.0", "0.63.0", "0.64.0-rc1", "1.0.0"},
		"git-refs":        fakeBranches{remote + "#HEAD": head},
		"git-tags":        fakeTags{"https://github.com/acme/lib": {"v1.0.0", "v1.1.0"}},
		"docker":          fakeDocker{"nginx": {"1.27.1", "1.28.0"}},
		"github-releases": fakeReleases{"9.3.1", "9.4.0"},
	}}
	root, cfg := setup(t, "separate: none\n", map[string]string{"MODULE.bazel": module, ".bazelversion": "9.3.1\n"})
	res, err := Resolve(t.Context(), root, cfg, env, opts)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	type up struct {
		name, version, digest string
		kind                  dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.NewDigest, u.Type})
	}
	want := []up{ // files in path order: .bazelversion first
		{"bazelbuild/bazel", "9.4.0", "", dep.Minor},
		{"rules_go", "1.0.0", "", dep.Major},
		{"rules_x", "HEAD", head, dep.Digest},
		{"acme/lib", "v1.1.0", sha("https://github.com/acme/lib@v1.1.0"), dep.Minor},
		{"nginx", "1.28.0", imageDigest("nginx", "1.28.0"), dep.Minor},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v\nwant      %+v", got, want)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Skip != "overridden by git_override" {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	if len(errs) > 0 || len(changes) != 2 {
		t.Fatalf("changes = %+v, errs = %v", changes, errs)
	}
	out := string(changes[1].New)
	for _, s := range []string{`version = "1.0.0"`, `commit = "` + head + `"`, `tag = "v1.1.0"`, `tag = "1.28.0", digest = "` + imageDigest("nginx", "1.28.0") + `"`} {
		if !strings.Contains(out, s) {
			t.Errorf("MODULE.bazel lacks %s:\n%s", s, out)
		}
	}
	if string(changes[0].New) != "9.4.0\n" {
		t.Errorf(".bazelversion = %q", changes[0].New)
	}
}
