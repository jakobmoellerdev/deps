// Package helm updates Helm chart versions: Chart.yaml dependencies (and the matching
// Chart.lock entries and digest, see Locker), helmfile releases, and the chart references of
// Kubernetes custom resources (k0s Chart, Flux HelmRelease, Argo CD Application). Charts are
// looked up with the helm datasource in classic chart repositories (index.yaml) and OCI
// registries.
package helm

import (
	"path"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name.
const Name = "helm"

// Datasource and Versioning of every helm dep.
const (
	Datasource = "helm"
	Versioning = "semver"
)

// Dep types.
const (
	TypeChart    = "chart"
	TypeHelmfile = "helmfile"
	TypeK0s      = "k0s"
	TypeFlux     = "flux"
	TypeArgoCD   = "argocd"
)

var helmfileRe = regexp.MustCompile(`(^|/)(helmfile\.ya?ml|helmfile\.d/[^/]+\.ya?ml)(\.gotmpl)?$`)

type helmManager struct {
	links links
}

// New returns the helm manager.
func New() manager.Manager { return &helmManager{} }

func (*helmManager) Name() string { return Name }

// Match reports whether the slash path is a Chart.yaml, a helmfile or any other YAML file
// (scanned for chart custom resources).
func (*helmManager) Match(p string) bool {
	return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml") || isHelmfile(p)
}

func isChart(p string) bool { return path.Base(p) == chartFile }

// isHelmfile reports whether the slash path is a helmfile. The name check skips the regexp
// for the paths that cannot match.
func isHelmfile(p string) bool { return strings.Contains(p, "helmfile") && helmfileRe.MatchString(p) }

// Extract returns the chart deps of src with RefSpan on the version.
func (m *helmManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	switch {
	case isChart(file):
		return chartDeps(file, src)
	case isHelmfile(file):
		return helmfileDeps(file, src)
	}
	return m.manifests(file, src), nil
}

// Edits replaces the version and, for a replacement (u.NewName), the chart name and the
// repository it moves to (see Rename).
func (*helmManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewName != "" {
		ne, err := NameEdits(d, u.NewName)
		if err != nil {
			return nil, err
		}
		edits = ne
	}
	if u.NewVersion != d.Version {
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	return edits, nil
}
