// Package toolversions extracts tool versions from version-manager files: `.nvmrc` (nvm),
// `.node-version` (nodenv), `.ruby-version` (ruby-version), `.python-version` (pyenv),
// `runtime.txt` (runtime-version), `.tool-versions` (asdf) and `mise.toml` (mise). Each file
// format is its own manager; they share the tool table and the version classification.
package toolversions

import (
	"bytes"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Manager names (Renovate's).
const (
	NameNvm            = "nvm"
	NameNodenv         = "nodenv"
	NameRubyVersion    = "ruby-version"
	NamePyenv          = "pyenv"
	NameRuntimeVersion = "runtime-version"
	NameAsdf           = "asdf"
	NameMise           = "mise"
)

// Dep types: the Node.js version of an nvm/nodenv file, the Ruby version of a ruby-version
// file, the Python version of a pyenv/runtime-version file, and one tool of asdf/mise.
const (
	DepTypeNode   = "node"
	DepTypeRuby   = "ruby"
	DepTypePython = "python"
	DepTypeTool   = "tool"
)

var (
	nvmRe         = regexp.MustCompile(`(^|/)\.nvmrc$`)
	nodenvRe      = regexp.MustCompile(`(^|/)\.node-version$`)
	rubyVersionRe = regexp.MustCompile(`(^|/)\.ruby-version$`)
	pyenvRe       = regexp.MustCompile(`(^|/)\.python-version$`)
	runtimeRe     = regexp.MustCompile(`(^|/)runtime\.txt$`)
	asdfRe        = regexp.MustCompile(`(^|/)\.tool-versions$`)
	miseRe        = regexp.MustCompile(`(^|/)(\.?mise\.toml|\.?mise/config\.toml|\.config/mise\.toml|\.config/mise/config\.toml)$`)
)

// toolManager is one file format: its path pattern and extractor. Edits are shared: every
// dep is a single version token.
type toolManager struct {
	name    string
	suffix  string // every path match ends in it; skips the regexp for other paths
	match   *regexp.Regexp
	extract func(file string, src []byte) ([]dep.Dep, error)
}

// NewNvm returns the nvm manager (`.nvmrc`).
func NewNvm() manager.Manager {
	return &toolManager{name: NameNvm, suffix: ".nvmrc", match: nvmRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractFirst(NameNvm, DepTypeNode, nodeTool, file, src), nil
	}}
}

// NewNodenv returns the nodenv manager (`.node-version`).
func NewNodenv() manager.Manager {
	return &toolManager{name: NameNodenv, suffix: ".node-version", match: nodenvRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractFirst(NameNodenv, DepTypeNode, nodeTool, file, src), nil
	}}
}

// NewRubyVersion returns the ruby-version manager (`.ruby-version`, read by rbenv, chruby,
// rvm, asdf, mise and Bundler's `ruby file:`).
func NewRubyVersion() manager.Manager {
	return &toolManager{name: NameRubyVersion, suffix: ".ruby-version", match: rubyVersionRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractFirst(NameRubyVersion, DepTypeRuby, rubyTool, file, src), nil
	}}
}

// NewPyenv returns the pyenv manager (`.python-version`, read by pyenv, uv, asdf and mise).
func NewPyenv() manager.Manager {
	return &toolManager{name: NamePyenv, suffix: ".python-version", match: pyenvRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractFirst(NamePyenv, DepTypePython, pythonTool, file, src), nil
	}}
}

// NewRuntimeVersion returns the runtime-version manager (`runtime.txt` of Heroku, Scalingo and
// Binder: `python-3.13.1`).
func NewRuntimeVersion() manager.Manager {
	return &toolManager{name: NameRuntimeVersion, suffix: "runtime.txt", match: runtimeRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractRuntime(file, src), nil
	}}
}

// NewAsdf returns the asdf manager (`.tool-versions`).
func NewAsdf() manager.Manager {
	return &toolManager{name: NameAsdf, suffix: ".tool-versions", match: asdfRe, extract: func(file string, src []byte) ([]dep.Dep, error) {
		return extractAsdf(file, src), nil
	}}
}

