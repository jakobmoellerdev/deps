package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/checksum"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/crate"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	dockerds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubreleases"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gittags"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/gotoolchain"
	helmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/nodeversion"
	npmds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/osv"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/refdoc"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/releasenotes"
)

// newDocs is the hidden maintainer command behind `task docs`: it regenerates the reference
// documentation and the committed schema.
func newDocs() *cobra.Command {
	var out, schemaPath, actionPath string
	cmd := &cobra.Command{
		Use:    "docs",
		Short:  "Generate the reference documentation and deps.schema.json",
		Hidden: true,
		Args:   noArgs,
		RunE: func(*cobra.Command, []string) error {
			return writeDocs(out, schemaPath, actionPath)
		},
	}
	f := cmd.Flags()
	f.StringVar(&out, "out", "docs/reference", "directory of the generated markdown files")
	f.StringVar(&schemaPath, "schema", "deps.schema.json", "path of the generated JSON Schema")
	f.StringVar(&actionPath, "action", "action.yml", "action.yml to document")
	return cmd
}

// writeDocs writes configuration.md, cli.md, action.md and managers.md to out and the schema
// to schemaPath.
func writeDocs(out, schemaPath, actionPath string) error {
	schema, err := configSchema()
	if err != nil {
		return err
	}
	s, err := reflectConfig()
	if err != nil {
		return err
	}
	cfg, err := docgen.Config(s)
	if err != nil {
		return fmt.Errorf("configuration.md: %w", err)
	}
	root := newRoot()
	root.InitDefaultCompletionCmd()
	cli, err := docgen.CLI(root)
	if err != nil {
		return fmt.Errorf("cli.md: %w", err)
	}
	src, err := os.ReadFile(actionPath)
	if err != nil {
		return err
	}
	action, err := docgen.Action(src, actionIntro, actionUsages)
	if err != nil {
		return fmt.Errorf("action.md: %w", err)
	}
	managers, err := docgen.Managers(managersInput())
	if err != nil {
		return fmt.Errorf("managers.md: %w", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{
		schemaPath:                             schema,
		filepath.Join(out, "configuration.md"): cfg,
		filepath.Join(out, "cli.md"):           cli,
		filepath.Join(out, "action.md"):        action,
		filepath.Join(out, "managers.md"):      managers,
	}
	for p, b := range files {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// actionIntro describes what the composite action does; the inputs come from action.yml.
const actionIntro = "The composite action in the repository root downloads the released static `deps` binary for the runner, " +
	"verifies it against the release's `checksums.txt` with `sha256sum`, prints `deps version`, and runs `deps <args>` with " +
	"`DEPS_TOKEN` set to the `token` input. Go is not needed. The same `action.yml` works unchanged on GitHub Actions and on " +
	"Gitea and Forgejo Actions.\n\n" +
	"The version is resolved first. With `cache: true` (the default) a binary of that version is reused instead of " +
	"downloaded: from the runner's tool cache (`$RUNNER_TOOL_CACHE/deps/<version>/<arch>`, kept between jobs on " +
	"self-hosted runners), else from the forge's Actions cache (`actions/cache/restore`, key `deps-<version>-linux-<arch>`; " +
	"GitHub's cache service and the Gitea/Forgejo runner cache server). A cached binary runs only if its sha256 matches the " +
	"checksum recorded from `checksums.txt` next to it; otherwise it is downloaded again. After a download the binary is " +
	"saved to the Actions cache.\n\n" +
	"Only linux/amd64 and linux/arm64 runners are supported; on any other OS or architecture the first step fails with " +
	"`deps: unsupported runner <os>/<arch>; only linux/amd64 and linux/arm64 are released`. Pin a release tag (`@v0.1.0`) so " +
	"runs are reproducible; a ref that is not a release tag (`@main`) downloads the latest release.\n\n" +
	"Complete workflows, tokens and permissions are described in [Running deps in CI](../ci.md); the arguments in " +
	"the [CLI reference](cli.md)."

// actionUsages are the snippets of the action reference.
var actionUsages = []docgen.ActionUsage{
	{
		Title: "Gitea and Forgejo",
		Note: "Use the full URL: a short `jakobmoellerdev/deps@…` is resolved against the instance's `DEFAULT_ACTIONS_URL`, " +
			"which is github.com by default.",
		Uses: "https://git.jakob-moeller.cloud/jakobmoellerdev/deps@vX.Y.Z",
	},
	{
		Title: "GitHub",
		Note:  "From the GitHub mirror; on github.com runners the binary is downloaded from the mirror's releases.",
		Uses:  "jakobmoellerdev/deps@vX.Y.Z",
	},
}

// managersIntro opens the managers and datasources reference.
const managersIntro = "A **manager** finds dependencies in files and edits them. It records byte spans, and an update " +
	"replaces only those bytes, so files are never reformatted. A **datasource** lists the available versions of a dep.\n\n" +
	"Every discovered file goes to every manager whose file pattern matches. Discovery always prunes `.git`, " +
	"`node_modules`, `vendor` and `testdata`, plus anything matched by [`ignorePaths`](configuration.md#ignorepaths).\n\n" +
	"The tables give the dep types (for [`rules[].depTypes`](configuration.md#rulesdeptypes)) and skip reasons. Skip " +
	"reasons are exact strings, shown by `deps plan -v` as `skip    <file>:<line>  <name>  <reason>`."

// datasourceDocs documents the datasources newSession registers, in table order.
func datasourceDocs() []refdoc.Datasource {
	return []refdoc.Datasource{
		gittags.Doc, githubreleases.Doc, goproxy.Doc, gotoolchain.Doc, dockerds.Doc, npmds.Doc, nodeversion.Doc, helmds.Doc, crate.Doc, customds.Doc,
	}
}

func managersInput() docgen.ManagersInput {
	return docgen.ManagersInput{
		Intro:          managersIntro,
		Skips:          engine.SkipDocs,
		Managers:       engine.ManagerDocs(),
		Datasources:    datasourceDocs(),
		Aliases:        config.DatasourceAliases,
		Shared:         datasource.Overview,
		Notes:          []refdoc.Section{osv.Doc, checksum.Doc, releasenotes.Doc},
		IgnoredDefault: config.IgnoredByDefault,
	}
}
