package engine

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
)

// Managers returns the managers enabled by cfg, in discovery order.
func Managers(_ *config.Config) ([]manager.Manager, error) {
	return []manager.Manager{actions.New(), gomod.New()}, nil
}
