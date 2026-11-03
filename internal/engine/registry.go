package engine

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/custom"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
)

// Managers returns the managers enabled by cfg, in discovery order: the built-in managers,
// then one custom manager per cfg.Custom entry.
func Managers(cfg *config.Config) ([]manager.Manager, error) {
	cs, err := custom.New(cfg.Custom)
	if err != nil {
		return nil, err
	}
	ms := make([]manager.Manager, 0, 3+len(cs))
	ms = append(ms, actions.New(), gomod.New(), docker.New())
	return append(ms, cs...), nil
}
