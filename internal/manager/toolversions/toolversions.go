// Package toolversions extracts tool versions from version-manager files: `.nvmrc` (nvm),
// `.node-version` (nodenv), `.tool-versions` (asdf) and `mise.toml` (mise). Each file format
// is its own manager; they share the tool table and the version classification.
package toolversions

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Manager names (Renovate's).
const (
	NameNvm    = "nvm"
	NameNodenv = "nodenv"
	NameAsdf   = "asdf"
	NameMise   = "mise"
)

// Dep types: the Node.js version of an nvm/nodenv file, and one tool of asdf/mise.
const (
	DepTypeNode = "node"
	DepTypeTool = "tool"
)

var (
	nvmRe    = regexp.MustCompile(`(^|/)\.nvmrc$`)
	nodenvRe = regexp.MustCompile(`(^|/)\.node-version$`)
	asdfRe   = regexp.MustCompile(`(^|/)\.tool-versions$`)
	miseRe   = regexp.MustCompile(`(^|/)(\.?mise\.toml|\.?mise/config\.toml|\.config/mise\.toml|\.config/mise/config\.toml)$`)
)

// toolManager is one file format: its path pattern and extractor. Edits are shared: every
// dep is a single version token.
type toolManager struct {
	name    string
	match   *regexp.Regexp
	extract func(file string, src []byte) ([]dep.Dep, error)
}

// NewNvm returns the nvm manager (`.nvmrc`).
func NewNvm() manager.Manager {
	return &toolManager{name: NameNvm, match: nvmRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractNode(NameNvm, file, src), nil
	}}
}

// NewNodenv returns the nodenv manager (`.node-version`).
func NewNodenv() manager.Manager {
	return &toolManager{name: NameNodenv, match: nodenvRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractNode(NameNodenv, file, src), nil
	}}
}

// NewAsdf returns the asdf manager (`.tool-versions`).
func NewAsdf() manager.Manager {
	return &toolManager{name: NameAsdf, match: asdfRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractAsdf(file, src), nil
	}}
}

// NewMise returns the mise manager (`mise.toml` and its alternative locations).
func NewMise() manager.Manager {
	return &toolManager{name: NameMise, match: miseRe, extract: extractMise}
}

func (m *toolManager) Name() string { return m.name }

// Match reports whether the slash path is a file of this manager.
func (m *toolManager) Match(path string) bool { return m.match.MatchString(path) }

// Extract returns the tool versions of src with byte spans into src.
func (m *toolManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	return m.extract(file, src)
}

