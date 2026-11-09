// Package glasskube updates the package versions of Glasskube `Package` and `ClusterPackage`
// resources, looked up in the package repository a `PackageRepository` resource of any
// scanned file names.
package glasskube

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/glasskubepackages"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "glasskube"

// Dep types.
const (
	TypePackage        = "package"
	TypeClusterPackage = "clusterPackage"
)

// group is the API group of Glasskube's resources, with the separator of the version.
const group = "packages.glasskube.dev/"

// defaultAnnotation marks the default PackageRepository, whose packages need no
// repositoryName.
const defaultAnnotation = "packages.glasskube.dev/default-repository"

type glasskubeManager struct {
	links links
}

// New returns the glasskube manager.
func New() manager.Manager { return &glasskubeManager{} }

func (*glasskubeManager) Name() string { return Name }

// Match reports whether the slash path is a YAML file: Glasskube resources have no file name
// convention (Renovate's manager has no default file pattern), so Extract prefilters.
func (*glasskubeManager) Match(p string) bool {
	return strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".yml")
}

// Extract returns the packages of the Package and ClusterPackage documents of src with
// RefSpan on the version, and records the PackageRepository documents and the repository each
// package names for Link. A file without the Glasskube API group is not parsed; an unparsable
// one (a template) yields nothing.
func (m *glasskubeManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	if !bytes.Contains(src, []byte(group)) {
		return nil, nil
	}
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	dec := yaml.NewDecoder(bytes.NewReader(yamlspan.KeepDocuments(src, mayHaveResource)))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Debug("skipping unparsable yaml", "file", file, "err", err)
			return nil, nil
		}
		if len(doc.Content) == 1 {
			x.document(doc.Content[0])
		}
	}
	m.links.add(file, x.repos, x.refs)
	return x.deps, nil
}

// mayHaveResource reports whether Extract must parse a document: its apiVersion, on the
// line of the key or the next, is in the Glasskube group. Glasskube's own install manifest
// names the group in CustomResourceDefinitions and annotations, which are skipped.
func mayHaveResource(doc []byte) bool {
	for rest := doc; ; {
		i := bytes.Index(rest, []byte("apiVersion"))
		if i < 0 {
			return false
		}
		rest = rest[i+len("apiVersion"):]
		value := rest
		if j := bytes.IndexByte(value, '\n'); j >= 0 {
			if k := bytes.IndexByte(value[j+1:], '\n'); k >= 0 {
				value = value[:j+1+k]
			}
		}
		if bytes.Contains(value, []byte(group)) {
			return true
		}
	}
}

// extractor collects the packages, repositories and repository references of one file.
type extractor struct {
	file  string
	src   []byte
	lines []int
	deps  []dep.Dep
	refs  []string // the repositoryName of each dep ("" = the default repository)
	repos []repository
}

// document extracts a Package or ClusterPackage, or records a PackageRepository. Resources
// lacking a field Renovate's schema requires (a package's name and version, a repository's
// metadata.name and spec.url) are none.
func (x *extractor) document(n *yaml.Node) {
	if !strings.HasPrefix(yamlspan.Value(yamlspan.Get(n, "apiVersion")), group) {
		return
	}
	spec := yamlspan.Get(n, "spec")
	depType := TypePackage
	switch yamlspan.Value(yamlspan.Get(n, "kind")) {
	case "ClusterPackage":
		depType = TypeClusterPackage
	case "Package":
	case "PackageRepository":
		meta := yamlspan.Get(n, "metadata")
		name, url := yamlspan.Str(yamlspan.Get(meta, "name")), yamlspan.Str(yamlspan.Get(spec, "url"))
		if name != nil && url != nil {
			x.repos = append(x.repos, repository{name: name.Value, url: strings.TrimSpace(url.Value),
				isDefault: yamlspan.Value(yamlspan.Get(yamlspan.Get(meta, "annotations"), defaultAnnotation)) == "true"})
		}
		return
	default:
		return
	}
	info := yamlspan.Get(spec, "packageInfo")
	name, v := yamlspan.Str(yamlspan.Get(info, "name")), yamlspan.Str(yamlspan.Get(info, "version"))
	if name == nil || v == nil {
		return
	}
	d := dep.Dep{Manager: Name, DepType: depType, Datasource: glasskubepackages.Name, Versioning: glasskubepackages.Versioning,
		File: x.file, Line: v.Line, Name: name.Value, Version: v.Value}
	switch off := yamlspan.Locate(x.src, x.lines, v); {
	case v.Value == "":
		d.Skip = "no version"
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		d.RefSpan = dep.Span{Start: off, End: off + len(v.Value)}
	}
	x.deps = append(x.deps, d)
	x.refs = append(x.refs, yamlspan.Value(yamlspan.Get(info, "repositoryName")))
}

// Edits replaces the version.
func (*glasskubeManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if u.NewVersion == d.Version {
		return nil, nil
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
