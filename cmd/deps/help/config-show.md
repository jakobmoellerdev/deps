Prints the effective configuration as YAML: every top-level key in schema order, with the value of the config file or else its default (`pin: keep`, `branchPrefix: deps/`, the default `ignorePaths`, …). Rules, custom managers and custom datasources are printed as written, with the defaults of their unset keys (`custom[].versioning: semver`, `customDatasources.<name>.format: json`) added. A leading comment names the file, or says that the built-in defaults apply.

Four values are shown as the commands resolve them:

- `git.author` is the commit author of `run`: `--author-name`/`--author-email` (accepted here too), then `$DEPS_GIT_AUTHOR`, then the file, then `deps[bot] <deps-bot@noreply.invalid>`.
- `actions.defaultHost` is the host of `owner/repo@ref` actions: the file, then the host of `$DEPS_DEFAULT_ACTIONS_URL`, then the platform default (`data.forgejo.org` on Forgejo Actions, else `github.com`).
- `git.platformCommit` is `$DEPS_PLATFORM_COMMIT` when set, else the file, else `auto`.
- `gitlabci.url` is the GitLab instance of the `gitlabci` manager: the file, then `$CI_SERVER_URL`, then `https://gitlab.com`.

`--sources` appends where each value comes from as a comment: `default`, `file`, `env` or `flag`. Nested objects (`git`, `actions`, `gitlabci`, `terraform`, `gomod`, `composer`, `dashboard`, `lockFileMaintenance`) are annotated per key:

```yaml
# effective configuration of /repo/.deps.yaml
version: 1 # default
pin: digest # file
...
git:
  author: bot <bot@example.com> # env
...
actions:
  defaultHost: github.com # default
```