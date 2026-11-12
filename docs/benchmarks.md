# Benchmarks

`deps plan` (scan, all registry lookups, patched diff) on large open-source repositories, limited to 4 CPUs (`taskset -c 0-3`, `GOMAXPROCS=4`) like a standard CI runner, median of 3 runs. The README shows a shortened version of this table.

| Repository | Ecosystems | Tracked files | Updates found | Wall time | CPU time | Peak memory |
|---|---|--:|--:|--:|--:|--:|
| openclaw/openclaw 🦞 | pnpm, Swift, Ruby, Actions | 66,962 | 671 | 4.5 s | 1.9 s | 133 MB |
| vercel/next.js | pnpm, Rust, Actions | 34,938 | 1,605 | 9.8 s | 4.5 s | 114 MB |
| kubernetes/kubernetes | Go, containers | 31,757 | 150 | 1.7 s | 0.5 s | 73 MB |
| grafana/grafana | Go, Yarn, containers, Actions | 24,512 | 1,147 | 12.5 s¹ | 4.8 s | 111 MB |
| backstage/backstage | Yarn workspaces | 12,657 | 1,245 | 6.5 s | 2.3 s | 104 MB |
| spring-projects/spring-boot | Gradle, Maven | 11,983 | 35 | 1.1 s | 0.2 s | 41 MB |
| astral-sh/ruff | Cargo, Python, npm | 11,327 | 146 | 3.4 s | 0.7 s | 66 MB |
| can1357/oh-my-pi | Bun, Rust, Python | 10,503 | 190 | 3.7 s | 0.5 s | 60 MB |
| istio/istio | Go, containers, Helm | 6,793 | 127 | 7.0 s | 0.8 s | 69 MB |
| argoproj/argo-cd | Go, Yarn, containers | 6,245 | 188 | 12.0 s¹ | 1.3 s | 85 MB |
| open-component-model | Go, containers, Helm | 3,109 | 105 | 2.5 s | 0.6 s | 53 MB |
| kubernetes-sigs/cluster-api | Go, containers | 2,403 | 29 | 1.2 s | 0.3 s | 50 MB |

CPU time is deps itself; the rest of the wall time is registry latency, mostly container registries paging through tag lists.

¹ `proxy.golang.org` does not answer the probe for a newer major module path of a few modules (grafana: `github.com/aws/aws-sdk-go-v2/service/licensemanager/v2`; argo-cd: three, among them `k8s.io/kubernetes/v2`), so those lookups hit their 10 s timeout in every run; without them both runs take about 3 s.

`deps run` adds the lockfile tools (`go mod tidy`, npm/pnpm/yarn, uv, cargo …) for the PRs it updates; PRs whose content didn't change are not pushed again.

## How the numbers are produced

1. Clone each repository (shallow is enough) under a scratch directory.
2. For every repository, run `taskset -c 0-3 env GOMAXPROCS=4 /usr/bin/time -v deps plan --no-color` three times warm and take the median wall time, user+sys CPU time and maximum resident set size.
3. Tracked files is `deps plan`'s scan count, updates found is the number of updates in the plan.

Registry latency dominates most rows, so re-measure all repositories in one session; numbers from different days are not comparable.
