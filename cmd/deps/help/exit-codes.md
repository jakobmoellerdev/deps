| Code | When |
|---|---|
| `0` | success, no errors |
| `1` | any error, including per-dependency lookup errors and per-PR errors (already printed as `error` lines), or a fatal error printed as `deps: <err>` |
| `2` | usage error: bad flag or argument, invalid `--log-level`/`--log-format`/`--platform`/`--concurrency`, invalid `DEPS_GIT_AUTHOR`, invalid `--trigger-schedule`/`DEPS_TRIGGER_SCHEDULE`, or any config error (parse error, unknown key, failed validation, unreadable `--config` file) |
