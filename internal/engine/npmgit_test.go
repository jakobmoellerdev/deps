package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
)

// npm git dependencies on a tag get tag updates (never digest-pinned); those on a commit, also
// abbreviated as argo-cd's argo-ui, digest updates of the default branch head written at the
// length of the pin; an abbreviated pin the head starts with is current. A packageManager
// Corepack hash moves with its version.
func TestNpmGitDependencies(t *testing.T) {
	const (
		argoUI, lib, same = "https://github.com/argoproj/argo-ui", "https://github.com/acme/lib", "https://gitlab.com/acme/same"
		head              = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		sameHead          = "abcdef1234567890abcdef1234567890abcdef12"
	)
	h1, h2 := strings.Repeat("a", 128), strings.Repeat("b", 128)
	pkg := `{
  "packageManager": "pnpm@12.15.0+sha512.` + h1 + `",
  "dependencies": {
    "argo-ui": "git+https://github.com/argoproj/argo-ui.git#28927a5",
    "lib": "github:acme/lib#v1.0.0",
    "same": "gitlab:acme/same#abcdef1"
  }
}
`
	ms := []manager.Manager{npm.New()}
	opts := Options{Managers: ms, Checksums: &fakeChecksums{sums: map[string]string{"12.16.0": h2}}, Datasources: map[string]Datasource{
		"git-refs": fakeBranches{argoUI + "#HEAD": head, same + "#HEAD": sameHead},
		"git-tags": fakeTags{lib: {"v1.0.0", "v1.1.0"}},
		"npm":      fakeReleases{"12.15.0", "12.16.0"},
	}}
	root, cfg := setup(t, "", map[string]string{"package.json": pkg})
	res, err := Resolve(t.Context(), root, cfg, env, opts)
	if err != nil || len(res.Errors) > 0 || len(res.Skipped) > 0 {
		t.Fatalf("err = %v, errors = %v, skipped = %+v", err, res.Errors, res.Skipped)
	}
	var got []string
	for _, u := range res.Updates {
		got = append(got, u.Dep.Name+" "+string(u.Type)+" "+u.NewVersion+" "+u.NewDigest+" "+u.NewChecksum[:min(1, len(u.NewChecksum))])
	}
	want := []string{"pnpm minor 12.16.0  b", "argo-ui digest HEAD " + head + " ", "lib minor v1.1.0  "}
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %q, want %q", got, want)
	}
	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	wantPkg := strings.NewReplacer("12.15.0+sha512."+h1, "12.16.0+sha512."+h2, "#28927a5", "#1111111", "#v1.0.0", "#v1.1.0").Replace(pkg)
	if len(errs) > 0 || len(changes) != 1 || string(changes[0].New) != wantPkg {
		t.Fatalf("changes = %+v, %v", changes, errs)
	}
}

func TestSameCommit(t *testing.T) {
	for _, c := range []struct {
		c, cur string
		want   bool
	}{
		{"abcdef1234", "abcdef1234", true},
		{"ABCDEF1234", "abcdef1234", true},
		{"abcdef1234", "abcdef1", true},
		{"abcdef1234", "abcdef2", false},
		{"abcdef1234", "abcde", false}, // too short to be an abbreviated commit
		{"sha256:abc", "sha256:abd", false},
	} {
		if got := sameCommit(c.c, c.cur); got != c.want {
			t.Errorf("sameCommit(%q, %q) = %v", c.c, c.cur, got)
		}
	}
}