// NewMise returns the mise manager (`mise.toml` and its alternative locations).
func NewMise() manager.Manager {
	return &toolManager{name: NameMise, suffix: ".toml", match: miseRe, extract: extractMise}
}

func (m *toolManager) Name() string { return m.name }

// Match reports whether the slash path is a file of this manager.
func (m *toolManager) Match(path string) bool {
	return strings.HasSuffix(path, m.suffix) && m.match.MatchString(path)
}

// Extract returns the tool versions of src with byte spans into src.
func (m *toolManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	return m.extract(file, src)
}

// Edits replaces the version token; quotes and the precision chosen by the versioning stay.
func (*toolManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// FileEdits implements manager.FileEditor: a Ruby update also moves the RUBY VERSION of the
// Gemfile.lock whose Gemfile reads its Ruby version from this file (`ruby file:
// ".ruby-version"`, bundler.VersionFileEdits).
func (*toolManager) FileEdits(root string, u dep.Update) (map[string][]patch.Edit, error) {
	if u.Dep.Datasource != dsRuby || u.NewVersion == u.Dep.Version {
		return nil, nil
	}
	return bundler.VersionFileEdits(root, u.Dep.File, u.NewVersion)
}

// tool is the dep a tool name resolves to. extract is the extractVersion of repositories
// whose release tags carry a prefix; skip, when set, is the reason a known tool is not
// updatable. With bare, a leading "v" of the version stays outside the token (the extracted
// release versions have none). refPrefix is the prefix a version must carry (`tag:` of a mise
// cargo git tool), kept outside the token.
type tool struct {
	datasource, name, source, extract, skip string
	bare                                    bool
	refPrefix                               string
}

const (
	dsNode    = "node-version"
	dsGo      = "go-toolchain"
	dsRuby    = "ruby-version"
	dsPython  = "python-version"
	dsGitHub  = "github-releases"
	dsGitTags = "git-tags"
	dsNpm     = "npm"
	dsMaven   = "maven"
	dsGradle  = "gradle-version"
	dsPypi    = "pypi"
	dsCrate   = "crate"
	dsGoMod   = "go"
	dsDart    = "dart-version"
	dsFlutter = "flutter-version"
	dsDotnet  = "dotnet-version"
	skipTool  = "unsupported tool"
)

var (
	nodeTool   = tool{datasource: dsNode, name: "node"}
	goTool     = tool{datasource: dsGo, name: "go"}
	rubyTool   = tool{datasource: dsRuby, name: "ruby"}
	pythonTool = tool{datasource: dsPython, name: "python"}
	// The Maven distribution and Gradle use Maven's version order (3.9.9, 8.11, 8.11-rc-1).
	mavenTool  = tool{datasource: dsMaven, name: "org.apache.maven:apache-maven"}
	gradleTool = tool{datasource: dsGradle, name: "gradle"}
)

func githubTool(repo string) tool {
	return tool{datasource: dsGitHub, name: repo, source: "https://github.com/" + repo}
}

// prefixedTool is a github-releases tool whose tags carry a prefix extract strips.
func prefixedTool(repo, extract string) tool {
	t := githubTool(repo)
	t.extract = extract
	return t
}

// tagsTool is a tool versioned by the git tags of a github.com repository without releases
// (Renovate's github-tags); extract, when set, strips a tag prefix.
func tagsTool(repo, extract string) tool {
	return tool{datasource: dsGitTags, name: repo, source: "https://github.com/" + repo, extract: extract}
}

func npmTool(pkg string) tool { return tool{datasource: dsNpm, name: pkg} }

func pypiTool(pkg string) tool { return tool{datasource: dsPypi, name: pkg} }

// tools maps asdf plugin and mise tool names to their dep, after Renovate's asdf and mise
// tool tables. github-releases tags are the version with or without a leading "v" (the
// datasource's tag match) unless the tool sets extract.
var tools = map[string]tool{
	"node":                 nodeTool,
	"nodejs":               nodeTool,
	"go":                   goTool,
	"golang":               goTool,
	"npm":                  npmTool("npm"),
	"pnpm":                 npmTool("pnpm"),
	"yarn":                 npmTool("@yarnpkg/cli"),
	"maven":                mavenTool,
	"gradle":               gradleTool,
	"ruby":                 rubyTool,
	"python":               pythonTool,
	"poetry":               pypiTool("poetry"),
	"dart":                 {datasource: dsDart, name: "dart"},
	"flutter":              {datasource: dsFlutter, name: "flutter"},
	"dotnet":               {datasource: dsDotnet, name: "dotnet-sdk"},
	"dotnet-core":          {datasource: dsDotnet, name: "dotnet-sdk"},
	"act":                  githubTool("nektos/act"),
	"actionlint":           githubTool("rhysd/actionlint"),
	"age":                  githubTool("FiloSottile/age"),
	"argocd":               githubTool("argoproj/argo-cd"),
	"asdf-plugin-manager":  githubTool("asdf-community/asdf-plugin-manager"),
	"astro":                githubTool("astronomer/astro-cli"),
	"atmos":                githubTool("cloudposse/atmos"),
	"aws-vault":            githubTool("99designs/aws-vault"),
	"buf":                  githubTool("bufbuild/buf"),
	"caddy":                githubTool("caddyserver/caddy"),
	"cargo-make":           githubTool("sagiegurari/cargo-make"),
	"ccache":               githubTool("ccache/ccache"),
	"clusterctl":           githubTool("kubernetes-sigs/cluster-api"),
	"committed":            githubTool("crate-ci/committed"),
	"conan":                githubTool("conan-io/conan"),
	"conftest":             githubTool("open-policy-agent/conftest"),
	"consul":               githubTool("hashicorp/consul"),
	"cookiecutter":         githubTool("cookiecutter/cookiecutter"),
	"cosign":               githubTool("sigstore/cosign"),
	"crane":                githubTool("google/go-containerregistry"),
	"crystal":              githubTool("crystal-lang/crystal"),
	"deno":                 githubTool("denoland/deno"),
	"detekt":               githubTool("detekt/detekt"),
	"direnv":               githubTool("direnv/direnv"),
	"dotenv-linter":        githubTool("dotenv-linter/dotenv-linter"),
	"dprint":               githubTool("dprint/dprint"),
	"ecspresso":            githubTool("kayac/ecspresso"),
	"editorconfig-checker": githubTool("editorconfig-checker/editorconfig-checker"),
	"elm":                  githubTool("elm/compiler"),
	"flux":                 githubTool("fluxcd/flux2"),
	"flux2":                githubTool("fluxcd/flux2"),
	"gh":                   githubTool("cli/cli"),
	"github-cli":           githubTool("cli/cli"),
	"ginkgo":               githubTool("onsi/ginkgo"),
	"gitleaks":             githubTool("gitleaks/gitleaks"),
	"golangci-lint":        githubTool("golangci/golangci-lint"),
	"gomplate":             githubTool("hairyhenderson/gomplate"),
	"goreleaser":           githubTool("goreleaser/goreleaser"),
	"gotestsum":            githubTool("gotestyourself/gotestsum"),
	"hadolint":             githubTool("hadolint/hadolint"),
	"helm":                 githubTool("helm/helm"),
	"helm-docs":            githubTool("norwoodj/helm-docs"),
	"helmfile":             githubTool("helmfile/helmfile"),
	"hivemind":             githubTool("DarthSim/hivemind"),
	"hk":                   githubTool("jdx/hk"),
	"istioctl":             githubTool("istio/istio"),
	"julia":                githubTool("JuliaLang/julia"),
	"just":                 githubTool("casey/just"),
	"k3d":                  githubTool("k3d-io/k3d"),
	"k3s":                  githubTool("k3s-io/k3s"),
	"k9s":                  githubTool("derailed/k9s"),
	"kind":                 githubTool("kubernetes-sigs/kind"),
	"ko":                   githubTool("ko-build/ko"),
	"kotlin":               githubTool("JetBrains/kotlin"),
	"ktlint":               githubTool("pinterest/ktlint"),
	"kubeconform":          githubTool("yannh/kubeconform"),
	"kubectl":              githubTool("kubernetes/kubernetes"),
	"kubectx":              githubTool("ahmetb/kubectx"),
	"kubens":               githubTool("ahmetb/kubectx"),
	"kubetail":             githubTool("johanhaleby/kubetail"),
	"lefthook":             githubTool("evilmartians/lefthook"),
	"localstack":           githubTool("localstack/localstack"),
	"lua":                  githubTool("lua/lua"),
	"minikube":             githubTool("kubernetes/minikube"),
	"mockery":              githubTool("vektra/mockery"),
	"ocaml":                githubTool("ocaml/ocaml"),
	"oci":                  githubTool("oracle/oci-cli"),
	"opa":                  githubTool("open-policy-agent/opa"),
	"openfga":              githubTool("openfga/openfga"),
	"opentofu":             githubTool("opentofu/opentofu"),
	"packer":               githubTool("hashicorp/packer"),
	"pipx":                 githubTool("pypa/pipx"),
	"pkl":                  githubTool("apple/pkl"),
	"prettier":             githubTool("prettier/prettier"),
	"protoc":               githubTool("protocolbuffers/protobuf"),
	"pulumi":               githubTool("pulumi/pulumi"),
	"redis":                githubTool("redis/redis"),
	"ripgrep":              githubTool("BurntSushi/ripgrep"),
	"ruff":                 githubTool("astral-sh/ruff"),
	"rumdl":                githubTool("rvben/rumdl"),
	"sbt":                  githubTool("sbt/sbt"),
	"shellcheck":           githubTool("koalaman/shellcheck"),
	"shfmt":                githubTool("mvdan/sh"),
	"skaffold":             githubTool("GoogleContainerTools/skaffold"),
	"skeema":               githubTool("skeema/skeema"),
	"sops":                 githubTool("getsops/sops"),
	"steampipe":            githubTool("turbot/steampipe"),
	"stern":                githubTool("stern/stern"),
	"stripe":               githubTool("stripe/stripe-cli"),
	"swiftformat":          githubTool("nicklockwood/SwiftFormat"),
	"swiftlint":            githubTool("realm/SwiftLint"),
	"taplo":                githubTool("tamasfe/taplo"),
	"tart":                 githubTool("cirruslabs/tart"),
	"task":                 githubTool("go-task/task"),
	"terraform":            githubTool("hashicorp/terraform"),
	"terraform-ls":         githubTool("hashicorp/terraform-ls"),
	"terraformer":          githubTool("GoogleCloudPlatform/terraformer"),
	"terragrunt":           githubTool("gruntwork-io/terragrunt"),
	"terramate":            githubTool("terramate-io/terramate"),
	"tflint":               githubTool("terraform-linters/tflint"),
	"tilt":                 githubTool("tilt-dev/tilt"),
	"trivy":                githubTool("aquasecurity/trivy"),
	"tusd":                 githubTool("tus/tusd"),
	"typos":                githubTool("crate-ci/typos"),
	"usage":                githubTool("jdx/usage"),
	"uv":                   githubTool("astral-sh/uv"),
	"vault":                githubTool("hashicorp/vault"),
	"yamlfmt":              githubTool("google/yamlfmt"),
	"yq":                   githubTool("mikefarah/yq"),
	// Not in Renovate's tables; the most frequent `unsupported tool` hits of real mise and
	// asdf configs (coder/coder, BoundaryML/baml, pulumi/pulumi, cloudfoundry, hashicorp).
	"ast-grep":                 githubTool("ast-grep/ast-grep"),
	"bats":                     githubTool("bats-core/bats-core"),
	"changie":                  githubTool("miniscruff/changie"),
	"cmake":                    githubTool("Kitware/CMake"),
	"concourse":                githubTool("concourse/concourse"),
	"doctl":                    githubTool("digitalocean/doctl"),
	"git-cliff":                githubTool("orhun/git-cliff"),
	"gofumpt":                  githubTool("mvdan/gofumpt"),
	"grype":                    githubTool("anchore/grype"),
	"jj":                       githubTool("jj-vcs/jj"),
	"kapp":                     githubTool("carvel-dev/kapp"),
	"lazygit":                  githubTool("jesseduffield/lazygit"),
	"mold":                     githubTool("rui314/mold"),
	"ninja":                    githubTool("ninja-build/ninja"),
	"protoc-gen-go":            githubTool("protocolbuffers/protobuf-go"),
	"sccache":                  githubTool("mozilla/sccache"),
	"syft":                     githubTool("anchore/syft"),
	"terraform-lsp":            githubTool("juliosueiras/terraform-lsp"),
	"tfupdate":                 githubTool("minamijoyo/tfupdate"),
	"vale":                     githubTool("errata-ai/vale"),
	"vendir":                   githubTool("carvel-dev/vendir"),
	"vercel":                   npmTool("vercel"),
	"watchexec":                githubTool("watchexec/watchexec"),
	"ytt":                      githubTool("carvel-dev/ytt"),
	"zizmor":                   githubTool("zizmorcore/zizmor"),
	"adr-tools":                tagsTool("npryce/adr-tools", ""),
	"aws-cli":                  tagsTool("aws/aws-cli", ""),
	"awscli":                   tagsTool("aws/aws-cli", ""),
	"checkov":                  tagsTool("bridgecrewio/checkov", ""),
	"clojure":                  tagsTool("clojure/brew-install", ""),
	"container-structure-test": tagsTool("GoogleContainerTools/container-structure-test", ""),
	"gleam":                    tagsTool("gleam-lang/gleam", ""),
	"idris":                    tagsTool("idris-lang/Idris-dev", ""),
	"kafka":                    tagsTool("apache/kafka", ""),
	"kubebuilder":              tagsTool("kubernetes-sigs/kubebuilder", ""),
	"markdownlint-cli2":        tagsTool("DavidAnson/markdownlint-cli2", ""),
	"nim":                      tagsTool("nim-lang/Nim", ""),
	"perl":                     tagsTool("Perl/perl5", ""),
	"pre-commit":               tagsTool("pre-commit/pre-commit", ""),
	"rebar":                    tagsTool("erlang/rebar3", ""),
	"scala":                    tagsTool("scala/scala", ""),
	"talhelper":                tagsTool("budimanjojo/talhelper", ""),
	"talosctl":                 tagsTool("siderolabs/talos", ""),
	"terraform-docs":           tagsTool("terraform-docs/terraform-docs", ""),
	"tfsec":                    tagsTool("aquasecurity/tfsec", ""),
	"tuist":                    tagsTool("tuist/tuist", ""),
	"yamllint":                 tagsTool("adrienverge/yamllint", ""),
	"zig":                      tagsTool("ziglang/zig", ""),
	// Tags `bun-v1.2.3`, `jq-1.7.1`, `kustomize/v5.4.3` (the repository also tags `api/…`,
	// `kyaml/…`), …; files write the bare version.
	"azure-cli":    prefixedTool("Azure/azure-cli", `^azure-cli-(?<version>\d.*)$`),
	"bun":          prefixedTool("oven-sh/bun", `^bun-v(?<version>\d.*)$`),
	"clang-format": prefixedTool("llvm/llvm-project", `^llvmorg-(?<version>\d.*)$`),
	"jq":           prefixedTool("jqlang/jq", `^jq-(?<version>\d.*)$`),
	"kustomize":    prefixedTool("kubernetes-sigs/kustomize", `^kustomize/v(?<version>\d.*)$`),
	"lychee":       prefixedTool("lycheeverse/lychee", `^lychee-v(?<version>\d.*)$`),
	"maestro":      prefixedTool("mobile-dev-inc/maestro", `^cli-(?<version>\d.*)$`),
	"mimirtool":    prefixedTool("grafana/mimir", `^mimir-(?<version>\d.*)$`),
	"swift":        prefixedTool("swiftlang/swift", `^swift-(?<version>\d.*)-RELEASE$`),
	"php":          tagsTool("php/php-src", `^php-(?<version>\d.*)$`),
	"sqlite":       tagsTool("sqlite/sqlite", `^version-(?<version>\d.*)$`),
}

// lookup returns the dep of tool name; an unknown name is kept as the dep name and skipped.
func lookup(name string) tool {
	if t, ok := tools[name]; ok {
		return t
	}
	return tool{name: name, skip: skipTool}
}

// ToolDep returns the dep of version raw of the asdf/mise tool name (node, go, ruby, …: the
// tool table) found at byte offset at of file, classified as asdf and mise versions are; an
// unknown tool is skipped as unsupported. Other managers naming such tools use it (the
// devcontainer manager's feature versions).
func ToolDep(mgr, depType, file string, line int, name, raw string, at int) dep.Dep {
	return newDep(mgr, depType, file, line, lookup(name), raw, at)
}

// derivesLines are the datasources that list derived X.Y and X release lines themselves
// (node, ruby, python, crate: for partial versions; go-toolchain: X.Y).
var derivesLines = map[string]bool{dsNode: true, dsGo: true, dsRuby: true, dsPython: true, dsCrate: true}

// Partial reports whether d is a partial asdf/mise tool version (`1.55`, `3`) under tag
// versioning whose datasource lists only full versions: the engine then derives the X.Y and
// X release lines from the stable X.Y.Z releases, so the version keeps its precision.
func Partial(d dep.Dep) bool {
	if d.Manager != NameAsdf && d.Manager != NameMise || d.Versioning != "tag" || derivesLines[d.Datasource] {
		return false
	}
	return partialRe.MatchString(d.Version)
}

var (
	// semverRe is a full MAJOR.MINOR.PATCH with an optional prerelease; partialRe has one or
	// two parts; goRe is a go-toolchain X.Y or X.Y.Z (after a stripped "v").
	semverRe  = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	partialRe = regexp.MustCompile(`^v?\d+(?:\.\d+)?$`)
	goRe      = regexp.MustCompile(`^\d+\.\d+(?:\.\d+)?$`)
	// rubyRe is a full Ruby X.Y.Z with an optional prerelease (`3.5.0-preview1`); rubyPartialRe
	// is X or X.Y.
	rubyRe        = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[.-][0-9A-Za-z][0-9A-Za-z.-]*)?$`)
	rubyPartialRe = regexp.MustCompile(`^\d+(?:\.\d+)?$`)
	// pythonRe is a CPython X.Y.Z with an optional PEP 440 pre-release (`3.15.0rc1`) and
	// pythonPartialRe X or X.Y; both may carry the free-threaded build's `t` (`3.13t`), which
	// stays outside the token.
	pythonRe        = regexp.MustCompile(`^(\d+\.\d+\.\d+(?:(?:a|b|rc)\d+)?)t?$`)
	pythonPartialRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)t?$`)
	// pep440Re is a PyPI release X.Y[.Z…] with optional pre, post and dev parts.
	pep440Re = regexp.MustCompile(`^\d+(?:\.\d+)+(?:(?:a|b|rc)\d+)?(?:\.post\d+)?(?:\.dev\d+)?$`)
	// mavenRe is a version of the maven versioning scheme (a digit, then [0-9A-Za-z._-]).
	mavenRe = regexp.MustCompile(`^\d[0-9A-Za-z._-]*$`)
	// wordRe is a letter-led value that is not a v-prefixed version (latest, system, lts/*).
	wordRe = regexp.MustCompile(`^(?:[A-Za-uw-z]|v(?:$|\D))`)
	// nodeRangeRe is a Node.js version with an npm operator (`>=20`, `^20.11.1`) or x-range
	// (`20.x`, `20.11.*`): submatch 1 is the version, the rest stays as written.
	nodeRangeRe = regexp.MustCompile(`^(?:>=|\^|~|=)?v?(\d+(?:\.\d+){0,2})(?:\.[xX*]){0,2}$`)
	// flutterChannelRe is the channel suffix asdf-flutter versions carry (`3.24.5-stable`).
	flutterChannelRe = regexp.MustCompile(`-(?:stable|beta|dev)$`)
	// shaRe is a full git commit hash (a mise `go:` tool built at a commit).
	shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// classify returns the version token of raw for t (ver at byte offset off in raw), its
