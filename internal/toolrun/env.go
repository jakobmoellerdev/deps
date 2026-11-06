package toolrun

import (
	"os"
	"slices"
	"strings"
)

// Secrets are deps' own credentials. They are kept from the tools deps runs, since those
// evaluate repository code (Gemfile, mix.exs, build scripts, setup.py) and packages fetched
// on the fly. Credentials of the ecosystems (GITHUB_TOKEN, NPM_TOKEN, CI_JOB_TOKEN, …) stay,
// as private registry configurations commonly reference them.
var Secrets = []string{
	"DEPS_TOKEN", "DEPS_GITHUB_COM_TOKEN", "DEPS_GITLAB_TOKEN", "DEPS_AZURE_DEVOPS_TOKEN",
	"DEPS_CIRCLECI_TOKEN", "DEPS_SIGNING_KEY", "DEPS_EXTENDS_USER", "DEPS_EXTENDS_TOKEN",
}

// Env is the environment of a tool deps runs: deps' own without Secrets, followed by extra
// (KEY=value entries, which win over inherited ones).
func Env(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(Secrets, k)
	})
	return append(env, extra...)
}
