deps finds the dependencies of a repository (GitHub/Gitea/Forgejo Actions, Go modules, container images, npm packages, Rust crates, Python packages and custom-annotated versions), looks up newer versions, and either prints the changes as a diff (`plan`), writes them (`apply`), or pushes one branch and opens one pull request per dependency or group (`run`). Files are edited byte-exactly and never reformatted.

The configuration is read from `.deps.yaml` (or `.deps.yml`) in `--dir`; `deps schema` prints its JSON Schema. With `--renovate`, the repository's Renovate config is translated instead (`deps migrate` writes that translation to `.deps.yaml`). Results go to stderr, diffs and explanations to stdout. See `deps help output`, `deps help environment` and `deps help exit-codes`.

Hidden flags for profiling: `--cpuprofile <file>` writes a CPU profile, and `--memprofile <file>` writes an allocation profile on exit (pprof format).
