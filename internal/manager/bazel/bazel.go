// Package bazel implements the bazel-module manager (Bazel module files: `MODULE.bazel` and the
// `*.MODULE.bazel` segments they include) and the bazelisk manager (`.bazelversion`), and
// updates `MODULE.bazel.lock` with `bazel mod deps` (see Locker).
package bazel

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/imageref"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "bazel-module"

// moduleFile is the root module file; segments are `<name>.MODULE.bazel`.
const moduleFile = "MODULE.bazel"

// Dep types (Renovate's).
const (
	DepTypeBazelDep                = "bazel_dep"
	DepTypeSingleVersionOverride   = "single_version_override"
	DepTypeMultipleVersionOverride = "multiple_version_override"
	DepTypeGitOverride             = "git_override"
	DepTypeArchiveOverride         = "archive_override"
	DepTypeLocalPathOverride       = "local_path_override"
	DepTypeGitRepository           = "git_repository"
	DepTypeNewGitRepository        = "new_git_repository"
	DepTypeOCIPull                 = "oci_pull"
	DepTypeRulesImgPull            = "rules_img_pull"
	DepTypeMavenInstall            = "maven_install"
	DepTypeCrateSpec               = "crate_spec"
)

// Datasources and versionings of the deps.
const (
	dsBazel   = "bazel"
	dsGitTags = "git-tags"
	dsGitRefs = "git-refs"
	dsDocker  = "docker"
	dsMaven   = "maven"
	dsCrate   = "crate"

	versioningBazel = "bazel-module"
)

// defaultBranch is the version of a commit pin without a tag: git-refs' default branch head.
const defaultBranch = "HEAD"

// Skip reasons.
const (
	skipNoVersion     = "no version"
	skipNonLiteral    = "non-literal value"
	skipPinned        = "pinned by single_version_override"
	skipVersionPin    = "version override"
	skipRegistryOnly  = "registry override"
	skipArchive       = "archive override"
	skipLocal         = "local path override"
	skipMultiple      = "multiple versions"
	skipRemote        = "unrecognized remote"
	skipBranch        = "branch reference"
	skipGitRef        = "no commit or tag"
	skipImage         = "invalid image"
	skipDigestNoTag   = "digest without tag"
	skipNoTag         = "no tag"
	skipCoordinates   = "invalid coordinates"
	skipCratePath     = "path dependency"
	skipCrateGit      = "git dependency"
	skipRequirement   = "unsupported requirement"
	skipOverriddenFmt = "overridden by %s"
)

type moduleManager struct{}

// New returns the bazel-module manager.
func New() manager.Manager { return moduleManager{} }

func (moduleManager) Name() string { return Name }

// Match reports whether the slash path is a `MODULE.bazel` or a `<name>.MODULE.bazel` segment
// (Renovate's `(^|/|\.)MODULE\.bazel$`).
func (moduleManager) Match(p string) bool {
	base := path.Base(p)
	return base == moduleFile || strings.HasSuffix(base, "."+moduleFile)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (moduleManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{moduleFile}, Exts: []string{".module.bazel"}}
}

// extractor walks the calls of one module file.
type extractor struct {
	file  string
	lines []int
	deps  []dep.Dep
	exts  map[string]string   // variable -> "maven", "oci" or "crate" (use_extension)
	rules map[string]string   // variable -> "git_repository", "new_git_repository" or "img" (use_repo_rule)
	maven map[string][]string // maven.install name -> its repositories, in order
}

