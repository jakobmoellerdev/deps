Without a key, lists every configuration key, one per line, with the first sentence of its description. Nested keys are paths: `actions.defaultHost`, `rules[].allow`, `custom[].checksum.asset`, `customDatasources.<name>.url`.

With a key, prints its type, default, allowed values and limits, then its full description, as plain text (the content of the [configuration reference](configuration.md), from the same schema). A key is accepted as listed or as it appears in error messages: `rules.allow`, `rules[].allow` and `rules[2].allow` are the same key, and so are `customDatasources.<name>.url` and `customDatasources.envtest.url`.

An unknown key exits 2 and suggests the closest key, or the keys ending in it (`allow` suggests `rules[].allow`).