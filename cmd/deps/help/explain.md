Resolves only the dependencies whose name matches any of the doublestar globs (the syntax of rule `names`, for example `'actions/*'` or `'k8s.io/*'`) and prints, per occurrence, the matching rules, the effective policy, the rejected candidates by reason and the resulting update. The report goes to stdout, one block per occurrence:

```
actions/checkout  .gitea/workflows/ci.yml:10  (github-actions, action, git-tags, tag)
  current   v7.0.1
  rules     none
  policy    pin=keep allow=- allowedUpdateTypes=major,minor,patch,digest,pin group=- schedule=always minAge=-
  candidates 68
  rejected  not newer: 58 (v7.0.1, v7.0.0, v6.1.0, ...)  precision: 7 (v7, v6, v5, ...)  invalid: 2 (v2-beta, v6-beta)  prefix: 1 (1.0.0)
  result    up to date

github.com/spf13/pflag  go.mod:28  (gomod, indirect, go, go)
  current   v1.0.10
  rules     none
  policy    pin=keep allow=- allowedUpdateTypes=major,minor,patch,digest,pin group=- schedule=always minAge=-
  result    skipped: ignored by default (indirect; enable with a rule setting ignore: false)
```

The header names the manager, dep type, datasource and versioning. The other lines in a block:

- `age       released <date>` follows `current` when a `currentAge` rule needed the release time of the current version; `age       unknown (currentAge rules do not match)` when the datasource does not know it.
- `builtin   monorepo <name> -> group "<name> monorepo"` appears for monorepo members.
- `rules` lists the matching rules as `#<i> match: <fields>; apply: <fields> (from <file>:<line>)` (the match fields it sets, `all` when none, then the apply fields it sets), or `none` when no rule matched, for example `#2 match: managers=[gomod] depTypes=[indirect]; apply: ignore=false (from .deps.yaml:12)`. `<i>` counts the rules of the whole config, those of `extends` first; `<file>` is where the rule is written: the config file (`.deps.yaml`, or the `--config` path), or an extended file as `<repo>//<path>[#<ref>]` (`(from org/deps-config//.deps.yaml:12)`). Rules with `updateTypes` that applied to an update follow as `#<i> match: <fields>; apply: <fields> (applies to <type>, from <file>:<line>)`.
- `policy` is the effective result of the apply fields: `pin`, `allow`, `allowedUpdateTypes`, `group`, `schedule` and `minAge`; `versioning=`, `rangeStrategy=` and `automerge=` are appended when set. It is followed by one `<type>: <policy>` line per type that `updateTypes` rules applied to.
- `rejected` groups versions by reason, most frequent first, with up to 3 newest examples each. Reasons include `ignored by rule #4 for major`, `outside allow /^\d+\.\d+$/`, `update type major not allowed`, `too new (released 2026-11-01, minimum 28 days)`, `release time unknown (minimum 28 days, timestamp-required)`, `older than current`, `above latest` (npm: a major above `dist-tags.latest`'s), `prerelease`, `precision` and `not newer`. The policy's `minAge=` reads `28 days (timestamp-required)` under `minimumReleaseAgeBehaviour: timestamp-required`.
- `result` reads `skipped: <reason>`, `error: <err>`, `up to date`, or one `update` line per PR bucket, for example `update v6.0.0 -> v7.0.1 (major, bucket major)`.
- Each advisory is printed as `security  <id>  <summary>  <url>`.

Config warnings (`warn    <config>: <field>: <msg>`) and errors go to stderr; see `deps help output`. A glob that matches no dependency fails with `no dependency matches <globs>` (exit 1). No globs, or an invalid glob, exits 2.
