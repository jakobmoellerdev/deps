package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/homebrew"
)

// artifactChecksums returns the checksum of the artifact a dep's template names at a version
// (missing = error, as a release without the asset).
type artifactChecksums map[string]string

func (c artifactChecksums) Checksum(_ context.Context, d dep.Dep, v, _ string) (string, error) {
	u := dep.ExpandVersion(d.ChecksumSource.Artifact, v)
	if s, ok := c[u]; ok {
		return s, nil
	}
	return "", errors.New("download " + u + ": 404 Not Found")
}

// TestResolveTogether: the per-platform downloads of a formula and its version statement move
// only as a whole. When a release lacks the assets (renamed), the version statement, which has
// no checksum of its own, is not updated alone; when one platform's asset is missing, the
// others are not either; when all are there, all move in one group.
func TestResolveTogether(t *testing.T) {
	const (
		rel  = "https://github.com/o/deps/releases/download/v"
		mac  = "/deps_darwin_arm64.tar.gz"
		lin  = "/deps_linux_amd64.tar.gz"
		file = "Formula/deps.rb"
	)
	h1, h2 := strings.Repeat("a", 64), strings.Repeat("b", 64)
	formula := "class Deps < Formula\n  version \"0.6.0\"\n" +
		"  on_macos do\n    url \"" + rel + "0.6.0" + mac + "\"\n    sha256 \"" + h1 + "\"\n  end\n" +
		"  on_linux do\n    url \"" + rel + "0.6.0" + lin + "\"\n    sha256 \"" + h1 + "\"\n  end\nend\n"
	for _, c := range []struct {
		name    string
		sums    artifactChecksums
		updates int
		err     string // the group's error; "" = none
		depErrs int    // checksum errors of the downloads
	}{
		{name: "assets renamed", sums: artifactChecksums{}, err: "1 of 3 deps of o/deps in " + file + " resolved to 0.7.0, the others did not; updated none", depErrs: 2},
		{name: "one asset missing", sums: artifactChecksums{rel + "0.7.0" + lin: h2}, err: "2 of 3 deps of o/deps in " + file + " resolved to 0.7.0, the others did not; updated none", depErrs: 1},
		{name: "all assets", sums: artifactChecksums{rel + "0.7.0" + lin: h2, rel + "0.7.0" + mac: h2}, updates: 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, cfg := setup(t, "", map[string]string{file: formula})
			o := Options{Managers: []manager.Manager{homebrew.New()}, Checksums: c.sums,
				Datasources: map[string]Datasource{"github-releases": fakeReleases{"v0.7.0", "v0.6.0"}}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Deps) != 3 || len(res.Skipped) != 0 {
				t.Fatalf("deps = %+v, skipped = %+v", res.Deps, res.Skipped)
			}
			var group []string
			depErrs := 0
			for _, e := range res.Errors {
				var de *DepError
				switch {
				case !errors.As(e, &de):
					t.Errorf("error %v is no DepError", e)
				case strings.Contains(e.Error(), "resolved to"):
					group = append(group, e.Error())
					if de.Line != 4 || de.Name != "o/deps" {
						t.Errorf("group error at %s:%d %s, want the first download", de.File, de.Line, de.Name)
					}
				default:
					depErrs++
				}
			}
			if c.err == "" && len(group) != 0 || c.err != "" && (len(group) != 1 || !strings.HasSuffix(group[0], c.err)) {
				t.Errorf("group errors = %q, want %q", group, c.err)
			}
			if depErrs != c.depErrs {
				t.Errorf("%d dep errors, want %d: %v", depErrs, c.depErrs, res.Errors)
			}
			if len(res.Updates) != c.updates {
				t.Fatalf("updates = %+v, want %d", res.Updates, c.updates)
			}
			if c.updates == 0 {
				return
			}
			gs := GroupUpdates(res.Updates, "")
			if len(gs) != 1 || len(gs[0].Updates) != 3 {
				t.Fatalf("groups = %+v, want one of 3 updates", gs)
			}
			for _, u := range gs[0].Updates {
				if u.NewVersion != "0.7.0" {
					t.Errorf("update %+v, want 0.7.0", u)
				}
			}
		})
	}
}
