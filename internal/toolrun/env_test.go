package toolrun

import (
	"slices"
	"testing"
)

func TestEnv(t *testing.T) {
	for _, k := range Secrets {
		t.Setenv(k, "secret")
	}
	t.Setenv("GITHUB_TOKEN", "gh")
	t.Setenv("NPM_TOKEN", "npm")
	t.Setenv("DEPS_LOG_LEVEL", "debug")
	env := Env("A=1", "NPM_TOKEN=override")
	for _, kv := range env {
		for _, k := range Secrets {
			if len(kv) > len(k) && kv[:len(k)+1] == k+"=" {
				t.Errorf("Env() has %s", kv)
			}
		}
	}
	for _, want := range []string{"GITHUB_TOKEN=gh", "NPM_TOKEN=npm", "DEPS_LOG_LEVEL=debug"} {
		if !slices.Contains(env, want) {
			t.Errorf("Env() misses %s", want)
		}
	}
	if got := env[len(env)-2:]; !slices.Equal(got, []string{"A=1", "NPM_TOKEN=override"}) {
		t.Errorf("Env() ends with %q", got)
	}
}
