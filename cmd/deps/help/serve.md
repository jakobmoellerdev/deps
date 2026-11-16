Runs deps as a long-running bot instead of one process per repository. It keeps a repository inventory in Valkey, gives every repository a due time, claims the ones that are due and runs a cycle for each of them in an in-process worker pool. A cycle is exactly what `deps run` does on a checkout (or `deps plan` with `mode: plan`), so everything the configuration of a repository says applies unchanged.

**Valkey is the only external dependency**, and it holds nothing that cannot be rebuilt: the forge stays the source of truth (the update branches, the fingerprints in the PR bodies, the dependency dashboard). Losing Valkey loses scheduling bookkeeping, the recent run history and the metadata cache; the next inventory refresh recreates the repository set and its due times.

**Scheduling.** A new repository's first cycle is due at `now + fnv64a(id) mod interval`, so a fleet on one cadence is spread evenly over it instead of coming due together; later cycles are `interval ± 10 %`, a failing one backs off exponentially up to the cadence. The due entry doubles as a visibility timeout: claiming a repository bumps it to `now + 15m` and takes a lease, so a replica that dies simply lets its repositories become due again — there is no janitor and no leader for the queue. Repositories that stayed due for ten minutes are stolen by any worker with a free slot.

**Roles.** Every replica runs the worker loop. The inventory refresh and the retention duties run on whichever replica holds the `scheduler` lock; the role is visible as `deps_scheduler_leader`. Workers own due partitions by rendezvous hashing over the live worker set, which needs no coordination: adding or removing a replica moves only its own share, and the lease still admits one worker per repository while the set is in flux.

**Schedules.** `schedule` time windows (`before 6am on monday`) are evaluated against the time the cycle starts, exactly as a CI run evaluates its own start time. Cron-style `schedule` entries match the *trigger* of a run, which a daemon does not have, so they never match here: they are an Actions feature. The daemon's cadence (`interval`, per repository rule) plus time windows replace them.

**Webhooks** (`POST /webhooks/<forge>`) bring a repository forward instead of waiting for its cadence: a push to the base branch, an edit of the dashboard issue or its comments (which is what makes a ticked checkbox act within seconds), and a pull request that was closed, reopened or edited (the rebase checkbox, the `stop-updating` label). A GitHub `installation` event refreshes the inventory. The signature is verified over the raw body before anything is parsed: `X-Hub-Signature-256` on GitHub, `X-Gitea-Signature`/`X-Forgejo-Signature` on Gitea and Forgejo, `X-Gitlab-Token` on GitLab; a delivery without a configured secret is refused. Accepted deliveries schedule the cycle 30 seconds out and only ever bring the due time forward, so a burst collapses into one cycle.

**Toolchains.** The image ships git, CA certificates and the deps binary. The ecosystem tools a repository needs are downloaded on first use into `<cache-dir>/tools`, verified against the publisher's own checksum, and reused by every later cycle and every replica sharing the volume. The version is the one the repository declares (`.tool-versions`, the `go`/`toolchain` directives, `.nvmrc`, `package.json` `packageManager` and `engines.node`, `rust-toolchain.toml`, `.java-version`, `.terraform-version`, `global.json`, …), else the `toolPins` entry; a tool with neither is treated as absent, which gives the same `<tool> not found` message as a machine without it. A tool whose publisher ships no checksum artifact is not installed at all — there is no unverified download path.

**Git.** Each repository keeps a warm bare mirror under `<cache-dir>/git`, so only its first cycle pays for a shallow snapshot; every cycle gets a *new* work tree, because the per-directory caches of the datasources and tools are keyed by absolute path. Both caches are swept to `cloneCacheMaxBytes` and `toolCacheMaxBytes`, least recently used first.

**Writable paths.** Everything the daemon and its tools produce lives under `cacheDir`: `git/` (the mirrors), `work/` (one work tree per cycle), `tools/` (the installed toolchains), `state/` (their own caches — the Go module cache, npm, uv, Gradle, NuGet, …) and `home/`, which is `$HOME` so that no tool drops a dotfile outside it. The only other path written at runtime is `$TMPDIR`, where the post-processors mirror a scope before running an ecosystem tool. The daemon creates all of them at startup, so the image's root file system can be mounted read-only (`deploy/` does).

The image is Debian slim rather than distroless, and that is not an oversight: the ecosystem tools are not self-contained binaries. `npm`, `npx` and `corepack` are scripts starting `#!/usr/bin/env node`, `mvn`, `gradle` and `gradlew` start `#!/bin/sh`, `flutter` is bash, `git` is absent from distroless entirely, and node needs libstdc++, which `distroless/base` lacks. A distroless image would lose npm, pnpm, yarn, Maven, Gradle, Flutter and `postUpgradeTasks` — most of what lock file regeneration is for — so the runtime is hardened by what it may write instead of by what it contains.

