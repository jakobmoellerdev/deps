Resolves every dependency and prints the report on stderr (see `deps help output`). Then writes a unified diff per changed file to stdout (`a/<path>` → `b/<path>`); `git apply` accepts it. Nothing is written to the repository.

Post-processors do not run, so `go.sum` and lockfile changes are missing from the diff; `plan` notes this with `note    go.sum changes are computed by apply/run`, `note    package-lock.json changes are computed by apply/run`, `note    Cargo.lock changes are computed by apply/run`, `note    Gemfile.lock changes are computed by apply/run`, `note    composer.lock changes are computed by apply/run`, `note    Manifest.toml changes are computed by apply/run` and `note    .terraform.lock.hcl changes are computed by apply/run`. Use `deps apply` to get them.

A dependency with updates in several PR buckets contributes one update to the files: the newest non-major one, else the lowest major. Its other updates are reported with `, separate PR`, as is the lock file maintenance update (`lockFileMaintenance`), which only `deps run` makes.

The platform is only detected from CI environment variables. Locally, lookups run without a platform token unless one of the token variables is set (see `deps help environment`).
