package engine

import (
	"context"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// headRefs is git-refs serving one default branch head.
type headRefs struct{ head string }

func (headRefs) Releases(context.Context, dep.Dep, string) ([]Release, error) { return nil, nil }

func (r headRefs) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	if v != "HEAD" {
		return "", nil
	}
	return r.head, nil
}

// latestDocker is digestOnlyDocker listing only tag latest.
type latestDocker struct{ digestOnlyDocker }

func (latestDocker) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return []Release{{Version: "latest"}}, nil
}

// TestKustomizePinsAndRefs checks the kustomize deps without a version tag: `pin: digest`
// appends the digest to a newTag, a `digest` field without newTag is refreshed against
// `latest`, a rename-only image is never pinned but replaced, and a commit ref follows the
// default branch head.
func TestKustomizePinsAndRefs(t *testing.T) {
	const (
		old  = "0123456789abcdef0123456789abcdef01234567"
		head = "fedcba9876543210fedcba9876543210fedcba98"
	)
	src := "resources:\n" +
		"  - https://github.com/org/infra//apps?ref=" + old + "\n" +
		"  - https://github.com/org/infra//apps?ref=main\n" +
		"images:\n" +
		"  - name: nginx\n    newTag: mainline\n" +
		"  - name: busybox\n    digest: " + imageDigest("x", "y") + "\n" +
		"  - name: app\n    newName: example.com/renamed\n"
	tests := []struct {
		name    string
		cfg     string
		updates map[string]dep.UpdateType
		skipped map[string]string
		want    string
	}{
		{
			name:    "refresh only",
			updates: map[string]dep.UpdateType{"busybox": dep.Digest, "github.com/org/infra": dep.Digest},
			skipped: map[string]string{"nginx": "non-version tag", "example.com/renamed": "non-version tag", "github.com/org/infra": "non-version ref"},
			want: "resources:\n" +
				"  - https://github.com/org/infra//apps?ref=" + head + "\n" +
				"  - https://github.com/org/infra//apps?ref=main\n" +
				"images:\n" +
				"  - name: nginx\n    newTag: mainline\n" +
				"  - name: busybox\n    digest: " + imageDigest("busybox", "latest") + "\n" +
				"  - name: app\n    newName: example.com/renamed\n",
		},
		{
			name:    "pin: digest",
			cfg:     "pin: digest\n",
			updates: map[string]dep.UpdateType{"nginx": dep.Pin, "busybox": dep.Digest, "github.com/org/infra": dep.Digest},
			skipped: map[string]string{"example.com/renamed": "non-version tag", "github.com/org/infra": "non-version ref"},
			want: "resources:\n" +
				"  - https://github.com/org/infra//apps?ref=" + head + "\n" +
				"  - https://github.com/org/infra//apps?ref=main\n" +
				"images:\n" +
				"  - name: nginx\n    newTag: mainline@" + imageDigest("nginx", "mainline") + "\n" +
				"  - name: busybox\n    digest: " + imageDigest("busybox", "latest") + "\n" +
				"  - name: app\n    newName: example.com/renamed\n",
		},
		{
			name:    "rename-only image replaced",
			cfg:     "rules: [{names: [example.com/renamed], replacementName: example.com/other}]\n",
			updates: map[string]dep.UpdateType{"busybox": dep.Digest, "github.com/org/infra": dep.Digest, "example.com/renamed": dep.Replacement},
			skipped: map[string]string{"nginx": "non-version tag"},
			want: "resources:\n" +
				"  - https://github.com/org/infra//apps?ref=" + head + "\n" +
				"  - https://github.com/org/infra//apps?ref=main\n" +
				"images:\n" +
				"  - name: nginx\n    newTag: mainline\n" +
				"  - name: busybox\n    digest: " + imageDigest("busybox", "latest") + "\n" +
				"  - name: app\n    newName: example.com/other\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, cfg := setup(t, tc.cfg, map[string]string{"kustomization.yaml": src})
			ms, err := Managers(cfg)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Resolve(t.Context(), root, cfg, env, Options{
				Managers:    ms,
				Datasources: map[string]Datasource{"docker": latestDocker{}, "git-refs": headRefs{head}},
				Security:    &fakeSec{},
			})
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("err = %v, errors = %v", err, res.Errors)
			}
			got := map[string]dep.UpdateType{}
			for _, u := range res.Updates {
				got[u.Dep.Name] = u.Type
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
			for n, s := range tc.skipped {
				if skips[n] != s {
					t.Errorf("%s: skip %q, want %q (skipped %v)", n, skips[n], s, skips)
				}
			}
			changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
			if len(errs) > 0 || len(changes) != 1 {
				t.Fatalf("changes %d, errors %v", len(changes), errs)
			}
			if string(changes[0].New) != tc.want {
				t.Errorf("got\n%s\nwant\n%s", changes[0].New, tc.want)
			}
		})
	}
}
