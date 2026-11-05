package gomod_test

import (
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
)

// TestPolicyDefaults is an external test: config imports the manager registry, which imports
// this package.
func TestPolicyDefaults(t *testing.T) {
	const src = "module example.com/x\n\ngo 1.27\n\nrequire github.com/spf13/cobra v1.9.1\n\n" +
		"require github.com/a/ind v1.0.0 // indirect\n\ntoolchain go1.27.1\n"
	deps, err := gomod.New().Extract("go.mod", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"go": true, "indirect": true, "toolchain": false, "direct": false}
	def := config.Default()
	f := false
	optIn := &config.Config{Version: 1, Pin: config.PinKeep, Rules: []config.Rule{{DepTypes: []string{"go", "indirect"}, Ignore: &f}}}
	seen := map[string]bool{}
	for _, d := range deps {
		if d.Skip != "" {
			continue
		}
		seen[d.DepType] = true
		if got := def.Policy(d).Ignore; got != want[d.DepType] {
			t.Errorf("%s (%s): default Ignore = %v", d.Name, d.DepType, got)
		}
		if optIn.Policy(d).Ignore {
			t.Errorf("%s: opt-in rule should un-ignore", d.Name)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("dep types = %v, want %v", seen, want)
	}
}
