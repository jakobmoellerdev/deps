package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/renovate"
)

// renovateMode reports whether the config comes from the Renovate config: --renovate, or
// --config naming a Renovate config file.
func (o *rootOpts) renovateMode() bool {
	return o.renovate || (o.config != "" && renovate.IsConfigName(filepath.Base(o.config)))
}

// loadConfig reads .deps.yaml (config.Load, extends read with fetch) or, in compatibility
// mode, translates the Renovate config in memory, logging every translation note.
func loadConfig(ctx context.Context, o *rootOpts, root string, info platform.Info, client *http.Client, auth *httpx.Auth, gh *githubapi.Client, fetch config.Fetcher) (*config.Config, error) {
	if !o.renovateMode() {
		return config.Load(ctx, root, o.config, fetch)
	}
	res, err := renovate.Load(ctx, root, o.config, renovateFetcher(root, info, client, auth, gh))
	if err != nil {
		return nil, err
	}
	for _, n := range res.Notes {
		level := slog.LevelInfo
		if n.Level == renovate.Warn {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "renovate compatibility: "+n.Msg, "setting", n.Path)
	}
	slog.InfoContext(ctx, "renovate compatibility mode", "config", res.Path, "crons", strings.Join(res.Crons, " | "))
	return config.Parse(res.YAML, res.Path)
}

// renovateFetcher fetches remote presets; the forge of local> presets is info, else
// detected from the origin remote of root.
func renovateFetcher(root string, info platform.Info, client *http.Client, auth *httpx.Auth, gh *githubapi.Client) *renovate.Fetcher {
	return &renovate.Fetcher{
		Platform: func(ctx context.Context) (platform.Info, error) {
			if info.Kind != "" {
				return info, nil
			}
			remote, err := gitx.Repo{Dir: root}.RemoteURL(ctx)
			if err != nil {
				return platform.Info{}, err
			}
			return platform.Detect(ctx, os.Getenv, remote, platform.Overrides{}, client)
		},
		GitHub: gh,
		Client: client,
		Auth:   auth,
	}
}

func newMigrate(o *rootOpts) *cli.Command {
	var output string
	var force bool
	return &cli.Command{
		Name:        "migrate",
		Usage:       "Translate the Renovate config into .deps.yaml",
		Description: helpText("migrate"),
		Metadata: map[string]any{docgen.MetaExample: "  deps migrate\n" +
			"  deps migrate --config .github/renovate.json5 --output -"},
		ArgValidator: noArgs,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "output", Destination: &output, Usage: "file to write (default: <dir>/.deps.yaml; - writes to stdout)"},
			&cli.BoolFlag{Name: "force", Destination: &force, Usage: "overwrite an existing output file"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return migrate(ctx, cmd, o, output, force)
		},
	}
}

func migrate(ctx context.Context, cmd *cli.Command, o *rootOpts, output string, force bool) error {
	stderr := cmd.Root().ErrWriter
	root, err := filepath.Abs(o.dir)
	if err != nil {
		return err
	}
	out := output
	if out == "" {
		out = filepath.Join(root, ".deps.yaml")
	}
	if out != "-" && !force {
		if _, err := os.Stat(out); err == nil {
			return &usageError{fmt.Errorf("%s exists; use --force to overwrite it or --output to write elsewhere", out)}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	client := newHTTPClient()
	info, _ := platform.FromEnv(os.Getenv)
	limits := httpx.NewLimits(o.concurrency)
	auth := httpx.NewAuth(info, envToken(), os.Getenv)
	gh := githubapi.New(client, auth, limits.Default)
	res, err := renovate.Load(ctx, root, o.config, renovateFetcher(root, info, client, auth, gh))
	if err != nil {
		return err
	}
	cfg, err := config.Parse(res.YAML, res.Path)
	if err != nil {
		return err
	}
	var warns, infos int
	for _, n := range res.Notes {
		if n.Level == renovate.Warn {
			warns++
			fmt.Fprintf(stderr, "%-8s%s: %s\n", "warn", n.Path, n.Msg)
			continue
		}
		infos++
		if o.verbose {
			fmt.Fprintf(stderr, "%-8s%s: %s\n", "info", n.Path, n.Msg)
		}
	}
	if out == "-" {
		if _, err := cmd.Root().Writer.Write(res.YAML); err != nil {
			return err
		}
	} else if err := os.WriteFile(out, res.YAML, 0o644); err != nil { //nolint:gosec // a config file, readable like the Renovate config
		return err
	}
	fmt.Fprintf(stderr, "wrote %s from %s: %d rules, %d custom managers, %d custom datasources; %d warnings, %d notes\n",
		out, res.Path, len(cfg.Rules), len(cfg.Custom), len(cfg.CustomDatasources), warns, infos)
	if len(res.Crons) > 0 {
		fmt.Fprintf(stderr, "schedules need these workflow crons (on.schedule): %s\n", strings.Join(res.Crons, ", "))
	}
	return nil
}
