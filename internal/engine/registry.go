package engine

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/custom"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/registry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Managers returns the managers enabled by cfg, in discovery order: the built-in managers
// (registry.Builtin), then one custom manager per cfg.Custom entry.
func Managers(cfg *config.Config) ([]manager.Manager, error) {
	cs, err := custom.New(cfg)
	if err != nil {
		return nil, err
	}
	ms := make([]manager.Manager, 0, len(registry.Builtin)+len(cs))
	for _, e := range registry.Builtin {
		ms = append(ms, e.New())
	}
	return append(ms, cs...), nil
}

// SkipDocs are the skip reasons the engine sets for every manager.
var SkipDocs = []refdoc.Skip{
	{Reason: "ignored by rule #<i>", When: "a pre-policy rule set `ignore: true`"},
	{Reason: "ignored by default (<type>; enable with a rule setting ignore: false)", When: "dep types `indirect`, `test-indirect`, `go`, `peerDependencies`, `engines`, `submodule`, `task`"},
	{Reason: "non-version value \"<v>\"", When: "the current value is not valid in the dep's versioning scheme"},
	{Reason: "non-version tag", When: "a docker-datasource dep with a non-version tag (`latest`, `bookworm`, a commit hash), not pinned, and `pin: keep`"},
	{Reason: "current version <v> not found upstream (pin an existing version)", When: "a pinned or to-be-pinned dep whose current tag or image tag no longer exists (digest refresh and pin need it)"},
}
