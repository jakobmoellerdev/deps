package engine

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/custom"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Managers returns the managers enabled by cfg, in discovery order: the built-in managers,
// then one custom manager per cfg.Custom entry.
func Managers(cfg *config.Config) ([]manager.Manager, error) {
	cs, err := custom.New(cfg)
	if err != nil {
		return nil, err
	}
	ms := make([]manager.Manager, 0, 5+len(cs))
	ms = append(ms, actions.New(), gomod.New(), docker.New(), npm.New(), helm.New())
	return append(ms, cs...), nil
}

// SkipDocs are the skip reasons the engine sets for every manager.
var SkipDocs = []refdoc.Skip{
	{Reason: "ignored by rule #<i>", When: "a pre-policy rule set `ignore: true`"},
	{Reason: "ignored by default (<type>; enable with a rule setting ignore: false)", When: "dep types `indirect`, `go`, `peerDependencies`, `engines`"},
	{Reason: "non-version value \"<v>\"", When: "the current value is not valid in the dep's versioning scheme"},
	{Reason: "non-version tag", When: "a docker-datasource dep with a non-version tag (`latest`, `bookworm`), not pinned, and `pin: keep`"},
}

// ManagerDocs returns the reference documentation of the built-in managers and the custom
// manager, in discovery order.
func ManagerDocs() []refdoc.Manager {
	return []refdoc.Manager{actions.Doc, gomod.Doc, docker.Doc, npm.Doc, helm.Doc, custom.Doc}
}