// Extract returns the deps of src in source order: `bazel_dep`s, the overrides (as deps of
// their own; Link applies them to the bazel_deps), `git_repository`/`new_git_repository` repo
// rules, and the `oci.pull`, rules_img `pull`, `maven.install`/`maven.artifact` and `crate.spec`
// extension tags.
func (moduleManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	x := &extractor{file: file, lines: yamlspan.LineStarts(src), exts: map[string]string{}, rules: map[string]string{}, maven: map[string][]string{}}
	cs := calls(tokenize(src))
	// maven.install repositories apply to every artifact of the same install name, also to
	// maven.artifact tags before the install.
	for _, c := range cs {
		if c.fn == "use_extension" {
			x.useExtension(c)
		} else if c.fn == "install" && x.exts[c.recv] == "maven" {
			name := mavenName(c)
			for _, r := range c.kwargs["repositories"].items {
				if r.plain && !slices.Contains(x.maven[name], r.text) {
					x.maven[name] = append(x.maven[name], r.text)
				}
			}
		}
	}
	for _, c := range cs {
		switch {
		case c.recv == "" && c.fn == "use_repo_rule":
			x.useRepoRule(c)
		case c.recv == "" && c.fn == "bazel_dep":
			x.bazelDep(c)
		case c.recv == "" && isOverride(c.fn):
			x.override(c)
		case c.recv == "" && (x.rules[c.fn] == DepTypeGitRepository || x.rules[c.fn] == DepTypeNewGitRepository):
			x.gitRepository(c, x.rules[c.fn])
		case c.recv == "" && x.rules[c.fn] == "img":
			x.imgPull(c)
		case c.recv != "" && x.exts[c.recv] == "oci" && c.fn == "pull":
			x.ociPull(c)
		case c.recv != "" && x.exts[c.recv] == "maven" && c.fn == "install":
			x.mavenInstall(c)
		case c.recv != "" && x.exts[c.recv] == "maven" && c.fn == "artifact":
			x.mavenArtifact(c)
		case c.recv != "" && x.exts[c.recv] == "crate" && c.fn == "spec":
			x.crateSpec(c)
		}
	}
	return x.deps, nil
}

func isOverride(fn string) bool {
	switch fn {
	case DepTypeSingleVersionOverride, DepTypeMultipleVersionOverride, DepTypeGitOverride, DepTypeArchiveOverride, DepTypeLocalPathOverride:
		return true
	}
	return false
}

// positional returns the i-th positional string argument of c, or "".
func positional(c call, i int) string {
	if i < len(c.args) && c.args[i].kind == valString && c.args[i].str.plain {
		return c.args[i].str.text
	}
	return ""
}

// label writes the canonical-repository prefix `@@` of a Bazel label as `@` (`@@rules_oci//…`).
func label(s string) string {
	if rest, ok := strings.CutPrefix(s, "@@"); ok {
		return "@" + rest
	}
	return s
}

// useExtension records `x = use_extension("<bzl>", "<name>")` of the extensions whose tags
// hold deps: rules_jvm_external's maven, rules_oci's oci and rules_rust's crate_universe crate.
func (x *extractor) useExtension(c call) {
	if c.assign == "" {
		return
	}
	bzl, name := label(positional(c, 0)), positional(c, 1)
	switch {
	case name == "maven" && strings.HasPrefix(bzl, "@rules_jvm_external//"):
		x.exts[c.assign] = "maven"
	case name == "oci" && strings.HasPrefix(bzl, "@rules_oci//"):
		x.exts[c.assign] = "oci"
	case name == "crate" && strings.HasPrefix(bzl, "@rules_rust//crate_universe"):
		x.exts[c.assign] = "crate"
	}
}

// useRepoRule records `x = use_repo_rule("<bzl>", "<rule>")` of Bazel's git repository rules
// and rules_img's pull.
func (x *extractor) useRepoRule(c call) {
	if c.assign == "" {
		return
	}
	bzl, rule := label(positional(c, 0)), positional(c, 1)
	switch {
	case bzl == "@bazel_tools//tools/build_defs/repo:git.bzl" && (rule == DepTypeGitRepository || rule == DepTypeNewGitRepository):
		x.rules[c.assign] = rule
	case bzl == "@rules_img//img:pull.bzl" && rule == "pull":
		x.rules[c.assign] = "img"
	}
}

// newDep returns a dep of c with the line of the call.
func (x *extractor) newDep(c call, depType, datasource, versioning string) dep.Dep {
	return dep.Dep{Manager: Name, DepType: depType, Datasource: datasource, Versioning: versioning, File: x.file, Line: yamlspan.LineOf(x.lines, c.start)}
}

// setVersion sets the version of d from the keyword argument key of c (RefSpan, Line); it
// returns false and sets Skip when it is missing or not a plain string.
func (x *extractor) setVersion(d *dep.Dep, c call, key string) bool {
	v, sp, ok, set := c.str(key)
	switch {
	case !set || ok && v == "":
		d.Skip = skipNoVersion
	case !ok:
		d.Skip = skipNonLiteral
	default:
		d.Version, d.RefSpan, d.Line = v, sp, yamlspan.LineOf(x.lines, sp.Start)
		return true
	}
	return false
}

