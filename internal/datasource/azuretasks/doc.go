package azuretasks

import (
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
)

// Doc is the reference documentation of the azure-pipelines-tasks datasource.
var Doc = refdoc.Datasource{
	Name:     Name,
	NameForm: "task name (`Bash`), task id, or `<publisher>.<extension>.<contribution>.<name|id>`; case-insensitive",
	Endpoints: "public lists (Renovate's): `GET <DEPS_AZURE_PIPELINES_TASKS_URL or https://raw.githubusercontent.com/renovatebot/azure-devops-marketplace/main>/azure-pipelines-builtin-tasks.json` once per run, and for tasks it lacks `…/azure-pipelines-marketplace-tasks.json` once (streamed; only the run's tasks are kept). " +
		"With `DEPS_AZURE_DEVOPS_URL` and `DEPS_AZURE_DEVOPS_TOKEN`: `GET <DEPS_AZURE_DEVOPS_URL>/_apis/distributedtask/tasks/` once per run, the tasks installed in that organization or collection (Renovate's behavior on the Azure platform). " +
		"After the versions a derived `X.Y` and `X` per release line, so major-only references (`Bash@3`) are updated with their precision",
	Auth:         "none for the public lists; Basic `deps:<DEPS_AZURE_DEVOPS_TOKEN>` (a personal access token) for the organization",
	ReleaseTimes: "none",
}