// versioning, or the reason raw is not updatable. Partial versions move at their precision
// under tag versioning: the datasources of node, go, ruby and python list derived X.Y/X
// candidates without "v", and for the other datasources the engine derives them (Partial), so
// a leading "v" stays outside the token. A Ruby version may carry the `ruby-` engine prefix
// of `.ruby-version` files (`ruby-3.4.7`), which stays outside too. A Python version may end
// in the free-threaded `t` (`3.13t`), which stays outside as well. A Node.js version may carry
// an npm operator or x-range (`>=20`, `20.x`), which stay outside: the bound moves.
func classify(t tool, raw string) (ver string, off int, versioning, skip string) {
	switch {
	case t.refPrefix != "":
		bare, ok := strings.CutPrefix(raw, t.refPrefix)
		if !ok {
			if strings.HasPrefix(raw, "branch:") || strings.HasPrefix(raw, "rev:") {
				return "", 0, "", "git ref"
			}
			return "", 0, "", "unsupported range"
		}
		t.refPrefix = ""
		ver, off, versioning, skip = classify(t, bare)
		return ver, off + len(raw) - len(bare), versioning, skip
	case strings.HasPrefix(raw, "ref:"), t.datasource == dsGoMod && shaRe.MatchString(raw):
		return "", 0, "", "git ref"
	case strings.HasPrefix(raw, "path:"):
		return "", 0, "", "local path"
	case strings.HasPrefix(raw, "prefix:"):
		return "", 0, "", "unsupported range"
	}
	switch t.datasource {
	case dsRuby:
		bare := strings.TrimPrefix(raw, "ruby-")
		off = len(raw) - len(bare)
		switch {
		case rubyRe.MatchString(bare):
			return bare, off, "ruby", ""
		case rubyPartialRe.MatchString(bare):
			return bare, off, "tag", ""
		case bare == "" || wordRe.MatchString(bare):
			return "", 0, "", "non-version value" // jruby-9.4, truffleruby-24, system
		}
		return "", 0, "", "unsupported range"
	case dsPython:
		if m := pythonRe.FindStringSubmatch(raw); m != nil {
			return m[1], 0, "pep440", ""
		}
		if m := pythonPartialRe.FindStringSubmatch(raw); m != nil {
			return m[1], 0, "tag", ""
		}
		if raw == "" || wordRe.MatchString(raw) {
			return "", 0, "", "non-version value" // pypy3.10-7.3.17, miniconda3-latest, system
		}
		return "", 0, "", "unsupported range" // 3.14-dev, >=3.12
	case dsFlutter:
		raw = flutterChannelRe.ReplaceAllString(raw, "")
	}
	if raw == "" || wordRe.MatchString(raw) {
		return "", 0, "", "non-version value"
	}
	bare := strings.TrimPrefix(raw, "v")
	off = len(raw) - len(bare)
	switch t.datasource {
	case dsGo:
		switch {
		case goRe.MatchString(bare):
			return bare, off, "tag", ""
		case partialRe.MatchString(raw):
			return "", 0, "", "partial version"
		}
		return "", 0, "", "unsupported range"
	case dsMaven, dsGradle:
		if mavenRe.MatchString(raw) {
			return raw, 0, "maven", ""
		}
		return "", 0, "", "unsupported range"
	case dsPypi:
		switch {
		case pep440Re.MatchString(raw):
			return raw, 0, "pep440", ""
		case partialRe.MatchString(raw):
			return bare, off, "tag", ""
		}
		return "", 0, "", "unsupported range"
	case dsNode:
		if m := nodeRangeRe.FindStringSubmatchIndex(raw); m != nil && !semverRe.MatchString(raw) {
			ver = raw[m[2]:m[3]]
			if strings.Count(ver, ".") == 2 {
				return ver, m[2], "semver", ""
			}
			return ver, m[2], "tag", ""
		}
	}
	switch {
	case semverRe.MatchString(raw) && t.bare:
		return bare, off, "semver", ""
	case semverRe.MatchString(raw):
		return raw, 0, "semver", ""
	case partialRe.MatchString(raw):
		return bare, off, "tag", ""
	}
	return "", 0, "", "unsupported range"
}

// newDep builds the dep of t's version raw found at byte offset at of the file.
func newDep(mgr, depType, file string, line int, t tool, raw string, at int) dep.Dep {
	d := dep.Dep{
		Manager: mgr, Datasource: t.datasource, DepType: depType, File: file, Line: line,
		Name: t.name, Source: t.source, Version: raw, Skip: t.skip, ExtractVersion: t.extract,
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
