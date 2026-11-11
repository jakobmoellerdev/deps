package toolrun

import (
	"os"
	"slices"
	"strings"
)

// secretWords name a credential. A DEPS_ variable holding one of them as a word
// (DEPS_GITHUB_COM_TOKEN, DEPS_SIGNING_KEY, DEPS_GALAXY_AUTH_URL) is one of deps' own
// credentials, which a datasource may add at any time; the word must be matched, not the
// substring, so that DEPS_GIT_AUTHOR stays.
var secretWords = []string{"TOKEN", "KEY", "SECRET", "PASSWORD", "PASS", "CREDENTIAL", "CREDENTIALS", "AUTH", "USER"}

// Secret reports whether the environment variable name holds one of deps' own credentials.
// They are kept from the tools deps runs, since those evaluate repository code (Gemfile,
// mix.exs, build scripts, setup.py) and packages fetched on the fly. Credentials of the
// ecosystems (GITHUB_TOKEN, NPM_TOKEN, CI_JOB_TOKEN, …) stay, as private registry
// configurations commonly reference them.
func Secret(name string) bool {
	rest, ok := strings.CutPrefix(name, "DEPS_")
	if !ok {
		return false
	}
	for word := range strings.SplitSeq(rest, "_") {
		if slices.Contains(secretWords, word) {
			return true
		}
	}
	return false
}

// Env is the environment of a tool deps runs: deps' own without the Secret variables,
// followed by extra (KEY=value entries, which win over inherited ones).
func Env(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return Secret(k)
	})
	return append(env, extra...)
}
