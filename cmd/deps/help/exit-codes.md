| Code | When |
|---|---|
| `0` | success, no errors |
| `1` | any error, including per-dependency lookup errors and per-PR errors (already printed as `error` lines), or a fatal error printed as `deps: <err>` |
| `2` | usage error: bad flag or argument, invalid `--log-level`/`--log-format`/`--platform`/`--concurrency`, invalid `DEPS_GIT_AUTHOR`, invalid `--trigger-schedule`/`DEPS_TRIGGER_SCHEDULE` or `--schedule-time`/`DEPS_SCHEDULE_TIME`, a `--signing-key`/`DEPS_SIGNING_KEY` that is no OpenSSH or armored OpenPGP private key, an invalid `DEPS_PLATFORM_COMMIT`, `platformCommit: enabled` on Gitea/Forgejo or with a signing key, an unknown key of `deps config describe`, an unreadable or unparsable `--from-renovate` file, or any config error (parse error, unknown key, failed validation, unreadable `--config` file; also a file `deps init` wrote that does not validate) |
