// Package registry lists the managers deps ships: per manager its reference documentation
// (name, dep types and the datasources of each dep type, as every manager package declares
// them) and its constructor. It is the single source of the manager, dep type and datasource
// names config validates and the configuration schema offers, and of the managers
// engine.Managers constructs.
package registry

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/actions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/ansible"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/ant"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/azurepipelines"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bazel"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/beam"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bitbucketpipelines"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bitrise"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/buildkite"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/bundler"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/circleci"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/clojure"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cloudbuild"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/composer"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/conda"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/crossplane"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/custom/customdoc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/devcontainer"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/droneci"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/elm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/fvm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gitlabci"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gitsubmodules"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/glasskube"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradle"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gradlewrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/homeassistant"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/homebrew"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/jenkins"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/julia"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/kotlinscript"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/kubernetes"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/kustomize"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/mavenwrapper"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nix"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/nuget"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/osgi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/paket"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep621"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pep723"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipenv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/piprequirements"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pipsetup"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/poetry"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/precommit"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/pub"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/quadlet"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/rusttoolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/sbt"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/setupcfg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/smithy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/swift"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/tekton"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/toolversions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/travis"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/vcpkg"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/velaci"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/woodpecker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Entry is one built-in manager: its reference documentation and its constructor, whose
// manager's Name is Doc.Name.
type Entry struct {
	Doc refdoc.Manager
	New func() manager.Manager
}

// Builtin are the built-in managers in discovery order. The custom manager is not among
// them: it is constructed from the configuration (custom.New) and discovered last.
var Builtin = []Entry{
	{actions.Doc, actions.New},
	{gomod.Doc, gomod.New},
	{docker.Doc, docker.New},
	{npm.Doc, npm.New},
	{helm.Doc, helm.New},
	{kustomize.Doc, kustomize.New},
	{kubernetes.Doc, kubernetes.New},
	{precommit.Doc, precommit.New},
	{gitlabci.Doc, gitlabci.New},
	{toolversions.DocNvm, toolversions.NewNvm},
	{toolversions.DocNodenv, toolversions.NewNodenv},
	{toolversions.DocAsdf, toolversions.NewAsdf},
	{toolversions.DocMise, toolversions.NewMise},
	{cargo.Doc, cargo.New},
	{piprequirements.Doc, piprequirements.New},
	{pep621.Doc, pep621.New},
	{pep723.Doc, pep723.New},
	{setupcfg.Doc, setupcfg.New},
	{pipsetup.Doc, pipsetup.New},
	{poetry.Doc, poetry.New},
	{pipenv.Doc, pipenv.New},
	{homeassistant.Doc, homeassistant.New},
	{conda.Doc, conda.New},
	{terraform.Doc, terraform.New},
	{terraform.VersionDoc, terraform.NewVersion},
	{terraform.TerragruntVersionDoc, terraform.NewTerragruntVersion},
	{terraform.TFLintDoc, terraform.NewTFLint},
	{terraform.TerragruntDoc, terraform.NewTerragrunt},
	{maven.Doc, maven.New},
	{gradle.Doc, gradle.New},
	{gradlewrapper.Doc, gradlewrapper.New},
	{mavenwrapper.Doc, mavenwrapper.New},
	{homebrew.Doc, homebrew.New},
	{swift.Doc, swift.New},
	{bundler.Doc, bundler.New},
	{toolversions.DocRubyVersion, toolversions.NewRubyVersion},
	{toolversions.DocPyenv, toolversions.NewPyenv},
	{toolversions.DocRuntimeVersion, toolversions.NewRuntimeVersion},
	{toolversions.DocBunVersion, toolversions.NewBunVersion},
	{gitsubmodules.Doc, gitsubmodules.New},
	{rusttoolchain.Doc, rusttoolchain.New},
	{devcontainer.Doc, devcontainer.New},
	{bitbucketpipelines.Doc, bitbucketpipelines.New},
	{woodpecker.Doc, woodpecker.New},
	{droneci.Doc, droneci.New},
	{circleci.Doc, circleci.New},
	{azurepipelines.Doc, azurepipelines.New},
	{cloudbuild.Doc, cloudbuild.New},
	{velaci.Doc, velaci.New},
	{woodpecker.CrowDoc, woodpecker.NewCrow},
	{travis.Doc, travis.New},
	{buildkite.Doc, buildkite.New},
	{bitrise.Doc, bitrise.New},
	{vcpkg.Doc, vcpkg.New},
	{elm.Doc, elm.New},
	{composer.Doc, composer.New},
	{julia.Doc, julia.New},
	{bazel.Doc, bazel.New},
	{bazel.BazeliskDoc, bazel.NewBazelisk},
	{nuget.Doc, nuget.New},
	{nuget.CakeDoc, nuget.NewCake},
	{paket.Doc, paket.New},
	{ansible.GalaxyDoc, ansible.NewGalaxy},
	{ansible.Doc, ansible.New},
	{sbt.Doc, sbt.New},
	{sbt.ScalafmtDoc, sbt.NewScalafmt},
	{osgi.Doc, osgi.New},
	{smithy.Doc, smithy.New},
	{kotlinscript.Doc, kotlinscript.New},
	{clojure.LeiningenDoc, clojure.NewLeiningen},
	{clojure.DepsEdnDoc, clojure.NewDepsEdn},
	{ant.Doc, ant.New},
	{pub.Doc, pub.New},
	{fvm.Doc, fvm.New},
	{beam.DocMix, beam.NewMix},
	{beam.DocGleam, beam.NewGleam},
	{nix.Doc, nix.New},
	{quadlet.Doc, quadlet.New},
	{crossplane.Doc, crossplane.New},
	{tekton.Doc, tekton.New},
	{glasskube.Doc, glasskube.New},
	{jenkins.Doc, jenkins.New},
}

// Docs returns the reference documentation of the built-in managers and the custom manager,
// in discovery order.
func Docs() []refdoc.Manager {
	out := make([]refdoc.Manager, 0, len(Builtin)+1)
	for _, e := range Builtin {
		out = append(out, e.Doc)
	}
	return append(out, customdoc.Doc)
}
