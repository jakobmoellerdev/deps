package engine

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/composer"
)

const composerJSON = `{
    "require": {
        "php": "^8.2",
        "monolog/monolog": "^2.9",
        "psr/log": "^1.1|^2.0",
        "guzzlehttp/guzzle": "7.8.0",
        "symfony/console": "*"
    },
    "require-dev": {
        "phpunit/phpunit": "~10.5"
    }
}
`

const composerLock = `{
    "content-hash": "x",
    "packages": [
        {"name": "guzzlehttp/guzzle", "version": "7.8.0"},
        {"name": "monolog/monolog", "version": "2.9.1"},
        {"name": "psr/log", "version": "2.0.0"},
        {"name": "symfony/console", "version": "v6.4.1"}
    ],
    "packages-dev": [
        {"name": "phpunit/phpunit", "version": "10.5.2"}
    ]
}
`

var composerReleases = fakeGo{
	"monolog/monolog":   {"2.9.1", "2.10.0", "3.8.1"},
	"psr/log":           {"1.1.4", "2.0.0", "3.0.2"},
	"guzzlehttp/guzzle": {"7.8.0", "7.9.2"},
	"symfony/console":   {"v6.4.1", "v6.4.9", "v7.1.0"},
	"phpunit/phpunit":   {"10.5.2", "10.5.38", "11.4.0"},
}

// TestComposerRangeStrategy resolves a composer.json with its composer.lock under each
// rangeStrategy and builds the manifest changes: auto is update-lockfile, widen for a
// constraint with alternatives.
func TestComposerRangeStrategy(t *testing.T) {
	for _, tt := range []struct {
		name, cfg string
		want      []string
		lines     []string // the changed constraint lines
	}{
		{
			name: "auto",
			want: []string{
				"guzzlehttp/guzzle 7.8.0->7.9.2 minor",
				"monolog/monolog 2.9->3.8.1 major", "monolog/monolog 2.9.1->2.10.0 minor lockfile",
				"phpunit/phpunit 10.5->11.4.0 major", "phpunit/phpunit 10.5.2->10.5.38 patch lockfile",
				"psr/log 2.0->3.0.2 major", // widened; 2.x versions are left alone
				"symfony/console v6.4.1->v6.4.9 patch lockfile", "symfony/console v6.4.1->v7.1.0 major lockfile",
			},
			lines: []string{`"monolog/monolog": "^3.0",`, `"psr/log": "^1.1|^2.0|^3.0",`, `"guzzlehttp/guzzle": "7.9.2",`, `"phpunit/phpunit": "~11.0"`},
		},
		{
			name: "bump", cfg: "rangeStrategy: bump\n",
			want: []string{
				"guzzlehttp/guzzle 7.8.0->7.9.2 minor",
				"monolog/monolog 2.9->2.10.0 minor", "monolog/monolog 2.9->3.8.1 major",
				"phpunit/phpunit 10.5->11.4.0 major", // ~10.5 cannot be raised for 10.5.38
				"psr/log 2.0->3.0.2 major",           // bump widens alternatives
				"symfony/console v6.4.1->v6.4.9 patch lockfile", "symfony/console v6.4.1->v7.1.0 major lockfile",
			},
			lines: []string{`"monolog/monolog": "^2.10",`, `"psr/log": "^1.1|^2.0|^3.0",`, `"guzzlehttp/guzzle": "7.9.2",`, `"phpunit/phpunit": "~11.0"`},
		},
		{
			name: "pin", cfg: "rangeStrategy: pin\n",
			want: []string{
				"guzzlehttp/guzzle 7.8.0->7.9.2 minor",
				"monolog/monolog 2.9->2.10.0 minor", "monolog/monolog 2.9->3.8.1 major",
				"phpunit/phpunit 10.5->10.5.38 patch", "phpunit/phpunit 10.5->11.4.0 major",
				"psr/log 2.0->3.0.2 major", "psr/log ^1.1|^2.0->2.0.0 pin",
				"symfony/console v6.4.1->v6.4.9 patch", "symfony/console v6.4.1->v7.1.0 major",
			},
			lines: []string{`"monolog/monolog": "2.10.0",`, `"psr/log": "2.0.0",`, `"guzzlehttp/guzzle": "7.9.2",`, `"symfony/console": "6.4.9"`, `"phpunit/phpunit": "10.5.38"`},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, map[string]string{"app/composer.json": composerJSON, "app/composer.lock": composerLock})
			o := Options{Managers: []manager.Manager{composer.New()}, Datasources: map[string]Datasource{composer.Datasource: composerReleases}}
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
			local, _ := Local(updates) // one version per dep, as plan writes them
			changes, errs := Build(t.Context(), root, o.Managers, local, nil)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			files := maps.Clone(map[string]string{"app/composer.json": composerJSON})
			for _, c := range changes {
				files[c.Path] = string(c.New)
			}
			orig := map[string]bool{}
			for l := range strings.SplitSeq(composerJSON, "\n") {
				orig[l] = true
			}
			var lines []string
			for l := range strings.SplitSeq(files["app/composer.json"], "\n") {
				if !orig[l] {
					lines = append(lines, strings.TrimSpace(l))
				}
			}
			if strings.Join(lines, "\n") != strings.Join(tt.lines, "\n") {
				t.Errorf("composer.json changes\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(tt.lines, "\n"))
			}
		})
	}
}
