# Registry end-to-end tests

Runs `deps plan` against real OCI registries to test deps' built-in registry client
(`internal/datasource/docker`): tag listing, manifest digests, Basic and Bearer auth, and
credentials from the Docker config, including namespaced `auths` entries.

This is a separate Go module so testcontainers-go and go-containerregistry, which start the
registries and push the test images, stay out of the root `go.mod`. The tests build `cmd/deps`
from the repository root, write a scratch repository and a `config.json` (via `DOCKER_CONFIG`)
per scenario, run `deps plan --report` with a clean environment, and assert on the JSON report
and the diff. When a scenario fails, the test prints the deps exit code, stdout and stderr
(`--log-level debug`); set `E2E_LOG_DEPS=1` to print them for every scenario.

## Run

```bash
task e2e:registry
# or
cd hack/e2e-registry && go test -tags e2e -count=1 -timeout 20m ./...
```

Without the `e2e` build tag nothing runs; without a reachable Docker daemon the tests skip.
`TestZot` and `TestHarbor` run in parallel; pass `-run TestZot` for the fast one. Not part of
`task ci`.

## What runs

**`TestZot`**: `ghcr.io/project-zot/zot` with htpasswd Basic auth (bcrypt, generated per run).
Pushes `team/app` 1.0.0 (Docker schema 2 image) and 1.1.0 (OCI index, linux/amd64 and
linux/arm64), and the Helm chart artifact `charts/demo` 0.1.0 and 0.2.0 (Helm config and content
media types), referenced from a `Dockerfile` and a `Chart.yaml` `oci://` dependency.

| Scenario | Docker config | Expected |
| --- | --- | --- |
| host credentials | `auths[host:port]` = good | `team/app` 1.0.0 -> 1.1.0, `demo` 0.1.0 -> 0.2.0 |
| host credentials pin digest | same, `pin: digest` | `team/app:1.1.0@<index digest>`, the digest of the pushed index |
| no credentials | none | an auth error for both, exit code 1 |
| namespaced credentials | `auths[host:port/team]` = good, `auths[host:port]` = bad | `team/app` updated, `demo` (no `charts` entry) fails with the host entry |

**`TestHarbor`**: Harbor from `harbor/docker-compose.yaml` (copied from
[harbor-cli's test setup](https://github.com/goharbor/harbor-cli/tree/main/test/harbor), see
`harbor/NOTICE`), started with testcontainers-go's compose module on a free port, so that
Harbor's token realm (`EXT_ENDPOINT`) is `http://localhost:<port>/service/token`. The test creates
the private projects `alpha` and `beta`, one robot account per project (pull and push on that
project only), and pushes `alpha/app` 1.0.0 and 1.1.0 (OCI index) and `beta/app` 2.0.0 and 2.1.0.
Every scenario uses `pin: digest` and checks the pinned digests.

| Scenario | Docker config | Expected |
| --- | --- | --- |
| admin host credentials | `auths[host:port]` = admin | both updated and pinned (Bearer token flow) |
| namespaced robot credentials | `auths[host:port/alpha]` = alpha robot, `auths[host:port/beta]` = beta robot | both updated and pinned |
| swapped robot credentials | the robots swapped | an auth error for both |
| anonymous private project | none | an auth error for both |
| anonymous public project | none, `beta` made public | `alpha` fails, `beta` updated and pinned |

Containers, the compose stack and its volumes are removed when the tests end (testcontainers'
reaper removes them if the test process dies).

## Resources

- About 2 GB of images on first run (Harbor's eight `goharbor/*` images, zot) and about 2 GB of
  RAM while Harbor runs.
- Docker (any recent engine; the compose module is built into the test, no `docker compose`
  CLI needed) with access to Docker Hub and ghcr.io.
- Wall time with cached images: about 1 minute, nearly all of it Harbor's startup; zot alone
  takes a few seconds.
