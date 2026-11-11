package toolrun

import (
	"slices"
	"strings"
	"testing"
)

func TestEnv(t *testing.T) {
	secrets := []string{
		"DEPS_TOKEN", "DEPS_GITHUB_COM_TOKEN", "DEPS_GITLAB_TOKEN", "DEPS_AZURE_DEVOPS_TOKEN",
		"DEPS_CIRCLECI_TOKEN", "DEPS_SIGNING_KEY", "DEPS_EXTENDS_USER", "DEPS_EXTENDS_TOKEN",
		"DEPS_CONDA_TOKEN", "DEPS_GALAXY_TOKEN", "DEPS_GALAXY_AUTH_URL",
	}
	for _, k := range secrets {
		t.Setenv(k, "secret")
	}
	kept := []string{"GITHUB_TOKEN=gh", "NPM_TOKEN=npm", "DEPS_LOG_LEVEL=debug", "DEPS_GIT_AUTHOR=bot <bot@example.com>"}
	for _, kv := range kept {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	env := Env("A=1", "NPM_TOKEN=override")
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); slices.Contains(secrets, k) {
			t.Errorf("Env() has %s", kv)
		}
	}
	for _, want := range kept {
		if !slices.Contains(env, want) {
			t.Errorf("Env() misses %s", want)
		}
	}
	if got := env[len(env)-2:]; !slices.Equal(got, []string{"A=1", "NPM_TOKEN=override"}) {
		t.Errorf("Env() ends with %q", got)
	}
}

// TestSecret checks that the filter catches the credentials by their name, so that one a
// datasource adds later is kept from the tools too.
func TestSecret(t *testing.T) {
	for name, want := range map[string]bool{
		"DEPS_TOKEN":                 true,
		"DEPS_CONDA_TOKEN":           true,
		"DEPS_GALAXY_AUTH_URL":       true,
		"DEPS_SIGNING_KEY":           true,
		"DEPS_EXTENDS_USER":          true,
		"DEPS_NEW_REGISTRY_PASSWORD": true,
		"DEPS_GIT_AUTHOR":            false, // AUTHOR is not AUTH
		"DEPS_PYPI_URL":              false,
		"DEPS_LOG_LEVEL":             false,
		"DEPS_":                      false,
		"DEPSTOKEN":                  false,
		"GITHUB_TOKEN":               false,
		"CI_JOB_TOKEN":               false,
	} {
		if got := Secret(name); got != want {
			t.Errorf("Secret(%q) = %v, want %v", name, got, want)
		}
	}
}
