package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

const day = 24 * time.Hour

func TestRuleNegatedGlobs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rule  string
		d     dep.Dep
		match bool
	}{
		{"exclusion only: other name", "names: ['!actions/*']", dep.Dep{Name: "docker/login-action"}, true},
		{"exclusion only: excluded name", "names: ['!actions/*']", dep.Dep{Name: "actions/checkout"}, false},
		{"several exclusions", "names: ['!a', '!b/**']", dep.Dep{Name: "c"}, true},
		{"several exclusions: second excludes", "names: ['!a', '!b/**']", dep.Dep{Name: "b/x/y"}, false},
		{"include and exclude: included", "names: ['actions/*', '!actions/cache']", dep.Dep{Name: "actions/checkout"}, true},
		{"include and exclude: excluded", "names: ['actions/*', '!actions/cache']", dep.Dep{Name: "actions/cache"}, false},
		{"include and exclude: not included", "names: ['actions/*', '!actions/cache']", dep.Dep{Name: "docker/x"}, false},
		{"exclusion wins in any order", "names: ['!actions/cache', '**']", dep.Dep{Name: "actions/cache"}, false},
		{"paths exclusion only", "paths: ['!test/**']", dep.Dep{File: "go.mod"}, true},
		{"paths exclusion only: excluded", "paths: ['!test/**']", dep.Dep{File: "test/e2e/go.mod"}, false},
		{"paths include and exclude", "paths: ['charts/**', '!charts/legacy/**']", dep.Dep{File: "charts/legacy/values.yaml"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := load(t, ".deps.yaml", "rules: [{"+tc.rule+", group: g}]\n")
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Policy(tc.d).Group == "g"; got != tc.match {
				t.Errorf("match = %v, want %v", got, tc.match)
			}
		})
	}
}

func TestRuleCurrentVersion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   string
		d     dep.Dep
		match bool
	}{
		{"constraint", "rules: [{currentVersion: '< 2', group: g}]", dep.Dep{Versioning: "semver", Version: "1.9.0"}, true},
		{"constraint excludes", "rules: [{currentVersion: '< 2', group: g}]", dep.Dep{Versioning: "semver", Version: "2.0.0"}, false},
		{"constraint with the locked version", "rules: [{currentVersion: '< 2', group: g}]", dep.Dep{Versioning: "semver", Version: "2.0.0", Locked: "1.4.0"}, true},
		{"constraint on a non-version", "rules: [{currentVersion: '< 2', group: g}]", dep.Dep{Versioning: "docker", Version: "latest"}, false},
		{"constraint under an unknown scheme", "rules: [{currentVersion: '< 2', group: g}]", dep.Dep{Versioning: "calver", Version: "1.0.0"}, false},
		{"regex as written", "rules: [{currentVersion: '/^v1\\./', group: g}]", dep.Dep{Versioning: "tag", Version: "v1.2.3"}, true},
		{"regex no match", "rules: [{currentVersion: '/^v1\\./', group: g}]", dep.Dep{Versioning: "tag", Version: "v2.0.0"}, false},
		{"negated regex", "rules: [{currentVersion: '!/-rc/', group: g}]", dep.Dep{Versioning: "semver", Version: "1.0.0-rc.1"}, false},
		{"negated regex other", "rules: [{currentVersion: '!/-rc/', group: g}]", dep.Dep{Versioning: "semver", Version: "1.0.0"}, true},
		// The rule's versioning (3-4 = 3.4) converts the version, not the dep's semver (3.0.0-4).
		{"scheme of a versioning rule", "rules: [{names: [x], versioning: 'regex:^(?<major>\\d+)-(?<minor>\\d+)$'}, {currentVersion: '>= 3.1', group: g}]", dep.Dep{Name: "x", Versioning: "semver", Version: "3-4"}, true},
		{"own scheme without a versioning rule", "rules: [{names: [y], versioning: 'regex:^(?<major>\\d+)-(?<minor>\\d+)$'}, {currentVersion: '>= 3.1', group: g}]", dep.Dep{Name: "x", Versioning: "semver", Version: "3-4"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := load(t, ".deps.yaml", tc.cfg+"\n")
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Policy(tc.d).Group == "g"; got != tc.match {
				t.Errorf("match = %v, want %v", got, tc.match)
			}
		})
	}
}

