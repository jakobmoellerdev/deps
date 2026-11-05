package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

type initOpts struct {
	fromRenovate string
	force        bool
}

func newInit(o *rootOpts) *cli.Command {
	opts := &initOpts{}
	return &cli.Command{
		Name:  "init",
		Usage: "Write a starter .deps.yaml, optionally translated from a Renovate config",
		Metadata: map[string]any{docgen.MetaExample: "  deps init\n" +
			"  deps init --from-renovate renovate.json\n" +
			"  deps init --dir ../service --force"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runInit(ctx, cmd, o, opts)
		},
		Flags: append(scanFlags(o, scanOffline),
			&cli.StringFlag{Name: "from-renovate", Destination: &opts.fromRenovate, Usage: "translate this Renovate config (JSON, e.g. renovate.json) into the new file"},
			&cli.BoolFlag{Name: "force", Destination: &opts.force, Usage: "overwrite an existing config file"},
		),
	}
}

func runInit(ctx context.Context, cmd *cli.Command, o *rootOpts, opts *initOpts) error {
	stderr := cmd.Root().ErrWriter
	root, err := filepath.Abs(o.dir)
	if err != nil {
		return err
	}
	target, err := initTarget(root, o.config, opts.force)
	if err != nil {
		return err
	}
	header := []string{"Written by `deps init`; `deps config describe` documents every key."}
	var body []byte // the document below version: 1
	detectCfg := config.Default()
	if opts.fromRenovate != "" {
		res, err := loadRenovate(ctx, root, opts.fromRenovate, defaultConcurrency)
		if err != nil {
			return err
		}
		printNotes(stderr, res, false)
		cfg, err := config.Parse(res.YAML, res.Path)
		if err != nil {
			return err
		}
		detectCfg.IgnorePaths = cfg.IgnorePaths
		body = res.YAML
	}
	found, err := detectManagers(ctx, stderr, root, detectCfg)
	if err != nil {
		return err
	}
	src := initFile(schemaURL(version), header, found, body)
	if err := os.WriteFile(target, src, 0o644); err != nil { //nolint:gosec // a config file, readable like any other
		return err
	}
	if _, err := config.Parse(src, target); err != nil {
		return fmt.Errorf("wrote %s, but it does not validate; fix it and run deps config check: %w", target, err)
	}
	fmt.Fprintf(cmd.Root().Writer, "wrote %s\n", target)
	return nil
}

// initTarget returns the file init writes: --config, else the existing .deps.yaml or .deps.yml
// in root, else root/.deps.yaml. An existing file is only overwritten with force.
func initTarget(root, explicit string, force bool) (string, error) {
	cands := []string{filepath.Join(root, ".deps.yaml"), filepath.Join(root, ".deps.yml")}
	if explicit != "" {
		cands = []string{explicit}
	}
	for _, p := range cands {
		_, err := os.Stat(p)
		switch {
		case err == nil && !force:
			return "", fmt.Errorf("%s exists; pass --force to overwrite it", p)
		case err == nil:
			return p, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", err
		}
	}
	return cands[0], nil
}

// detected is what the offline scan found for one manager.
type detected struct {
	manager     string
	files, deps int
}

// detectManagers scans root with the built-in managers and cfg's ignorePaths, offline: files
// are extracted, but no dependency is looked up. Files that fail to extract are warned about
// on stderr. The result is in discovery order and holds only managers with dependencies.
func detectManagers(ctx context.Context, stderr io.Writer, root string, cfg *config.Config) ([]detected, error) {
	managers, err := engine.Managers(cfg)
	if err != nil {
		return nil, err
	}
	deps, errs, err := engine.Extract(ctx, root, cfg, manager.Env{DefaultHost: "github.com"}, managers)
	if err != nil {
		return nil, err
	}
	for _, e := range errs {
		fmt.Fprintf(stderr, "warn    %v\n", e)
	}
	byName := map[string]*detected{}
	seen := map[[2]string]bool{} // manager, file
	for _, d := range deps {
		m := byName[d.Manager]
		if m == nil {
			m = &detected{manager: d.Manager}
			byName[d.Manager] = m
		}
		m.deps++
		if k := [2]string{d.Manager, d.File}; !seen[k] {
			seen[k] = true
			m.files++
		}
	}
	var out []detected
	for _, m := range managers {
		if d := byName[m.Name()]; d != nil {
			out = append(out, *d)
			delete(byName, m.Name())
		}
	}
	return out, nil
}

// initFile renders the new config: the $schema modeline, the header lines, the detected
// managers as comments, then body (a .deps.yaml document, e.g. a Renovate translation) with
// version: 1 above its keys, below its leading comments.
func initFile(schema string, header []string, found []detected, body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("# yaml-language-server: $schema=" + schema + "\n")
	for _, h := range header {
		b.WriteString("# " + h + "\n")
	}
	b.WriteString("#\n")
	if len(found) == 0 {
		b.WriteString("# No dependency files detected (offline scan with the default ignorePaths).\n")
	} else {
		b.WriteString("# Detected managers (offline scan):\n")
		width := 0
		for _, d := range found {
			width = max(width, len(d.manager))
		}
		for _, d := range found {
			fmt.Fprintf(&b, "#   %-*s  %s, %s\n", width, d.manager, plural(d.files, "file", "files"), plural(d.deps, "dependency", "dependencies"))
		}
	}
	rest := body
	if len(rest) > 0 && rest[0] == '#' {
		b.WriteString("#\n")
	}
	for len(rest) > 0 {
		line, after, _ := bytes.Cut(rest, []byte("\n"))
		if t := bytes.TrimSpace(line); len(t) > 0 && t[0] != '#' {
			break
		}
		b.Write(line)
		b.WriteByte('\n')
		rest = after
	}
	fmt.Fprintf(&b, "version: %d\n", config.FormatVersion)
	b.Write(rest)
	return b.Bytes()
}
