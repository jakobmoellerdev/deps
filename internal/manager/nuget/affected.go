package nuget

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// thisFileDir is the MSBuild property holding the directory of the file an Include or Project
// attribute is written in, with a trailing separator; deps evaluates it to resolve the paths
// that use it, the common way to write a repository-relative import.
const thisFileDir = "$(MSBuildThisFileDirectory)"

// isConventionImport reports whether the slash path is an MSBuild file that every project
// below its directory imports implicitly: the .NET SDK wraps a project in the nearest
// Directory.Build.props and Directory.Build.targets, and central package management reads
// Directory.Packages.props.
func isConventionImport(p string) bool {
	switch strings.ToLower(path.Base(p)) {
	case "directory.build.props", "directory.build.targets", "directory.packages.props":
		return true
	}
	return false
}

// msbuildLinks returns the `<ProjectReference Include>` and `<Import Project>` paths of an
// MSBuild file as they are written, and whether deps failed to read all of them (a malformed
// file, an `<Import Project>` without a path): such a file is attributed conservatively, as
// if it imported every file of the repository. `<Import Sdk>` imports a file of an SDK, not
// one of the repository, and is left out. Element names are matched case-insensitively, as
// MSBuild does.
func msbuildLinks(src []byte) (refs, imports []string, unresolved bool) {
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))))
	dec.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
			return r, nil
		}
		return nil, fmt.Errorf("unsupported encoding %q", label)
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return refs, imports, unresolved || !errors.Is(err, io.EOF)
		}
		e, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case strings.EqualFold(e.Name.Local, "ProjectReference"):
			if v, ok := attrValue(e, "include"); ok {
				refs = append(refs, v)
			}
		case strings.EqualFold(e.Name.Local, "Import"):
			if _, ok := attrValue(e, "sdk"); ok {
				continue
			}
			v, ok := attrValue(e, "project")
			if !ok {
				unresolved = true
				continue
			}
			imports = append(imports, v)
		}
	}
}

// attrValue returns the value of the attribute name (lower case) of the element.
func attrValue(e xml.StartElement, name string) (string, bool) {
	for _, a := range e.Attr {
		if strings.EqualFold(a.Name.Local, name) {
			return a.Value, true
		}
	}
	return "", false
}

// outsideDirs are the MSBuild properties holding a directory outside the repository: the
// toolset, Visual Studio and the package folder. A path starting with one of them imports a
// file deps never patches, so it is left out instead of making its file opaque.
var outsideDirs = []string{
	"$(MSBuildToolsPath)", "$(MSBuildToolsPath32)", "$(MSBuildToolsPath64)", "$(MSBuildBinPath)",
	"$(MSBuildExtensionsPath)", "$(MSBuildExtensionsPath32)", "$(MSBuildExtensionsPath64)",
	"$(MSBuildSDKsPath)", "$(MSBuildProgramFiles32)", "$(VSToolsPath)", "$(NuGetPackageRoot)",
}

