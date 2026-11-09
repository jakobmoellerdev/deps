package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/deno"
)

const denoJSON = `{
  // deno.jsonc
  "imports": {
    "@std/path": "jsr:@std/path@^1.0.0",
    "chalk": "npm:chalk@5.3.0",
    "oak": "https://deno.land/x/oak@v12.6.1/mod.ts",
  },
  "tasks": { "vite": "deno run -A npm:create-vite@^6.0.0" },
}
`

// denoLock locks @std/path, written `^1.0.0` in deno.jsonc, as deno writes it: `1`.
const denoLock = `{
  "version": "5",
  "specifiers": { "jsr:@std/path@1": "1.0.8", "npm:chalk@5.3.0": "5.3.0" },
  "workspace": { "dependencies": ["jsr:@std/path@1", "npm:chalk@5.3.0"] }
}
`

// TestDenoRangeStrategy resolves a deno.jsonc with its deno.lock under rangeStrategy auto
// (update-lockfile, as for npm), bump and widen, and builds the configuration changes.
func TestDenoRangeStrategy(t *testing.T) {
	releases := fakeGo{
		"@std/path":               {"1.0.0", "1.0.8", "1.1.0", "2.0.0"},
		"chalk":                   {"5.3.0", "5.4.0"},
		"create-vite":             {"6.0.0", "6.1.0", "7.0.0"},
		"https://deno.land/x/oak": {"v12.6.1", "14.2.0", "v17.2.0"},
	}
	for _, tt := range []struct {
		name, cfg string
		want      []string
		lines     []string // the changed lines
	}{
		{
			name: "auto",
			want: []string{
				"@std/path 1.0.0->2.0.0 major", "@std/path 1.0.8->1.1.0 minor lockfile", "chalk 5.3.0->5.4.0 minor",
				"create-vite 6.0.0->7.0.0 major", "https://deno.land/x/oak v12.6.1->v17.2.0 major",
			},
			lines: []string{`"@std/path": "jsr:@std/path@^2.0.0",`, `"chalk": "npm:chalk@5.4.0",`, `"oak": "https://deno.land/x/oak@v17.2.0/mod.ts",`, `"tasks": { "vite": "deno run -A npm:create-vite@^7.0.0" },`},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n",
			want: []string{
				"@std/path 1.0.0->1.1.0 minor", "@std/path 1.0.0->2.0.0 major", "chalk 5.3.0->5.4.0 minor",
				"create-vite 6.0.0->6.1.0 minor", "create-vite 6.0.0->7.0.0 major", "https://deno.land/x/oak v12.6.1->v17.2.0 major",
			},
			lines: []string{`"@std/path": "jsr:@std/path@^1.1.0",`, `"chalk": "npm:chalk@5.4.0",`, `"oak": "https://deno.land/x/oak@v17.2.0/mod.ts",`, `"tasks": { "vite": "deno run -A npm:create-vite@^6.1.0" },`},
		},
		{
			// A specifier takes no `||`: widen replaces, as Renovate's deno versioning.
			name: "widen", cfg: "rangeStrategy: widen\n",
			want: []string{
				"@std/path 1.0.0->2.0.0 major", "chalk 5.3.0->5.4.0 minor", // in-range versions are left alone
				"create-vite 6.0.0->7.0.0 major", "https://deno.land/x/oak v12.6.1->v17.2.0 major",
			},
			lines: []string{`"@std/path": "jsr:@std/path@^2.0.0",`, `"chalk": "npm:chalk@5.4.0",`, `"oak": "https://deno.land/x/oak@v17.2.0/mod.ts",`, `"tasks": { "vite": "deno run -A npm:create-vite@^7.0.0" },`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"app/deno.jsonc": denoJSON, "app/deno.lock": denoLock})
			o := Options{Managers: []manager.Manager{deno.New()}, Datasources: map[string]Datasource{"jsr": releases, "npm": releases, "deno": releases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			var updates []dep.Update
			for _, u := range res.Updates {
				if !u.Lockfile {
					updates = append(updates, u)
				}
			}
			local, _ := Local(updates)
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			orig := map[string]bool{}
			for l := range strings.SplitSeq(denoJSON, "\n") {
				orig[l] = true
			}
			var lines []string
			for _, c := range changes {
				for l := range strings.SplitSeq(string(c.New), "\n") {
					if !orig[l] {
						lines = append(lines, strings.TrimSpace(l))
					}
				}
			}
			if strings.Join(lines, "\n") != strings.Join(tt.lines, "\n") {
				t.Errorf("deno.jsonc changes\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(tt.lines, "\n"))
			}
		})
	}
}
