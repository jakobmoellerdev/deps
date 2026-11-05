# SchemaStore entry

With an entry in [SchemaStore](https://www.schemastore.org), editors that use its catalog (VS Code YAML extension, yaml-language-server, JetBrains IDEs) validate `.deps.yaml` and `.deps.yml` without a `# yaml-language-server: $schema=…` modeline. The entry is prepared here; it is not submitted yet.

## Catalog entry

Add this object to the `schemas` array of [`src/api/json/catalog.json`](https://github.com/SchemaStore/schemastore/blob/master/src/api/json/catalog.json) (the array is sorted by `name`, case-insensitively):

```json
{
  "name": "deps",
  "description": "deps configuration file (dependency updates for GitHub and Gitea, a Renovate replacement)",
  "fileMatch": [".deps.yaml", ".deps.yml"],
  "url": "https://github.com/jakobmoellerdev/deps/releases/latest/download/deps.schema.json"
}
```

The `url` is the GitHub mirror's latest-release redirect: every release attaches `deps.schema.json` on Gitea and GitHub, and GitHub's `releases/latest/download/<asset>` always resolves to the newest one. The schema's `$id` stays the versioned Gitea URL of that release (`https://git.jakob-moeller.cloud/jakobmoellerdev/deps/releases/download/<tag>/deps.schema.json`). A modeline in `.deps.yaml` takes precedence over the catalog, so pinning the schema to your deps version keeps working.

## Steps

1. Wait for the first release that carries the `deps.schema.json` asset (any release after v0.3.0) and check that `curl -sSfL https://github.com/jakobmoellerdev/deps/releases/latest/download/deps.schema.json | jq '."$id"'` prints that release's URL.
2. Fork [SchemaStore/schemastore](https://github.com/SchemaStore/schemastore) and add the entry above to `src/api/json/catalog.json`. The schema stays hosted here, so no file goes to `src/schemas/json/`.
3. Run the repository's checks (`node ./cli.js check` per its [CONTRIBUTING.md](https://github.com/SchemaStore/schemastore/blob/master/CONTRIBUTING.md)); they validate the catalog and that `fileMatch` does not collide with another entry.
4. Open the pull request ("Add deps schema"), linking this repository and `deps schema`.