func TestRuleCurrentAge(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - currentAge: '> 1 year'
    group: old
  - names: [a]
    currentAge: '<= 30 days'
    group: fresh
  - currentAge: '>= 2 months'
    updateTypes: [major]
    ignore: true
`)
	if err != nil {
		t.Fatal(err)
	}
	a := dep.Dep{Name: "a"}
	for _, tc := range []struct {
		name   string
		d      dep.Dep
		age    time.Duration
		group  string
		rules  []int
		major  bool // majors ignored
		wanted bool // WantsCurrentAge
	}{
		{"unknown age", a, UnknownAge, "", nil, false, true},
		{"over a year", a, 366 * day, "old", []int{0}, true, true},
		{"exactly a year is not over", a, 365 * day, "", nil, true, true},
		{"fresh", a, 30 * day, "fresh", []int{1}, false, true},
		{"two months", a, 60 * day, "", nil, true, true},
		{"fresh name rule does not match b", dep.Dep{Name: "b"}, 10 * day, "", nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := c.PolicyAt(tc.d, "", tc.age)
			if p.Group != tc.group || !slices.Equal(p.Rules, tc.rules) {
				t.Errorf("group %q rules %v, want %q %v", p.Group, p.Rules, tc.group, tc.rules)
			}
			if got := c.PolicyAt(tc.d, dep.Major, tc.age).Ignore; got != tc.major {
				t.Errorf("major ignored = %v, want %v", got, tc.major)
			}
			if got := c.WantsCurrentAge(tc.d); got != tc.wanted {
				t.Errorf("WantsCurrentAge = %v, want %v", got, tc.wanted)
			}
		})
	}
	if p := c.Policy(a); p.Group != "" || c.PolicyFor(a, dep.Major).Ignore {
		t.Errorf("Policy and PolicyFor match currentAge rules: %+v", p)
	}

	c, err = load(t, ".deps.yaml", "rules: [{names: [a], currentAge: '> 1 day', group: g}, {names: [b], group: h}]\n")
	if err != nil {
		t.Fatal(err)
	}
	if !c.WantsCurrentAge(a) || c.WantsCurrentAge(dep.Dep{Name: "b"}) {
		t.Error("WantsCurrentAge must follow the other selectors of currentAge rules")
	}
}

func TestParseAgeRange(t *testing.T) {
	for _, tc := range []struct {
		in, op string
		age    time.Duration
	}{
		{"> 1 year", ">", 365 * day},
		{">=2 months", ">=", 60 * day},
		{"< 30 days", "<", 30 * day},
		{" <= 12h ", "<=", 12 * time.Hour},
		{"> 3y", ">", 3 * 365 * day},
		{"< 1 month", "<", 30 * day},
		{"> 2 weeks", ">", 14 * day},
	} {
		a, err := parseAgeRange(tc.in)
		if err != nil || a.op != tc.op || a.age != tc.age {
			t.Errorf("parseAgeRange(%q) = %+v, %v; want %s %v", tc.in, a, err, tc.op, tc.age)
		}
	}
	for _, tc := range []struct {
		in              string
		below, at, over bool // matches at 9, 10 and 11 days
	}{
		{"< 10 days", true, false, false},
		{"<= 10 days", true, true, false},
		{"> 10 days", false, false, true},
		{">= 10 days", false, true, true},
	} {
		a, err := parseAgeRange(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := [3]bool{a.matches(9 * day), a.matches(10 * day), a.matches(11 * day)}; got != [3]bool{tc.below, tc.at, tc.over} {
			t.Errorf("%s at 9, 10, 11 days = %v", tc.in, got)
		}
	}
	for _, in := range []string{"1 year", "> ", "~ 1 day", "> soon", "> 99999999999 years", "= 1 day"} {
		if a, err := parseAgeRange(in); err == nil {
			t.Errorf("parseAgeRange(%q) = %+v, want an error", in, a)
		}
	}
}

func TestRuleMatcherErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"rules: [{names: ['!']}]\n", `rules[0].names[0]: invalid glob "!": missing pattern after !`},
		{"rules: [{names: [a, '![']}]\n", `rules[0].names[1]: invalid glob "!["`},
		{"rules: [{paths: ['!']}]\n", `rules[0].paths[0]: invalid glob "!": missing pattern after !`},
		{"rules: [{currentVersion: '>>1'}]\n", `rules[0].currentVersion: invalid constraint ">>1"`},
		{"rules: [{currentVersion: '!/(/'}]\n", `rules[0].currentVersion: invalid regex "!/(/"`},
		{"rules: [{currentVersion: '< 2', versioning: loose}]\n", "rules[0].currentVersion: cannot be combined with versioning"},
		{"rules: [{currentAge: old}]\n", `rules[0].currentAge: invalid age range "old": want an operator`},
		{"rules: [{currentAge: '> soon'}]\n", `rules[0].currentAge: invalid age range "> soon": invalid age "soon"`},
		{"rules: [{currentAge: '> 1 day', versioning: loose}]\n", "rules[0].currentAge: cannot be combined with versioning or extractVersion"},
		{"rules: [{currentAge: '> 1 day', extractVersion: '^v(?<version>.+)$'}]\n", "rules[0].currentAge: cannot be combined with versioning or extractVersion"},
	} {
		_, err := load(t, ".deps.yaml", tc.body)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want %q", tc.body, err, tc.want)
		}
	}
	// Errors point at the line of the offending key.
	_, err := load(t, ".deps.yaml", "rules:\n  - names: [a]\n    currentAge: old\n")
	var ce *Error
	if !errors.As(err, &ce) || len(ce.Problems()) != 1 || ce.Problems()[0].Line != 3 {
		t.Errorf("problems = %+v, want one on line 3", ce)
	}
}