// name sets d.Name from the keyword argument key of c; false (Skip set) when it is not a
// plain string.
func (x *extractor) name(d *dep.Dep, c call, key string) bool {
	v, _, ok, _ := c.str(key)
	if !ok || v == "" {
		d.Skip = skipNonLiteral
		return false
	}
	d.Name = v
	return true
}

// bazelDep is `bazel_dep(name = …, version = …)`.
func (x *extractor) bazelDep(c call) {
	d := x.newDep(c, DepTypeBazelDep, dsBazel, versioningBazel)
	if x.name(&d, c, "name") {
		x.setVersion(&d, c, "version")
	}
	x.deps = append(x.deps, d)
}

// override records one override as a dep named after its module_name: single_version_override
// keeps its version (not updated: pinned on purpose, often patched) and registry (Source);
// git_override with a commit tracks the default branch (git-refs), with a tag the tags
// (git-tags); the others are skipped.
func (x *extractor) override(c call) {
	d := x.newDep(c, c.fn, "", "")
	if !x.name(&d, c, "module_name") {
		x.deps = append(x.deps, d)
		return
	}
	switch c.fn {
	case DepTypeSingleVersionOverride:
		d.Datasource, d.Versioning = dsBazel, versioningBazel
		if r, _, ok, _ := c.str("registry"); ok {
			d.Source = r
		}
		d.Skip = skipRegistryOnly
		if v, sp, ok, _ := c.str("version"); ok && v != "" {
			d.Version, d.RefSpan, d.Line, d.Skip = v, sp, yamlspan.LineOf(x.lines, sp.Start), skipVersionPin
		}
	case DepTypeMultipleVersionOverride:
		d.Skip = skipMultiple
	case DepTypeArchiveOverride:
		d.Skip = skipArchive
	case DepTypeLocalPathOverride:
		d.Skip = skipLocal
	case DepTypeGitOverride:
		x.gitRef(&d, c)
	}
	x.deps = append(x.deps, d)
}

// gitRef fills a git_override or git_repository dep from its remote and commit or tag: a tag
// (with a commit, both are updated) is a git-tags dep, a commit alone tracks the default
// branch head (git-refs digest updates).
func (x *extractor) gitRef(d *dep.Dep, c call) {
	remote, _, ok, _ := c.str("remote")
	if !ok {
		d.Skip = skipRemote
		return
	}
	name, source := manager.GitRepo(remote)
	if source == "" {
		d.Skip = skipRemote
		return
	}
	if d.DepType == DepTypeGitRepository || d.DepType == DepTypeNewGitRepository {
		d.Name = name
	}
	d.Source, d.Versioning = source, "tag"
	commit, csp, hasCommit, commitSet := c.str("commit")
	tag, tsp, hasTag, tagSet := c.str("tag")
	switch {
	case commitSet && !hasCommit || tagSet && !hasTag:
		d.Skip = skipNonLiteral
	case hasTag && tag != "":
		d.Datasource, d.Version, d.RefSpan, d.Line = dsGitTags, tag, tsp, yamlspan.LineOf(x.lines, tsp.Start)
		if hasCommit && commit != "" {
			d.Digest, d.DigestSpan = commit, csp
		}
	case hasCommit && manager.IsHex(commit, 40):
		d.Datasource, d.Version, d.Digest, d.DigestSpan, d.Line = dsGitRefs, defaultBranch, commit, csp, yamlspan.LineOf(x.lines, csp.Start)
	case hasCommit:
		d.Skip = skipGitRef
	default:
		if _, _, _, branch := c.str("branch"); branch {
			d.Skip = skipBranch
		} else {
			d.Skip = skipGitRef
		}
	}
}

// gitRepository is a `git_repository`/`new_git_repository` repo rule: named after the remote's
// path, like a git_override.
func (x *extractor) gitRepository(c call, depType string) {
	d := x.newDep(c, depType, "", "")
	x.gitRef(&d, c)
	if d.Name == "" {
		d.Name, _, _, _ = c.str("remote")
	}
	x.deps = append(x.deps, d)
}

