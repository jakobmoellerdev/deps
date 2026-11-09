package config

import (
	"maps"
	"slices"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/registry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// managers are the managers of the registry in discovery order, the custom manager last.
var managers = registry.Docs()

// managerByName returns the registry entry of name, or nil.
func managerByName(name string) *refdoc.Manager {
	for i := range managers {
		if managers[i].Name == name {
			return &managers[i]
		}
	}
	return nil
}

// uses reports whether deps of m with depType ("" = any of m's dep types) can have the
// canonical datasource ds ("" = any datasource); a dep type without Datasources (the custom
// manager) can have any.
func uses(m *refdoc.Manager, depType, ds string) bool {
	for _, dt := range m.DepTypes {
		if depType != "" && dt.Name != depType {
			continue
		}
		if ds == "" || dt.Datasources == nil || slices.Contains(dt.Datasources, ds) {
			return true
		}
	}
	return false
}

func managerNames() []string {
	out := make([]string, len(managers))
	for i, m := range managers {
		out[i] = m.Name
	}
	return out
}

// depTypeNames are the dep types of all managers, each once, in order of first appearance.
func depTypeNames() []string {
	var out []string
	for _, m := range managers {
		for _, dt := range m.DepTypes {
			if !slices.Contains(out, dt.Name) {
				out = append(out, dt.Name)
			}
		}
	}
	return out
}

// standaloneDatasources are datasources no built-in manager uses: custom managers and
// Renovate markers reach them.
var standaloneDatasources = []string{"gitlab-packages", "rpm", "apk", "deb", "artifactory", "gitlab-releases", "gitlab-tags", "gitea-releases", "gitea-tags", "forgejo-releases", "forgejo-tags", "endoflife-date"}

// datasourceNames are the datasources of all managers' dep types and standaloneDatasources,
// sorted.
func datasourceNames() []string {
	out := slices.Clone(standaloneDatasources)
	for _, m := range managers {
		for _, dt := range m.DepTypes {
			for _, ds := range dt.Datasources {
				if !slices.Contains(out, ds) {
					out = append(out, ds)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// ManagerDepTypes returns the dep types of each manager (rules[].managers values), in
// documentation order (docs/reference/managers.md).
func ManagerDepTypes() map[string][]string {
	out := make(map[string][]string, len(managers))
	for _, m := range managers {
		dts := make([]string, len(m.DepTypes))
		for i, dt := range m.DepTypes {
			dts[i] = dt.Name
		}
		out[m.Name] = dts
	}
	return out
}

// datasourceAliasNames are the DatasourceAliases keys, sorted.
func datasourceAliasNames() []string { return slices.Sorted(maps.Keys(DatasourceAliases)) }