**Credentials.** A forge authenticates with a static token (`tokenEnv`) or as a GitHub App installation (`app`), which is the only way to get an API budget per organization rather than per account. Installation tokens are minted on demand, shared between replicas through Valkey, and never handed out with less life left than one cycle may take. With `credentialLimit`, requests are counted per credential and hour; a cycle that would exceed the budget is rescheduled to the reset instead of spending it on errors.

**Configuration** (`--config`, default `/etc/deps/serve.yaml`). Unknown keys are errors, as in `.deps.yaml`.

```yaml
valkey: {url: "redis://valkey:6379/0"}
listen: ":8080"
apiTokenEnv: DEPS_API_TOKEN     # bearer token of the API; without it the API needs no auth
workers: 8                      # cycles at once in this replica; also the memory multiplier
concurrency: 32                 # --concurrency of one cycle
interval: 24h                   # default cadence
cycleTimeout: 30m
cacheDir: /var/lib/deps
cloneCacheMaxBytes: 214748364800
toolCacheMaxBytes: 21474836480
toolPins: {go: "1.27.4", node: "24.11.0", uv: "0.9.7", java: "21"}
inventoryInterval: 1h
maxNewPRs: 10
allowTasks: []                  # postUpgradeTasks allowlist; empty leaves tasks off
signingKeyEnv: ""
credentialLimit: 0              # hourly API budget per credential; 0 = untracked
reportTtl: 168h
author: {name: "deps[bot]", email: "deps-bot@noreply.invalid"}
forges:
  - name: monstera
    kind: forgejo
    apiUrl: https://git.example.com/api/v1
    serverUrl: https://git.example.com
    tokenEnv: DEPS_FORGEJO_TOKEN
    webhookSecretEnv: DEPS_FORGEJO_WEBHOOK_SECRET
    repositories:
      - {match: "my-org/archive-*", enabled: false}
      - {match: "my-org/*", mode: run, interval: 6h}
  - name: github-app
    kind: github
    apiUrl: https://api.github.com
    serverUrl: https://github.com
    app: {id: 123456, privateKeyFile: /etc/deps/app.pem}
    webhookSecretEnv: DEPS_GITHUB_WEBHOOK_SECRET
    repositories: [{match: "**", mode: run}]
```

`repositories` entries are matched top to bottom and the first match wins, as the rules of `.deps.yaml` do; `match` uses the glob syntax of `--repositories` (`*` within a path segment, `**` for any number of them). A repository matching no entry is not managed. `mode: plan` performs no forge write at all and only stores the report. `enabled: false` turns a matching group off. The inventory lists the owners derived from the rules; name them explicitly with `owners:` when a rule's owner is itself a glob.

**HTTP surface** on `--listen`:

| Path | Purpose |
|---|---|
| `GET /healthz` | 200 once the process is up |
| `GET /readyz` | 200 when Valkey answers and the worker loop heartbeated within 30s |
| `GET /metrics` | Prometheus text exposition |
| `GET /api/v1/repos?cursor=&limit=&status=` | the inventory, paged |
| `GET /api/v1/repos/{id}` | one repository (`id` is `<host>/<owner>/<name>`, URL-escaped) |
| `POST /api/v1/repos/{id}/run` | run it now (202) |
| `GET /api/v1/runs?repo=&limit=` | recent cycles, newest first |
| `GET /api/v1/runs/{runID}/report` | the JSON `--report` document of a cycle, until `reportTtl` passes |
| `POST /webhooks/{forge}` | forge deliveries |

The API takes `Authorization: Bearer <apiTokenEnv>`; the probes, the metrics and the webhooks are exempt, the last because they carry their own signature.

**Metrics:** `deps_cycles_total{forge,mode,status}`, `deps_cycle_duration_seconds{forge,mode}`, `deps_queue_due`, `deps_queue_claims_total{kind}`, `deps_prs_total{action}`, `deps_http_requests_total{host,outcome}`, `deps_rate_limited_total{host}`, `deps_toolchain_installs_total{tool,result}`, `deps_webhooks_total{forge,result}`, `deps_clone_cache_bytes`, `deps_tool_cache_bytes`, `deps_workers_live`, `deps_worker_active_cycles`, `deps_repositories`, `deps_scheduler_leader`, `deps_inventory_refresh_total{result}`, `deps_cycle_lease_lost_total`.

**`--once`** refreshes the inventory, runs every repository that is due and exits. It is what a cron-style deployment without a long-running process uses, and what the end-to-end tests drive.

**Sizing.** Cycles run in process: repository code (gradle, maven, bundler, `setup.py`) still executes in the worker, and an out-of-memory kill takes the replica down with everything it is running. Size `workers` against the container's memory limit (a cycle was measured at 40–130 MB plus its tool child processes) and set `GOMEMLIMIT` to about 80 % of it. Reduce `workers` before considering anything larger.