// image fills an image dep from the repository written as image, its tag and digest
// arguments (docker-style updates: the tag, and a digest that is already there).
func (x *extractor) image(d *dep.Dep, image string, c call) {
	r, err := imageref.Parse(image)
	if err != nil || r.Tag != "" || r.Digest != "" {
		d.Name, d.Skip = image, skipImage
		return
	}
	d.Name, d.Source = r.Familiar, r.Name
	tag, tsp, hasTag, tagSet := c.str("tag")
	digest, dsp, hasDigest, digestSet := c.str("digest")
	switch {
	case tagSet && !hasTag || digestSet && !hasDigest:
		d.Skip = skipNonLiteral
	case tag == "" && digest != "":
		d.Skip = skipDigestNoTag
	case tag == "":
		d.Skip = skipNoTag
	default:
		d.Version, d.RefSpan, d.Line = tag, tsp, yamlspan.LineOf(x.lines, tsp.Start)
		if digest != "" {
			d.Digest, d.DigestSpan = digest, dsp
		}
	}
}

// ociPull is rules_oci's `oci.pull(image = …, tag = …, digest = …)`.
func (x *extractor) ociPull(c call) {
	d := x.newDep(c, DepTypeOCIPull, dsDocker, "docker")
	if img, _, ok, _ := c.str("image"); ok {
		x.image(&d, strings.TrimPrefix(strings.TrimPrefix(img, "https://"), "http://"), c)
	} else {
		d.Skip = skipNonLiteral
	}
	x.deps = append(x.deps, d)
}

// imgPull is rules_img's `pull(registry = …, repository = …, tag = …, digest = …)`; without a
// registry the repository is on Docker Hub.
func (x *extractor) imgPull(c call) {
	d := x.newDep(c, DepTypeRulesImgPull, dsDocker, "docker")
	repo, _, ok, _ := c.str("repository")
	reg, _, okReg, regSet := c.str("registry")
	switch {
	case !ok || regSet && !okReg:
		d.Skip = skipNonLiteral
	case reg != "":
		x.image(&d, reg+"/"+repo, c)
	default:
		x.image(&d, repo, c)
	}
	x.deps = append(x.deps, d)
}

// mavenName is the repository name of a maven.install or maven.artifact tag (`name`, default
// `maven`).
func mavenName(c call) string {
	if n, _, ok, _ := c.str("name"); ok && n != "" {
		return n
	}
	return "maven"
}

// mavenSource renders the repositories of the install name as the maven datasource's source
// (each with a trailing "/"); "" (Maven Central, rules_jvm_external's default) without any.
func (x *extractor) mavenSource(c call) string {
	repos := x.maven[mavenName(c)]
	out := make([]string, len(repos))
	for i, r := range repos {
		out[i] = strings.TrimSuffix(r, "/") + "/"
	}
	return strings.Join(out, " ")
}

// mavenInstall is `maven.install(artifacts = ["group:artifact[:packaging[:classifier]]:version", …])`:
// one dep per artifact, the version being the last coordinate.
func (x *extractor) mavenInstall(c call) {
	arts, ok := c.kwargs["artifacts"]
	if !ok {
		return
	}
	if arts.kind != valList {
		d := x.newDep(c, DepTypeMavenInstall, dsMaven, "maven")
		d.Name, d.Skip = "artifacts", skipNonLiteral
		x.deps = append(x.deps, d)
		return
	}
	src := x.mavenSource(c)
	for _, a := range arts.items {
		d := x.newDep(c, DepTypeMavenInstall, dsMaven, "maven")
		d.Source, d.Line = src, yamlspan.LineOf(x.lines, a.start)
		parts := strings.Split(a.text, ":")
		switch {
		case !a.plain:
			d.Name, d.Skip = a.text, skipNonLiteral
		case len(parts) < 2 || len(parts) > 5 || slices.Contains(parts, ""):
			d.Name, d.Skip = a.text, skipCoordinates
		case len(parts) == 2:
			d.Name, d.Skip = a.text, skipNoVersion // managed by a BOM
		default:
			d.Name = parts[0] + ":" + parts[1]
			v := parts[len(parts)-1]
			d.Version = v
			d.RefSpan = dep.Span{Start: a.valEnd - len(v), End: a.valEnd}
		}
		x.deps = append(x.deps, d)
	}
}

// mavenArtifact is `maven.artifact(group = …, artifact = …, version = …)`.
func (x *extractor) mavenArtifact(c call) {
	d := x.newDep(c, DepTypeMavenInstall, dsMaven, "maven")
	d.Source = x.mavenSource(c)
	g, _, okG, _ := c.str("group")
	a, _, okA, _ := c.str("artifact")
	if !okG || !okA || g == "" || a == "" {
		d.Skip = skipNonLiteral
		if okG && okA {
			d.Skip = skipCoordinates
		}
		d.Name = g + ":" + a
		x.deps = append(x.deps, d)
		return
	}
	d.Name = g + ":" + a
	x.setVersion(&d, c, "version")
	x.deps = append(x.deps, d)
}

