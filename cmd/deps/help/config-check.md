Loads and validates the config file as `plan` does, offline: strict decoding (unknown keys are errors), every value, glob, constraint, schedule and template. It prints `<path>: ok` and exits 0, or prints every problem and exits 2. Without a config file it reports that the built-in defaults apply and exits 0.

Static warnings, such as rules that can never match, go to stderr as `warn    <path>: <field>: <msg>` and do not change the exit code.

Because it needs no token, network or checkout history, it fits a pre-commit hook. With [pre-commit](https://pre-commit.com):

```yaml
# .pre-commit-config.yaml
repos:
  - repo: local
    hooks:
      - id: deps-config-check
        name: deps config check
        entry: deps config check
        language: system
        files: ^\.deps\.ya?ml$
        pass_filenames: false
```

As a plain git hook, `.git/hooks/pre-commit`:

```sh
#!/bin/sh
exec deps config check
```

For a config elsewhere, add `--config <file>` to the entry and adjust `files`.