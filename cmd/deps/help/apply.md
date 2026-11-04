Resolves every dependency, prints the same report as `deps plan` on stderr, and writes the changed files, including `go mod tidy` output (`go.mod`, `go.sum`), regenerated `package-lock.json`, `pnpm-lock.yaml` or `yarn.lock`, and `Cargo.lock` updated with `cargo update`. Post-processors run on temporary copies; a failed one drops that module's or lockfile root's changes and is reported as an error.

A dependency with updates in several PR buckets contributes one update to the files: the newest non-major one, else the lowest major. Its other updates are reported with `, separate PR`, as is the lock file maintenance update (`lockFileMaintenance`), which only `deps run` makes.

The platform is only detected from CI environment variables. Locally, lookups run without a platform token unless one of the token variables is set (see `deps help environment`).
