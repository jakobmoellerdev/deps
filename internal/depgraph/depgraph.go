// Package depgraph builds GitHub dependency graph snapshots (the dependency submission API)
// from extracted dependencies: one manifest per file, one package URL (purl) per dependency.
package depgraph

import (
	"net/url"
	"sort"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/imageref"
)

// Static are the managers whose manifests GitHub's dependency graph reads by itself (static
// analysis, or Dependabot graph jobs for Go and Python). A submitted manifest replaces
// GitHub's view of that file, and deps knows only the declared dependencies, not the resolved
// transitive ones, so these are left out unless asked for.
var Static = map[string]bool{
	"gomod": true, "npm": true, "pip_requirements": true, "pep621": true, "poetry": true, "pipenv": true,
	"cargo": true, "maven": true, "github-actions": true,
}

// Snapshot is the request body of POST /repos/{repo}/dependency-graph/snapshots.
type Snapshot struct {
	Version   int                 `json:"version"`
	Job       Job                 `json:"job"`
	SHA       string              `json:"sha"`
	Ref       string              `json:"ref"`
	Detector  Detector            `json:"detector"`
	Scanned   string              `json:"scanned"`
	Manifests map[string]Manifest `json:"manifests"`
}

type Job struct {
	ID         string `json:"id"`
	Correlator string `json:"correlator"`
}

type Detector struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	URL     string `json:"url"`
}

type Manifest struct {
	Name     string             `json:"name"`
	File     File               `json:"file"`
	Resolved map[string]Package `json:"resolved"`
}

type File struct {
	SourceLocation string `json:"source_location"`
}

type Package struct {
	PackageURL   string `json:"package_url"`
	Relationship string `json:"relationship"`
	Scope        string `json:"scope"`
}

// Options select and describe the snapshot.
type Options struct {
	Job      Job
	SHA, Ref string
	Detector Detector
	Scanned  time.Time
	// Prefix is the path of the scanned directory from the repository root, with a trailing
	// '/' ("" at the root): GitHub wants manifest paths relative to the repository root.
	Prefix string
	// AllManagers also submits the managers of Static.
	AllManagers bool
}

// Build returns the snapshot of deps and how many deps it contains. Deps without a package
// URL (their datasource has no purl type, or there is no concrete version) and deps the
// manager skipped for lack of a version are left out; deps skipped only by policy
// (ignored by default or by a rule) are real dependencies and are included.
func Build(deps []dep.Dep, o Options) (Snapshot, int) {
	s := Snapshot{
		Job: o.Job, SHA: o.SHA, Ref: o.Ref, Detector: o.Detector,
		Scanned:   o.Scanned.UTC().Format(time.RFC3339),
		Manifests: map[string]Manifest{},
	}
	n := 0
	for _, d := range deps {
		if !o.AllManagers && Static[d.Manager] {
			continue
		}
		if d.Skip != "" && !strings.HasPrefix(d.Skip, "ignored by") {
			continue
		}
		purl, ok := PURL(d)
		if !ok {
			continue
		}
		file := o.Prefix + d.File
		m, ok := s.Manifests[file]
		if !ok {
			m = Manifest{Name: file, File: File{SourceLocation: file}, Resolved: map[string]Package{}}
			s.Manifests[file] = m
		}
		if _, dup := m.Resolved[purl]; dup {
			continue
		}
		m.Resolved[purl] = Package{PackageURL: purl, Relationship: "direct", Scope: scope(d)}
		n++
	}
	return s, n
}

// Files returns the manifest paths of s, sorted.
func (s Snapshot) Files() []string {
	out := make([]string, 0, len(s.Manifests))
	for f := range s.Manifests {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func scope(d dep.Dep) string {
	switch d.DepType {
	case "devDependencies", "dev-dependencies", "dev-packages", "test", "tool.uv.dev-dependencies", "tool.pdm.dev-dependencies":
		return "development"
	}
	return "runtime"
}

// version is the concrete version of d: the locked one when known, else the written one; ""
// for a range or constraint without a locked version (`>= 0.13`, `^1.2`).
func version(d dep.Dep) string {
	if d.Locked != "" {
		return d.Locked
	}
	v := d.Version
	if v == "" || (d.Range != "" && d.Range != v) || strings.ContainsAny(v, "$*{}<>=^~ ,|") {
		return ""
	}
	return v
}

// esc escapes a purl name or version segment.
func esc(s string) string { return strings.ReplaceAll(url.PathEscape(s), "%2F", "/") }

// PURL returns the package URL of d by its datasource; GitHub Actions get the githubactions
// type of the GitHub Advisory Database.
func PURL(d dep.Dep) (string, bool) {
	v := version(d)
	if v == "" {
		return "", false
	}
	if d.Manager == "github-actions" && d.Datasource == "git-tags" && d.DepType == "action" {
		parts := strings.Split(d.Name, "/")
		if len(parts) < 2 || strings.Contains(d.Name, "://") {
			return "", false
		}
		return "pkg:githubactions/" + esc(parts[0]) + "/" + esc(parts[1]) + "@" + esc(v), true
	}
	switch d.Datasource {
	case "go":
		return "pkg:golang/" + esc(d.Name) + "@" + esc(v), true
	case "npm":
		name := d.Name
		if scope, rest, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
			name = "%40" + esc(scope[1:]) + "/" + esc(rest)
		} else {
			name = esc(name)
		}
		return "pkg:npm/" + name + "@" + esc(v), true
	case "pypi":
		return "pkg:pypi/" + esc(strings.ToLower(d.Name)) + "@" + esc(v), true
	case "crate":
		return "pkg:cargo/" + esc(d.Name) + "@" + esc(v), true
	case "maven":
		group, artifact, ok := strings.Cut(d.Name, ":")
		if !ok {
			return "", false
		}
		return "pkg:maven/" + esc(group) + "/" + esc(artifact) + "@" + esc(v), true
	case "github-releases":
		if strings.Contains(d.Name, "://") || strings.Count(d.Name, "/") != 1 {
			return "", false // GHES and other hosts have no purl type
		}
		return "pkg:github/" + esc(d.Name) + "@" + esc(v), true
	case "docker":
		r, err := imageref.Parse(d.Name)
		if err != nil {
			return "", false
		}
		registry, path, _ := strings.Cut(r.Name, "/")
		purl := "pkg:docker/" + esc(path) + "@" + esc(v)
		if registry != "docker.io" {
			purl += "?repository_url=" + url.QueryEscape(registry)
		}
		return purl, true
	}
	return "", false
}
