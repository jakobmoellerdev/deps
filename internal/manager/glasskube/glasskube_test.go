package glasskube

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

type want struct {
	file, depType, name, version, source, skip string
	line                                       int
}

// run extracts files (path → content) in path order, as the engine discovers them, and links
// the deps.
func run(t *testing.T, m manager.Manager, files map[string]string) [][]dep.Dep {
	t.Helper()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var out [][]dep.Dep
	for _, p := range paths {
		if !m.Match(p) {
			t.Fatalf("Match(%q) = false", p)
		}
		src := []byte(files[p])
		deps, err := m.Extract(p, src, manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range deps {
			if d.Manager != Name || d.Datasource != "glasskube-packages" || d.Versioning != "glasskube" {
				t.Errorf("%s: %s/%s/%s", d.Name, d.Manager, d.Datasource, d.Versioning)
			}
			if d.Skip == "" && string(src[d.RefSpan.Start:d.RefSpan.End]) != d.Version {
				t.Errorf("%s: src[RefSpan] = %q, want %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End], d.Version)
			}
		}
		out = append(out, deps)
	}
	m.(manager.Linker).Link(out)
	return out
}

func check(t *testing.T, files [][]dep.Dep, wants []want) {
	t.Helper()
	var got []dep.Dep
	for _, deps := range files {
		got = append(got, deps...)
	}
	if len(got) != len(wants) {
		t.Fatalf("got %d deps, want %d: %+v", len(got), len(wants), got)
	}
	for i, w := range wants {
		d := got[i]
		if d.File != w.file || d.DepType != w.depType || d.Name != w.name || d.Version != w.version || d.Source != w.source || d.Skip != w.skip || d.Line != w.line {
			t.Errorf("%d: got %s %s %s %s source=%q skip=%q line %d\nwant %+v", i, d.File, d.DepType, d.Name, d.Version, d.Source, d.Skip, d.Line, w)
		}
	}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, ok := range map[string]bool{
		"packages/argo-cd/clusterpackage.yaml": true, "a.yml": true, "x.json": false, "yaml": false, "Chart.lock": false,
	} {
		if got := m.Match(p); got != ok {
			t.Errorf("Match(%q) = %v", p, got)
		}
	}
	if m.Name() != Name {
		t.Error("Name")
	}
}

// The ClusterPackages of glasskube/gitops-template (66598aec) resolve against the default
// PackageRepository of the Glasskube install manifest it commits (an excerpt: the namespaces,
// the PackageRepository CustomResourceDefinition and the PackageRepository).
func TestGitopsTemplate(t *testing.T) {
	files := map[string]string{}
	root := "testdata/gitops-template"
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		files[filepath.ToSlash(strings.TrimPrefix(p, root+"/"))] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	const repo = "https://packages.dl.glasskube.dev/packages"
	check(t, run(t, New(), files), []want{
		{"packages/argo-cd/clusterpackage.yaml", TypeClusterPackage, "argo-cd", "v2.14.1+1", repo, "", 9},
		{"packages/cloudnative-pg/clusterpackage.yaml", TypeClusterPackage, "cloudnative-pg", "v1.25.0+1", repo, "", 9},
		{"packages/kube-prometheus-stack/clusterpackage.yaml", TypeClusterPackage, "kube-prometheus-stack", "v68.4.4+1", repo, "", 9},
	})
}

const (
	defaultRepo = `apiVersion: packages.glasskube.dev/v1alpha1
kind: PackageRepository
metadata:
  annotations:
    packages.glasskube.dev/default-repository: "true"
  name: glasskube
spec:
  url: https://packages.dl.glasskube.dev/packages
`
	namedPackage = `apiVersion: packages.glasskube.dev/v1alpha1
kind: ClusterPackage
metadata:
  name: argo-cd
spec:
  packageInfo:
    name: argo-cd
    repositoryName: glasskube
    version: v2.11.7+1
`
)

// The cases of Renovate's extract.spec.ts, and the repository precedence.
func TestRepositories(t *testing.T) {
	const pub = "https://packages.dl.glasskube.dev/packages"
	m := New()
	// A package without a matching repository is skipped (Renovate's unknown-registry).
	check(t, run(t, m, map[string]string{"package.yaml": namedPackage}), []want{
		{"package.yaml", TypeClusterPackage, "argo-cd", "v2.11.7+1", "", "unknown PackageRepository glasskube", 9},
	})
	// The repository of another file; Link forgot the earlier run's files.
	check(t, run(t, m, map[string]string{"package.yaml": namedPackage, "repo.yaml": defaultRepo}), []want{
		{"package.yaml", TypeClusterPackage, "argo-cd", "v2.11.7+1", pub, "", 9},
	})
	check(t, run(t, m, map[string]string{"package.yaml": namedPackage}), []want{
		{"package.yaml", TypeClusterPackage, "argo-cd", "v2.11.7+1", "", "unknown PackageRepository glasskube", 9},
	})
	// An empty or missing repositoryName uses the default repository.
	check(t, run(t, m, map[string]string{
		"package-with-empty-reponame.yaml":   strings.Replace(namedPackage, "repositoryName: glasskube", `repositoryName: ""`, 1),
		"package-with-missing-reponame.yaml": strings.Replace(namedPackage, "    repositoryName: glasskube\n", "", 1),
		"repo.yaml":                          defaultRepo,
	}), []want{
		{"package-with-empty-reponame.yaml", TypeClusterPackage, "argo-cd", "v2.11.7+1", pub, "", 9},
		{"package-with-missing-reponame.yaml", TypeClusterPackage, "argo-cd", "v2.11.7+1", pub, "", 8},
	})
	// Package and repository in one file (Renovate's package-and-repo.yaml); a namespaced
	// Package; the first repository of a name, or the first default one, in path order wins.
	check(t, run(t, m, map[string]string{
		"a/both.yaml": namedPackage + "---\n" + strings.Replace(defaultRepo, pub, "https://a.example/packages/", 1),
		"b/dup.yml":   strings.Replace(defaultRepo, pub, "https://b.example/packages", 1),
		"c/ns.yaml": `apiVersion: packages.glasskube.dev/v1alpha1
kind: Package
metadata:
  name: shiori
  namespace: shiori
spec:
  packageInfo:
    name: shiori
    repositoryName: private
    version: 'v1.7.4+1'
---
apiVersion: packages.glasskube.dev/v1alpha1
kind: PackageRepository
metadata:
  name: private
spec:
  url: https://private.example/packages
`,
		"d/nodefault.yaml": strings.Replace(namedPackage, "    repositoryName: glasskube\n", "", 1),
	}), []want{
		{"a/both.yaml", TypeClusterPackage, "argo-cd", "v2.11.7+1", "https://a.example/packages/", "", 9},
		{"c/ns.yaml", TypePackage, "shiori", "v1.7.4+1", "https://private.example/packages", "", 10},
		{"d/nodefault.yaml", TypeClusterPackage, "argo-cd", "v2.11.7+1", "https://a.example/packages/", "", 8},
	})
}

func TestNoDefaultRepository(t *testing.T) {
	check(t, run(t, New(), map[string]string{
		"p.yaml": strings.Replace(namedPackage, "    repositoryName: glasskube\n", "", 1),
		"r.yaml": strings.Replace(defaultRepo, `"true"`, `"false"`, 1),
	}), []want{
		{"p.yaml", TypeClusterPackage, "argo-cd", "v2.11.7+1", "", "no default PackageRepository", 8},
	})
}

// Documents that are not Glasskube packages with a name and version yield nothing; a version
// deps cannot locate or that is empty is skipped, and keeps that reason after Link.
func TestExtractSkips(t *testing.T) {
	m := New()
	for name, src := range map[string]string{
		"no group":       "apiVersion: v1\nkind: ConfigMap\ndata:\n  x: packages.glasskube.dev\n",
		"crd":            "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nspec:\n  group: packages.glasskube.dev/x\n",
		"other kind":     "apiVersion: packages.glasskube.dev/v1alpha1\nkind: PackageInfo\nspec:\n  packageInfo: {name: a, version: v1.0.0+1}\n",
		"no packageInfo": "apiVersion: packages.glasskube.dev/v1alpha1\nkind: ClusterPackage\nspec: {}\n",
		"no version":     "apiVersion: packages.glasskube.dev/v1alpha1\nkind: Package\nspec:\n  packageInfo:\n    name: a\n",
		"repo no url":    "apiVersion: packages.glasskube.dev/v1alpha1\nkind: PackageRepository\nmetadata:\n  name: r\n",
		"unparsable":     "apiVersion: packages.glasskube.dev/v1alpha1\nkind: Package\nspec: [\n",
		"other group":    "apiVersion: other.dev/v1\nkind: Package\nmetadata:\n  annotations:\n    packages.glasskube.dev/x: y\nspec:\n  packageInfo: {name: a, version: v1.0.0+1}\n",
	} {
		deps, err := m.Extract("f.yaml", []byte(src), manager.Env{})
		if err != nil || len(deps) != 0 {
			t.Errorf("%s: %+v, %v", name, deps, err)
		}
	}
	m.(manager.Linker).Link(nil)
	check(t, run(t, m, map[string]string{
		"a.yaml": `apiVersion:
  packages.glasskube.dev/v1alpha1
kind: Package
spec:
  packageInfo:
    name: escaped
    version: "v1.0.0\x2B1"
---
apiVersion: packages.glasskube.dev/v1alpha1
kind: Package
spec:
  packageInfo:
    name: empty
    version: ""
    repositoryName: glasskube
`,
		"b.yaml": defaultRepo,
	}), []want{
		{"a.yaml", TypePackage, "escaped", "v1.0.0+1", "https://packages.dl.glasskube.dev/packages", "cannot locate value", 7},
		{"a.yaml", TypePackage, "empty", "", "https://packages.dl.glasskube.dev/packages", "no version", 14},
	})
}

func TestEdits(t *testing.T) {
	src := "apiVersion: packages.glasskube.dev/v1alpha1\nkind: ClusterPackage\nspec:\n  packageInfo:\n    name: cert-manager\n    version: \"v1.17.0+1\" # pinned\n"
	files := run(t, New(), map[string]string{"cm.yaml": src, "repo.yaml": defaultRepo})
	d := files[0][0]
	m := New()
	edits, err := m.Edits(dep.Update{Dep: d, NewVersion: "v1.19.1+1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(src, "v1.17.0+1", "v1.19.1+1", 1); string(out) != want {
		t.Errorf("edited:\n%s\nwant:\n%s", out, want)
	}
	if edits, err := m.Edits(dep.Update{Dep: d, NewVersion: d.Version}); err != nil || edits != nil {
		t.Errorf("same version: %v, %v", edits, err)
	}
}
