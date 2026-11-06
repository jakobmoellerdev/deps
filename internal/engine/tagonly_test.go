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
				"FROM alpine:latest@" + imageDigest("alpine", "latest") + "\n" +
				"FROM busybox\n" + // untagged: the implicit latest
				"FROM nginx@" + zero + "\n", // digest only
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
			updates: map[string]dep.UpdateType{"ubuntu": dep.Digest, controller: dep.Digest, openscap: dep.Digest, "nginx": dep.Digest},
			skipped: map[string]string{"debian": "non-version tag", grafana: "non-version tag", "busybox": "non-version tag"},
			lines: map[string]string{
				"Dockerfile": "FROM ubuntu:latest@" + imageDigest("ubuntu", "latest") + "\n" +
					"FROM debian:bookworm\n" +
					"FROM " + grafana + ":" + commit + "\n" +
					"FROM alpine:latest@" + imageDigest("alpine", "latest") + "\n" +
					"FROM busybox\n" +
					"FROM nginx@" + imageDigest("nginx", "latest") + "\n",
				"chart/values.yaml": "manager:\n  image:\n    repository: " + controller + "\n    tag: latest@" + imageDigest(controller, "latest") + "\n",
				".env":              "# renovate: datasource=docker depName=" + openscap + "\nOPENSCAP_IMAGE=" + openscap + ":latest@" + imageDigest(openscap, "latest") + "\n",
			},
		},
		{
			name:    "pin: digest pins the non-version tag",
			cfg:     customCfg + "pin: digest\n",
			updates: map[string]dep.UpdateType{"ubuntu": dep.Digest, "debian": dep.Pin, grafana: dep.Pin, controller: dep.Digest, openscap: dep.Digest, "busybox": dep.Pin, "nginx": dep.Digest},
			lines: map[string]string{
				"Dockerfile": "FROM ubuntu:latest@" + imageDigest("ubuntu", "latest") + "\n" +
					"FROM debian:bookworm@" + imageDigest("debian", "bookworm") + "\n" +
					"FROM " + grafana + ":" + commit + "@" + imageDigest(grafana, commit) + "\n" +
					"FROM alpine:latest@" + imageDigest("alpine", "latest") + "\n" +
					"FROM busybox@" + imageDigest("busybox", "latest") + "\n" +
					"FROM nginx@" + imageDigest("nginx", "latest") + "\n",
			},
		},
		{
			name:    "digest updates disabled",
			cfg:     customCfg + "rules: [{allowedUpdateTypes: [major, minor, patch]}]\n",
			updates: map[string]dep.UpdateType{},
			skipped: map[string]string{"debian": "non-version tag", grafana: "non-version tag", "busybox": "non-version tag"},
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

// vTags lists tags v7.0.6 and v7.1.0 with their commits, like git-tags, and fails Digest
// like git-tags does for a name that is not a tag.
type vTags struct{}

func (vTags) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return []Release{{Version: "v7.0.6", Digest: "aaaa"}, {Version: "v7.1.0", Digest: "bbbb"}}, nil
}

func (vTags) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	return "", errors.New("tag " + v + " not found upstream")
}

// TestDigestOfVersionWithoutV checks that a SHA pin commented `# 7.1.0` (grafana's
// peter-evans/create-pull-request) refreshes against the commit of tag v7.1.0: the digest
// lookup matches the tag with a leading "v" added or removed.
func TestDigestOfVersionWithoutV(t *testing.T) {
	pin := func(version, digest string) []dep.Dep {
		return []dep.Dep{{Datasource: "git-tags", Versioning: "tag", Name: "o/r", Version: version, Digest: digest, Line: 1}}
	}
	for _, tc := range []struct {
		name, version, digest string
		want                  []string // type version digest
	}{
		{"current", "7.1.0", "bbbb", nil},
		{"stale digest", "7.1.0", "cccc", []string{"digest 7.1.0 bbbb"}},
	} {
		res, _ := resolveStub(t, "", "github-actions", pin(tc.version, tc.digest), vTags{}, nil)
		var got []string
		for _, u := range res.Updates {
			got = append(got, string(u.Type)+" "+u.NewVersion+" "+u.NewDigest)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: updates = %q, want %q", tc.name, got, tc.want)
		}
	}
}