// resolveLink resolves the path raw, written in file (a slash path relative to the
// repository), to a slash path relative to the repository. ok is false when raw is an MSBuild
// expression deps does not evaluate or a wildcard; a path leaving the repository resolves to
// "", true, since no patched file sits there.
func resolveLink(file, raw string) (string, bool) {
	p := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(raw), `\`, "/"), thisFileDir)
	if i := strings.Index(p, ")"); i > 0 && strings.HasPrefix(p, "$(") &&
		slices.ContainsFunc(outsideDirs, func(d string) bool { return strings.EqualFold(d, p[:i+1]) }) {
		return "", true
	}
	if p == "" || strings.Contains(p, "$(") || strings.Contains(p, "*") {
		return "", false
	}
	p = path.Join(path.Dir(file), p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", true
	}
	return p, true
}

// msbuildGraph is the MSBuild file graph of a repository: the projects it holds, which
// project reaches which through a ProjectReference, which file imports which, and the files
// whose links deps could not resolve. All paths are slash paths relative to the repository.
type msbuildGraph struct {
	projects   []string            // every project below the repository, sorted
	dependents map[string][]string // project → the projects whose ProjectReference names it
	importers  map[string][]string // file → the files whose Import names it
	opaque     []string            // files with a link deps could not resolve, sorted
}

// newMSBuildGraph reads the MSBuild files below dir, preferring the content in patched (by
// slash path relative to dir) over the one on disk, and links them. Unreadable files and
// files with links deps cannot resolve are opaque: they are attributed to whatever they could
// import.
func newMSBuildGraph(dir string, patched map[string][]byte) *msbuildGraph {
	g := &msbuildGraph{dependents: map[string][]string{}, importers: map[string][]string{}}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil //nolint:nilerr // unreadable parts of the tree are not scanned
		case d.IsDir():
			if p != dir && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		case !d.Type().IsRegular():
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil //nolint:nilerr // a path outside dir is none of its files
		}
		if r := filepath.ToSlash(rel); isMSBuild(r) {
			files = append(files, r)
		}
		return nil
	})
	slices.Sort(files)
	for _, f := range files {
		if isProject(f) {
			g.projects = append(g.projects, f)
		}
		src, ok := patched[f]
		if !ok {
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
			if err != nil {
				g.opaque = append(g.opaque, f)
				continue
			}
			src = b
		}
		refs, imports, unresolved := msbuildLinks(src)
		for _, r := range refs {
			t, ok := resolveLink(f, r)
			switch {
			case !ok:
				unresolved = true
			case t != "" && !slices.Contains(g.dependents[t], f):
				g.dependents[t] = append(g.dependents[t], f)
			}
		}
		for _, im := range imports {
			t, ok := resolveLink(f, im)
			switch {
			case !ok:
				unresolved = true
			case t != "" && !slices.Contains(g.importers[t], f):
				g.importers[t] = append(g.importers[t], f)
			}
		}
		if unresolved {
			g.opaque = append(g.opaque, f)
		}
	}
	return g
}

// seed adds to out the projects that evaluate file: the project itself, every project below
// the directory of a convention import, and the projects reached through the files importing
// it. seen holds the files already walked. It reports whether file could be attributed; a
// .props or .targets no project evaluates cannot be, and a caller restores everything then.
func (g *msbuildGraph) seed(file string, seen, out map[string]bool) bool {
	if seen[file] {
		return true
	}
	seen[file] = true
	found := false
	switch {
	case isProject(file):
		out[file], found = true, true
	case isConventionImport(file):
		// MSBuild imports the nearest convention file only, so this is a superset.
		found = true
		d := path.Dir(file)
		for _, p := range g.projects {
			if d == "." || strings.HasPrefix(p, d+"/") {
				out[p] = true
			}
		}
	}
	for _, im := range g.importers[file] {
		found = g.seed(im, seen, out) || found
	}
	return found
}

// addDependents adds to out every project that reaches one of its projects through a chain of
// ProjectReferences: a lockfile holds the packages of the referenced projects too.
func (g *msbuildGraph) addDependents(out map[string]bool) {
	queue := slices.Collect(maps.Keys(out))
	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, d := range g.dependents[p] {
			if !out[d] {
				out[d] = true
				queue = append(queue, d)
			}
		}
	}
}

// restoreTargets returns the lockfiles of files that the patched manifests can move, sorted:
// those of the projects owning a patched file, of the projects below the directory of a
// patched convention import (Directory.Packages.props), of the projects importing a patched
// .props or .targets, and of the projects reaching any of them through a ProjectReference.
// Without manifests (lock file maintenance), with a patched file that is not an MSBuild file
// or with one no project evaluates, every lockfile is a target, as before.
func restoreTargets(dir string, manifests, files map[string][]byte) []string {
	names := slices.Sorted(maps.Keys(files))
	if len(manifests) == 0 || len(names) == 0 {
		return names
	}
	for m := range manifests {
		if !isMSBuild(m) {
			return names
		}
	}
	g := newMSBuildGraph(dir, manifests)
	seen, out := map[string]bool{}, map[string]bool{}
	for _, m := range slices.Sorted(maps.Keys(manifests)) {
		if !g.seed(m, seen, out) {
			return names
		}
	}
	for _, f := range g.opaque {
		g.seed(f, seen, out)
	}
	g.addDependents(out)
	dirs := make(map[string]bool, len(out))
	for p := range out {
		dirs[path.Dir(p)] = true
	}
	return slices.DeleteFunc(names, func(l string) bool { return !dirs[path.Dir(l)] })
}
