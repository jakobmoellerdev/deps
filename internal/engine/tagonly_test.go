package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// digestOnlyDocker serves digests (imageDigest) but fails release listings: deps with a
// non-version tag must be resolved without them.
type digestOnlyDocker struct{}

func (digestOnlyDocker) Releases(_ context.Context, d dep.Dep, _ string) ([]Release, error) {
	return nil, errors.New("releases listed for " + d.Name)
}

func (digestOnlyDocker) Digest(_ context.Context, d dep.Dep, v string) (string, error) {
	return imageDigest(d.Name, v), nil
}

func TestNonVersionTagDigests(t *testing.T) {
	const (
		zero       = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		controller = "ghcr.io/open-component-model/kubernetes/controller"
		openscap   = "cgr.dev/chainguard/openscap"
		grafana    = "grafana/grafana-dev"
		commit     = "3a22eba17f23b18faa27436ab2f9c3ea977b550b" // a commit hash, not version 3
	)
	files := func() map[string]string {
		return map[string]string{
			"Dockerfile": "FROM ubuntu:latest@" + zero + "\n" +
				"FROM debian:bookworm\n" +
				"FROM " + grafana + ":" + commit + "\n" +
				"FROM alpine:latest@" + imageDigest("alpine", "latest") + "\n",
			"chart/values.yaml": "manager:\n  image:\n    repository: " + controller + "\n    tag: latest@" + zero + "\n",
			".env":              "# renovate: datasource=docker depName=" + openscap + "\nOPENSCAP_IMAGE=" + openscap + ":latest@" + zero + "\n",
		}
	}
	const customCfg = "custom: [{files: ['**/.env']}]\n"
	tests := []struct {
		name    string
		cfg     string
		updates map[string]dep.UpdateType // dep name -> type
		skipped map[string]string         // dep name -> skip reason
		lines   map[string]string         // file -> expected content
	}{
		{
			name:    "refresh pinned, skip unpinned",
			cfg:     customCfg,
			updates: map[string]dep.UpdateType{"ubuntu": dep.Digest, controller: dep.Digest, openscap: dep.Digest},
			skipped: map[string]string{"debian": "non-version tag", grafana: "non-version tag"},
			lines: map[string]string{
				"Dockerfile": "FROM ubuntu:latest@" + imageDigest("ubuntu", "latest") + "\n" +
					"FROM debian:bookworm\n" +
					"FROM " + grafana + ":" + commit + "\n" +
					"FROM alpine:latest@" + imageDigest("alpine", "latest") + "\n",
				"chart/values.yaml": "manager:\n  image:\n    repository: " + controller + "\n    tag: latest@" + imageDigest(controller, "latest") + "\n",
				".env":              "# renovate: datasource=docker depName=" + openscap + "\nOPENSCAP_IMAGE=" + openscap + ":latest@" + imageDigest(openscap, "latest") + "\n",
			},
		},
		{
			name:    "pin: digest pins the non-version tag",
			cfg:     customCfg + "pin: digest\n",
			updates: map[string]dep.UpdateType{"ubuntu": dep.Digest, "debian": dep.Pin, grafana: dep.Pin, controller: dep.Digest, openscap: dep.Digest},
			lines: map[string]string{
				"Dockerfile": "FROM ubuntu:latest@" + imageDigest("ubuntu", "latest") + "\n" +
					"FROM debian:bookworm@" + imageDigest("debian", "bookworm") + "\n" +
					"FROM " + grafana + ":" + commit + "@" + imageDigest(grafana, commit) + "\n" +
					"FROM alpine:latest@" + imageDigest("alpine", "latest") + "\n",
			},
		},
		{
			name:    "digest updates disabled",
			cfg:     customCfg + "rules: [{updates: [major, minor, patch]}]\n",
			updates: map[string]dep.UpdateType{},
			skipped: map[string]string{"debian": "non-version tag", grafana: "non-version tag"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, cfg := setup(t, tc.cfg, files())
			ms, err := Managers(cfg)
			if err != nil {
				t.Fatal(err)
			}
			sec := &fakeSec{}
			res, err := Resolve(t.Context(), root, cfg, env, Options{
				Managers:    ms,
				Datasources: map[string]Datasource{"docker": digestOnlyDocker{}},
				Security:    sec,
			})
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("err = %v, errors = %v", err, res.Errors)
			}
			got := map[string]dep.UpdateType{}
			for _, u := range res.Updates {
				got[u.Dep.Name] = u.Type
				if u.NewVersion != u.Dep.Version {
					t.Errorf("%s: version changed to %s", u.Dep.Name, u.NewVersion)
				}
			}
			if len(got) != len(tc.updates) {
				t.Errorf("updates = %v, want %v", got, tc.updates)
			}
			for n, k := range tc.updates {
				if got[n] != k {
					t.Errorf("%s: update type %q, want %q", n, got[n], k)
				}
			}
			skips := map[string]string{}
			for _, d := range res.Skipped {
				skips[d.Name] = d.Skip
			}
			if len(skips) != len(tc.skipped) {
				t.Errorf("skipped = %v, want %v", skips, tc.skipped)
			}
			for n, s := range tc.skipped {
				if skips[n] != s {
					t.Errorf("%s: skip %q, want %q", n, skips[n], s)
				}
			}
			if len(sec.asked) > 0 {
				t.Errorf("advisories asked for non-version tags: %v", sec.asked)
			}
			changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			for _, c := range changes {
				if want, ok := tc.lines[c.Path]; ok && string(c.New) != want {
					t.Errorf("%s:\n%s\nwant:\n%s", c.Path, c.New, want)
				}
			}
			var paths []string
			for _, c := range changes {
				paths = append(paths, c.Path)
			}
			for p := range tc.lines {
				if !slices.Contains(paths, p) {
					t.Errorf("no change for %s (changes: %s)", p, strings.Join(paths, ", "))
				}
			}
		})
	}
}
