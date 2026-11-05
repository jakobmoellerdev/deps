package azurepipelines

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the azure-pipelines manager
// (docs/reference/managers.md).
var Doc = refdoc.Manager{
	Name:    Name,
	Summary: "Updates container resources, GitHub repository resources and task versions in Azure Pipelines YAML. Tasks are ignored by default.",
	Files: []string{
		"`*.y(a)ml` below `.azuredevops/`",
		"paths matching `azure.*pipelines?.*\\.ya?ml$` (`azure-pipelines.yml`, `.azure-pipelines/*.yml`, `azure-pipelines/**/*.yaml`)",
	},
	DepTypes: []refdoc.DepType{
		{Name: "container", What: "`resources.containers[].image`", Datasource: "`docker`", Versioning: "`docker`"},
		{Name: "repository", What: "`resources.repositories[]` of `type: github` with `ref: refs/tags/<tag>`; Source `https://github.com/<name>`", Datasource: "`git-tags`", Versioning: "`tag`"},
		{Name: "task", What: "`task: <name>@<version>` of every step, wherever it nests (`steps`, `jobs`, `stages`, deployment strategies, templates, `extends` parameters, `${{ if }}` blocks); ignored by default", Datasource: "`azure-pipelines-tasks`", Versioning: "`tag`"},
	},
	Skips: []refdoc.Skip{
		{Reason: "variable reference", When: "`$` (`$(var)`, `${{ … }}`) in the image, repository, ref or task"},
		{Reason: "unsupported repository type", When: "a repository resource of `type: git` (Azure Repos) or `bitbucket`, as in Renovate"},
		{Reason: "no ref", When: "a GitHub repository resource without `ref` (default branch)"},
		{Reason: "non-tag ref", When: "a ref other than `refs/tags/<tag>` (a branch)"},
		{Reason: "no version", When: "a task without `@<version>`"},
		{Reason: "invalid reference", When: "the image reference does not parse"},
		{Reason: "no tag", When: "an image without tag"},
		{Reason: "digest without tag", When: "an image pinned by digest only"},
		{Reason: "cannot locate value", When: "the value bytes were not found (escapes, block scalars)"},
	},
	Sections: []refdoc.Section{
		{Title: "Tasks", Items: []string{
			"Renovate disables this manager because a task version listed publicly may not be installed in an Azure DevOps organization or Server yet. deps ignores only DepType `task` by default; `{manager: azure-pipelines, depTypes: [task], ignore: false}` opts in. Containers and repositories are updated by default.",
			"A major version (`Bash@3`) is updated to the next major (`Bash@4`), a full version (`Bash@3.231.0`) to the newest full version: the precision is kept (Renovate writes the full version unless an `extractVersion` rule cuts it).",
			"With `DEPS_AZURE_DEVOPS_URL` and `DEPS_AZURE_DEVOPS_TOKEN`, the versions are the tasks installed in that organization or collection (see the `azure-pipelines-tasks` datasource).",
		}},
		{Title: "Details", Items: []string{
			"Images are parsed and edited like the docker manager: tags update, digests are refreshed or pinned. The docker manager leaves `containers` entries with a `container` key to this manager.",
			"Job-level `container:` values are not extracted: they can name a container resource instead of an image (as in Renovate).",
		}},
	},
}
