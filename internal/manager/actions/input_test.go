package actions

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

func TestVersionInputBumpedAlong(t *testing.T) {
	const sha = "1111111111111111111111111111111111111111"
	const sha2 = "2222222222222222222222222222222222222222"
	for _, tt := range []struct {
		name, src, want string
		u               dep.Update
	}{
		{
			name: "pinned digest with comment",
			src:  "steps:\n  - uses: jakobmoellerdev/deps@" + sha + " # v0.2.5\n    with:\n      version: v0.2.5\n",
			want: "steps:\n  - uses: jakobmoellerdev/deps@" + sha2 + " # v0.3.0\n    with:\n      version: v0.3.0\n",
			u:    dep.Update{NewVersion: "v0.3.0", NewDigest: sha2},
		},
		{
			name: "tag ref",
			src:  "steps:\n  - uses: o/a@v1.2.0\n    with:\n      args: plan\n      version: v1.2.0 # keep\n",
			want: "steps:\n  - uses: o/a@v1.3.0\n    with:\n      args: plan\n      version: v1.3.0 # keep\n",
			u:    dep.Update{NewVersion: "v1.3.0"},
		},
		{
			name: "quoted values",
			src:  "steps:\n  - uses: 'o/a@v1.2.0'\n    with: {version: \"v1.2.0\"}\n",
			want: "steps:\n  - uses: 'o/a@v1.3.0'\n    with: {version: \"v1.3.0\"}\n",
			u:    dep.Update{NewVersion: "v1.3.0"},
		},
		{
			name: "tag to digest",
			src:  "steps:\n  - uses: o/a@v1.2.0\n    with:\n      version: 'v1.2.0'\n",
			want: "steps:\n  - uses: o/a@" + sha2 + " # v1.3.0\n    with:\n      version: 'v1.3.0'\n",
			u:    dep.Update{NewVersion: "v1.3.0", NewDigest: sha2},
		},
		{
			name: "mismatching version untouched",
			src:  "steps:\n  - uses: o/a@v1.2.0\n    with:\n      version: v1.1.0\n",
			want: "steps:\n  - uses: o/a@v1.3.0\n    with:\n      version: v1.1.0\n",
			u:    dep.Update{NewVersion: "v1.3.0"},
		},
		{
			name: "digest refresh keeps input",
			src:  "steps:\n  - uses: o/a@" + sha + " # v1\n    with:\n      version: v1\n",
			want: "steps:\n  - uses: o/a@" + sha2 + " # v1\n    with:\n      version: v1\n",
			u:    dep.Update{NewVersion: "v1", NewDigest: sha2},
		},
		{
			name: "other input name untouched",
			src:  "steps:\n  - uses: o/a@v1.2.0\n    with:\n      tool-version: v1.2.0\n",
			want: "steps:\n  - uses: o/a@v1.3.0\n    with:\n      tool-version: v1.2.0\n",
			u:    dep.Update{NewVersion: "v1.3.0"},
		},
		{
			name: "marker hands input to custom manager",
			src:  "steps:\n  - uses: o/a@v1.2.0\n    with:\n      # deps: git-tags o/a\n      version: v1.2.0\n",
			want: "steps:\n  - uses: o/a@v1.3.0\n    with:\n      # deps: git-tags o/a\n      version: v1.2.0\n",
			u:    dep.Update{NewVersion: "v1.3.0"},
		},
		{
			name: "setup action owns its version input",
			src:  "steps:\n  - uses: azure/setup-helm@v4\n    with:\n      version: v4\n",
			want: "steps:\n  - uses: azure/setup-helm@v5\n    with:\n      version: v4\n",
			u:    dep.Update{NewVersion: "v5"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps, err := New().Extract(".github/workflows/ci.yml", []byte(tt.src), env)
			if err != nil {
				t.Fatal(err)
			}
			tt.u.Dep = deps[0]
			edits, err := New().Edits(tt.u)
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply([]byte(tt.src), edits)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tt.want {
				t.Errorf("got\n%s\nwant\n%s", out, tt.want)
			}
		})
	}
}
