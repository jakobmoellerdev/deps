package renovate

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

// Implicit tells the rules written in the Renovate file from those of presets and those deps
// generates for managers Renovate does not run, by the origin comment of each translated
// rule as config reads it back.
func TestImplicit(t *testing.T) {
	res, err := load(t, `{
		"extends": ["config:recommended", ":semanticPrefixFixDepsChoreOthers"],
		"enabledManagers": ["npm", "gomod", "pre-commit"],
		"gomod": {"enabled": false},
		"ignoreDeps": ["left-pad"],
		"packageRules": [{"matchPackageNames": ["react"], "automerge": true}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(res.YAML, res.Path)
	if err != nil {
		t.Fatal(err)
	}
	own := map[string]bool{}
	implicit := 0
	for i := range cfg.Rules {
		c := cfg.Rules[i].Comment()
		if c == "" {
			t.Errorf("rules[%d] has no origin comment", i)
		}
		if Implicit(c) {
			implicit++
		} else {
			own[c] = true
		}
	}
	for _, want := range []string{"packageRules[0]", "ignoreDeps"} {
		if !own[want] {
			t.Errorf("%s not among the file's own rules %v", want, own)
		}
	}
	if len(own) != 2 || implicit == 0 {
		t.Errorf("own rules %v, %d implicit; want only packageRules[0] and ignoreDeps", own, implicit)
	}
}
