| Variable | Used by | Meaning |
|---|---|---|
| `DEPS_TOKEN`, `GITHUB_TOKEN`, `GITEA_TOKEN`, `FORGEJO_TOKEN` | all | platform token, first non-empty wins; `run --token` takes precedence. `run` falls back to `git credential fill` for the platform host, and without any token fails with `no token for <host>` (except `--dry-run`). Sent to the platform host's lookups as Basic `x-access-token:<token>`. |
| `DEPS_GITHUB_COM_TOKEN` | all | token for github.com lookups when the platform is not github.com (avoids anonymous rate limits for actions and releases hosted on GitHub) |
| `DEPS_DEFAULT_ACTIONS_URL` | all | default host of `owner/repo@ref` actions (hostname, or a URL whose host is used); overridden by the config key `actions.defaultHost` |
| `DEPS_NODE_DIST_URL` | `node-version` datasource | base URL of a nodejs.org `dist` mirror (default `https://nodejs.org/dist`) |
| `DEPS_GIT_AUTHOR` | `run` | commit author `Name <email>`; between the `--author-*` flags and the config key `gitAuthor` |
| `DEPS_TRIGGER_SCHEDULE` | `run` | default of `--trigger-schedule` |
| `GITHUB_EVENT_NAME`, `GITHUB_EVENT_PATH` | `run` | run trigger and its payload (cron of `schedule` events); set by GitHub, Gitea and Forgejo runners |
| `DEPS_LOG_LEVEL`, `DEPS_LOG_FORMAT` | all | defaults of `--log-level`, `--log-format` |
| `NPM_CONFIG_REGISTRY`, `npm_config_registry` | `npm` datasource | registry when no `.npmrc` sets one (default `https://registry.npmjs.org/`) |
| `GOPROXY`, `GONOPROXY`, `GOPRIVATE`, `GOMODCACHE` | `go` datasource | read via `go env -json` (the process environment when `go` is not on `PATH`). `GOMODCACHE`'s `cache/download/**/@v/*.info` files provide release times without network. |
| `GITHUB_ACTIONS`, `GITEA_ACTIONS`, `FORGEJO_ACTIONS`, `GITHUB_SERVER_URL`, `GITHUB_API_URL`, `GITHUB_REPOSITORY`, `FORGEJO_SERVER_URL`, `FORGEJO_API_URL`, `FORGEJO_REPOSITORY` | all | platform detection: `FORGEJO_ACTIONS=true` → forgejo, `GITEA_ACTIONS=true` → gitea, `GITHUB_ACTIONS=true` → github; the `FORGEJO_*` variables fall back to the `GITHUB_*` ones. `run` also detects from the `origin` remote; `plan`, `apply` and `explain` only from these variables. |
| `GITHUB_STEP_SUMMARY`, `GITEA_STEP_SUMMARY` | all | step summary file (see `deps help output`) |

On a 401 without a static token, git tag lookups call `git credential fill` once per host, with `GIT_TERMINAL_PROMPT=0` and a 10s timeout. Every `git` process deps runs has `GIT_TERMINAL_PROMPT=0`, so deps never prompts. npm lookups use `.npmrc` `_authToken` entries, and docker lookups the Docker credential chain (`~/.docker/config.json` and credential helpers).
