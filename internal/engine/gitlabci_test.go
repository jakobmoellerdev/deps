package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

const (
	glOld  = "1111111111111111111111111111111111111111"
	glNew  = "2222222222222222222222222222222222222222"
	glMain = "3333333333333333333333333333333333333333"
)

// glTags is git-tags and git-refs for GitLab include tests: tags v1.0.0 and v1.1.0, branch main.
type glTags struct{}

func (glTags) Releases(context.Context, dep.Dep, string) ([]Release, error) {
	return []Release{{Version: "v1.0.0", Digest: glOld}, {Version: "v1.1.0", Digest: glNew}}, nil
}

func (glTags) Digest(_ context.Context, _ dep.Dep, v string) (string, error) {
	switch v {
	case "v1.0.0":
		return glOld, nil
	case "v1.1.0":
		return glNew, nil
	case "main":
		return glMain, nil
	}
	return "", errors.New(v + " not found")
}

func TestGitLabIncludeDigests(t *testing.T) {
	const src = `include:
  - project: group/tag
    ref: v1.0.0
  - project: group/branch
    ref: main
  - component: gitlab.com/group/comp/build@v1.0.0
  - remote: https://gitlab.com/group/remote/-/raw/v1.0.0/ci.yml
  - project: group/pinned
    ref: ` + glOld + ` # v1.0.0
  - project: group/pinnedbranch
    ref: ` + glOld + ` # main
`
	tests := []struct {
		name, cfg, want string
		skipped         map[string]string
	}{
		{
			name: "default",
			want: `include:
  - project: group/tag
    ref: v1.1.0
  - project: group/branch
    ref: main
  - component: gitlab.com/group/comp/build@v1.1.0
  - remote: https://gitlab.com/group/remote/-/raw/v1.1.0/ci.yml
  - project: group/pinned
    ref: ` + glNew + ` # v1.1.0
  - project: group/pinnedbranch
    ref: ` + glMain + ` # main
`,
			skipped: map[string]string{"group/branch": "non-version tag"},
		},
		{
			name: "pin digest",
			cfg:  "pin: digest\n",
			want: `include:
  - project: group/tag
    ref: ` + glNew + ` # v1.1.0
  - project: group/branch
    ref: ` + glMain + ` # main
  - component: gitlab.com/group/comp/build@` + glNew + ` # v1.1.0
  - remote: https://gitlab.com/group/remote/-/raw/` + glNew + `/ci.yml # v1.1.0
  - project: group/pinned
    ref: ` + glNew + ` # v1.1.0
  - project: group/pinnedbranch
    ref: ` + glMain + ` # main
`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, cfg := setup(t, tc.cfg+"separateMajorMinor: false\n", map[string]string{".gitlab-ci.yml": src})
			ms, err := Managers(cfg)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Resolve(t.Context(), root, cfg, env, Options{
				Managers:    ms,
				Datasources: map[string]Datasource{"git-tags": glTags{}, "git-refs": glTags{}},
				Security:    &fakeSec{},
			})
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("err = %v, errors = %v", err, res.Errors)
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
			changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			if len(changes) != 1 {
				t.Fatalf("changes = %d, want 1", len(changes))
			}
			if got := string(changes[0].New); got != tc.want {
				t.Errorf("got\n%s\nwant\n%s\ndiff lines: %v", got, tc.want, strings.Count(got, "\n"))
			}
		})
	}
}