// Edits replaces the version token; quotes and the precision chosen by the versioning stay.
func (*toolManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// tool is the dep a tool name resolves to. skip, when set, is the reason a known tool is not
// updatable.
type tool struct{ datasource, name, source, skip string }

const (
	dsNode    = "node-version"
	dsGo      = "go-toolchain"
	dsGitHub  = "github-releases"
	dsNpm     = "npm"
	dsMaven   = "maven"
	dsGradle  = "gradle-version"
	skipTool  = "unsupported tool"
	skipTagPx = "unsupported tag prefix"
)

var (
	nodeTool = tool{datasource: dsNode, name: "node"}
	goTool   = tool{datasource: dsGo, name: "go"}
	// The Maven distribution and Gradle use Maven's version order (3.9.9, 8.11, 8.11-rc-1).
	mavenTool  = tool{datasource: dsMaven, name: "org.apache.maven:apache-maven"}
	gradleTool = tool{datasource: dsGradle, name: "gradle"}
)

func githubTool(repo string) tool {
	return tool{datasource: dsGitHub, name: repo, source: "https://github.com/" + repo}
}

func npmTool(pkg string) tool { return tool{datasource: dsNpm, name: pkg} }

// tools maps asdf plugin and mise tool names to their dep. github-releases repositories are
// listed only when their release tags are the version with or without a leading "v" (the
// datasource's tag match); repositories with other tag prefixes are known but skipped.
var tools = map[string]tool{
	"node":          nodeTool,
	"nodejs":        nodeTool,
	"go":            goTool,
	"golang":        goTool,
	"npm":           npmTool("npm"),
	"pnpm":          npmTool("pnpm"),
	"maven":         mavenTool,
	"gradle":        gradleTool,
	"actionlint":    githubTool("rhysd/actionlint"),
	"age":           githubTool("FiloSottile/age"),
	"argocd":        githubTool("argoproj/argo-cd"),
	"buf":           githubTool("bufbuild/buf"),
	"cosign":        githubTool("sigstore/cosign"),
	"crane":         githubTool("google/go-containerregistry"),
	"deno":          githubTool("denoland/deno"),
	"flux":          githubTool("fluxcd/flux2"),
	"flux2":         githubTool("fluxcd/flux2"),
	"gh":            githubTool("cli/cli"),
	"github-cli":    githubTool("cli/cli"),
	"gitleaks":      githubTool("gitleaks/gitleaks"),
	"golangci-lint": githubTool("golangci/golangci-lint"),
	"goreleaser":    githubTool("goreleaser/goreleaser"),
	"hadolint":      githubTool("hadolint/hadolint"),
	"helm":          githubTool("helm/helm"),
	"helm-docs":     githubTool("norwoodj/helm-docs"),
	"helmfile":      githubTool("helmfile/helmfile"),
	"just":          githubTool("casey/just"),
	"k3d":           githubTool("k3d-io/k3d"),
	"k9s":           githubTool("derailed/k9s"),
	"kind":          githubTool("kubernetes-sigs/kind"),
	"ko":            githubTool("ko-build/ko"),
	"kubeconform":   githubTool("yannh/kubeconform"),
	"kubectl":       githubTool("kubernetes/kubernetes"),
	"opentofu":      githubTool("opentofu/opentofu"),
	"packer":        githubTool("hashicorp/packer"),
	"ripgrep":       githubTool("BurntSushi/ripgrep"),
	"shellcheck":    githubTool("koalaman/shellcheck"),
	"skaffold":      githubTool("GoogleContainerTools/skaffold"),
	"sops":          githubTool("getsops/sops"),
	"stern":         githubTool("stern/stern"),
	"task":          githubTool("go-task/task"),
	"terraform":     githubTool("hashicorp/terraform"),
	"terragrunt":    githubTool("gruntwork-io/terragrunt"),
	"tflint":        githubTool("terraform-linters/tflint"),
	"trivy":         githubTool("aquasecurity/trivy"),
	"vault":         githubTool("hashicorp/vault"),
	"yq":            githubTool("mikefarah/yq"),
	// Tags `bun-v1.2.3`, `jq-1.7.1`, `kustomize/v5.4.3`.
	"bun":       {datasource: dsGitHub, name: "oven-sh/bun", source: "https://github.com/oven-sh/bun", skip: skipTagPx},
	"jq":        {datasource: dsGitHub, name: "jqlang/jq", source: "https://github.com/jqlang/jq", skip: skipTagPx},
	"kustomize": {datasource: dsGitHub, name: "kubernetes-sigs/kustomize", source: "https://github.com/kubernetes-sigs/kustomize", skip: skipTagPx},
}

// lookup returns the dep of tool name; an unknown name is kept as the dep name and skipped.
func lookup(name string) tool {
	if t, ok := tools[name]; ok {
		return t
	}
	return tool{name: name, skip: skipTool}
}

var (
	// semverRe is a full MAJOR.MINOR.PATCH with an optional prerelease; partialRe has one or
	// two parts; goRe is a go-toolchain X.Y or X.Y.Z (after a stripped "v").
	semverRe  = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	partialRe = regexp.MustCompile(`^v?\d+(?:\.\d+)?$`)
	goRe      = regexp.MustCompile(`^\d+\.\d+(?:\.\d+)?$`)
	// mavenRe is a version of the maven versioning scheme (a digit, then [0-9A-Za-z._-]).
	mavenRe = regexp.MustCompile(`^\d[0-9A-Za-z._-]*$`)
	// wordRe is a letter-led value that is not a v-prefixed version (latest, system, lts/*).
	wordRe = regexp.MustCompile(`^(?:[A-Za-uw-z]|v(?:$|\D))`)
)

// classify returns the version token of raw for t (ver at byte offset off in raw), its
// versioning, or the reason raw is not updatable. Partial versions move at their precision
// under tag versioning; the datasources of node and go list derived X.Y/X candidates without
// "v", so a leading "v" stays outside the token.
func classify(t tool, raw string) (ver string, off int, versioning, skip string) {
	switch {
	case strings.HasPrefix(raw, "ref:"):
		return "", 0, "", "git ref"
	case strings.HasPrefix(raw, "path:"):
		return "", 0, "", "local path"
	case strings.HasPrefix(raw, "prefix:"):
		return "", 0, "", "unsupported range"
	case raw == "" || wordRe.MatchString(raw):
		return "", 0, "", "non-version value"
	}
	bare := strings.TrimPrefix(raw, "v")
	off = len(raw) - len(bare)
	if t.datasource == dsGo {
		switch {
		case goRe.MatchString(bare):
			return bare, off, "tag", ""
		case partialRe.MatchString(raw):
			return "", 0, "", "partial version"
		}
		return "", 0, "", "unsupported range"
	}
	if t.datasource == dsMaven || t.datasource == dsGradle {
		if mavenRe.MatchString(raw) {
			return raw, 0, "maven", ""
		}
		return "", 0, "", "unsupported range"
	}
	switch {
	case semverRe.MatchString(raw):
		return raw, 0, "semver", ""
	case !partialRe.MatchString(raw):
		return "", 0, "", "unsupported range"
	case t.datasource == dsNode:
		return bare, off, "tag", ""
	}
	return "", 0, "", "partial version"
}

// newDep builds the dep of t's version raw found at byte offset at of the file.
func newDep(mgr, depType, file string, line int, t tool, raw string, at int) dep.Dep {
	d := dep.Dep{
		Manager: mgr, Datasource: t.datasource, DepType: depType, File: file, Line: line,
		Name: t.name, Source: t.source, Version: raw, Skip: t.skip,
	}
	if d.Skip != "" {
		return d
	}
	ver, off, versioning, skip := classify(t, raw)
	if skip != "" {
		d.Skip = skip
		return d
	}
	d.Version, d.Versioning = ver, versioning
	d.RefSpan = dep.Span{Start: at + off, End: at + off + len(ver)}
	return d
}

// lineStarts returns the byte offset of every line of src.
func lineStarts(src []byte) []int {
	ls := []int{0}
	for i, b := range src {
		if b == '\n' {
			ls = append(ls, i+1)
		}
	}
	return ls
}

// lineAt returns the 1-based line of byte offset off given lineStarts ls.
func lineAt(ls []int, off int) int {
	n, _ := slices.BinarySearch(ls, off+1)
	return n
}

// bom is the UTF-8 byte order mark, skipped at the start of a file.
var bom = []byte("\ufeff")

// eachLine calls fn with every line of src (without "\n" and a trailing "\r"), its 1-based
// number and its byte offset, until fn returns false. A leading BOM is skipped.
func eachLine(src []byte, fn func(l []byte, n, off int) bool) {
	off := 0
	if bytes.HasPrefix(src, bom) {
		off = len(bom)
	}
	for n := 1; off <= len(src); n++ {
		end := len(src)
		if i := bytes.IndexByte(src[off:], '\n'); i >= 0 {
			end = off + i
		}
		l := src[off:end]
		if len(l) > 0 && l[len(l)-1] == '\r' {
			l = l[:len(l)-1]
		}
		if !fn(l, n, off) || end == len(src) {
			return
		}
		off = end + 1
	}
}

// fields returns the [start,end) offsets of the first (up to) two blank-separated tokens of l
// before a `#` comment, and how many there are.
func fields(l []byte) (f [2][2]int, n int) {
	i := 0
	for n < len(f) {
		for i < len(l) && (l[i] == ' ' || l[i] == '\t') {
			i++
		}
		if i == len(l) || l[i] == '#' {
			break
		}
		j := i
		for j < len(l) && l[j] != ' ' && l[j] != '\t' && l[j] != '#' {
			j++
		}
		f[n], n, i = [2]int{i, j}, n+1, j
	}
	return f, n
}