// crateSpec is crate_universe's `crate.spec(package = …, version = …)`: the version is a Cargo
// requirement, updated like the cargo manager's; `git` crates with a `tag` are git-tags deps,
// other git and `path` crates are skipped.
func (x *extractor) crateSpec(c call) {
	d := x.newDep(c, DepTypeCrateSpec, dsCrate, "cargo")
	if !x.name(&d, c, "package") {
		x.deps = append(x.deps, d)
		return
	}
	_, _, _, local := c.str("path")
	git, _, okGit, gitSet := c.str("git")
	switch {
	case local:
		d.Skip = skipCratePath
	case gitSet:
		tag, tsp, okTag, _ := c.str("tag")
		_, source := manager.GitRepo(git)
		if !okGit || !okTag || tag == "" || source == "" {
			d.Skip = skipCrateGit
			break
		}
		d.Datasource, d.Versioning, d.Source = dsGitTags, "tag", source
		d.Version, d.RefSpan, d.Line = tag, tsp, yamlspan.LineOf(x.lines, tsp.Start)
	case x.setVersion(&d, c, "version"):
		if !cargo.SetRequirement(&d, d.Version) {
			d.Skip = skipRequirement
		}
	}
	x.deps = append(x.deps, d)
}

// Edits replaces the version string, the commit or image digest, and rewrites a crate.spec
// requirement per the update's rangeStrategy.
func (moduleManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range != "" {
		return cargo.New().Edits(u)
	}
	var edits []patch.Edit
	if u.NewVersion != d.Version {
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	switch {
	case u.NewDigest == "" && d.Digest != "":
		return nil, errors.New("cannot drop the digest")
	case u.NewDigest == "":
	case d.Digest == "":
		return nil, fmt.Errorf("cannot pin %s: no digest argument to update", d.Name)
	default:
		edits = append(edits, patch.Edit{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: u.NewDigest})
	}
	return edits, nil
}

// NewRange implements manager.RangeRewriter for crate.spec requirements (the cargo manager's
// rewrite).
func (moduleManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return cargo.New().(manager.RangeRewriter).NewRange(d, v, strategy)
}

// Link applies the overrides of each workspace to its bazel_deps: files share a workspace when
// the nearest directory holding a scanned MODULE.bazel is the same (a root module and the
// segments it includes). For a module with exactly one override (Bazel allows no more), the
// bazel_dep is skipped unless the override is a single_version_override without a version,
// whose registry becomes the bazel_dep's source. A single_version_override without a version
// is no dep and is dropped.
func (moduleManager) Link(files [][]dep.Dep) {
	roots := map[string]bool{}
	for _, f := range files {
		if len(f) > 0 && path.Base(f[0].File) == moduleFile {
			roots[path.Dir(f[0].File)] = true
		}
	}
	workspace := func(file string) string {
		for dir := path.Dir(file); ; dir = path.Dir(dir) {
			if roots[dir] {
				return dir
			}
			if dir == "." || dir == "/" {
				return path.Dir(file)
			}
		}
	}
	type ref struct{ file, i int }
	overrides := map[string][]ref{} // workspace + "\x00" + module
	for fi, f := range files {
		for i, d := range f {
			if isOverride(d.DepType) && d.Name != "" {
				k := workspace(d.File) + "\x00" + d.Name
				overrides[k] = append(overrides[k], ref{fi, i})
			}
		}
	}
	for _, f := range files {
		for i := range f {
			d := &f[i]
			if d.DepType != DepTypeBazelDep || d.Name == "" {
				continue
			}
			ovs := overrides[workspace(d.File)+"\x00"+d.Name]
			if len(ovs) != 1 {
				continue
			}
			o := files[ovs[0].file][ovs[0].i]
			switch {
			case o.DepType != DepTypeSingleVersionOverride:
				d.Skip = fmt.Sprintf(skipOverriddenFmt, o.DepType)
			case o.Version != "":
				d.Skip = skipPinned
				d.Source = o.Source
			default:
				d.Source = o.Source
			}
		}
	}
	for k, f := range files {
		files[k] = slices.DeleteFunc(f, func(d dep.Dep) bool {
			return d.DepType == DepTypeSingleVersionOverride && d.Skip == skipRegistryOnly
		})
	}
}
