package tfregistry

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/terraform"
)

// credentials resolves registry tokens as the Terraform CLI does: TF_TOKEN_<host>, then the
// `credentials` blocks of the CLI config, then the configured credentials helper.
type credentials struct {
	getenv   func(string) string
	goos     string
	readFile func(string) ([]byte, error)
	readDir  func(string) ([]os.DirEntry, error)
	stat     func(string) (fs.FileInfo, error)
	lookPath func(string) (string, error)
	run      func(ctx context.Context, name string, args ...string) ([]byte, error)

	once sync.Once
	cfg  terraform.CLIConfig
	dir  string // the CLI config directory; "" when unknown
	err  error

	hosts datasource.Memo[string, string]
}

func newCredentials(getenv func(string) string) *credentials {
	return &credentials{
		getenv: getenv, goos: runtime.GOOS,
		readFile: os.ReadFile, readDir: os.ReadDir, stat: os.Stat, lookPath: exec.LookPath, run: runHelper,
	}
}

// token returns the token of host ("" for anonymous), resolved once per host.
func (c *credentials) token(ctx context.Context, host string) (string, error) {
	host = strings.ToLower(host)
	return c.hosts.Do(ctx, host, func() (string, error) { return c.resolve(ctx, host) })
}

func (c *credentials) resolve(ctx context.Context, host string) (string, error) {
	if tok := c.fromEnv(host); tok != "" {
		return tok, nil
	}
	c.once.Do(func() { c.cfg, c.dir, c.err = c.load() })
	if c.err != nil {
		return "", c.err
	}
	if tok, ok := c.cfg.Credentials[host]; ok {
		return tok, nil
	}
	if c.cfg.Helper == "" {
		return "", nil
	}
	return c.fromHelper(ctx, host)
}

// fromEnv reads TF_TOKEN_<host> with dots as underscores and hyphens as is or as double
// underscores, or the host verbatim (TF_TOKEN_xn--caf-dma.fr).
func (c *credentials) fromEnv(host string) string {
	dots := strings.ReplaceAll(host, ".", "_")
	for _, name := range []string{strings.ReplaceAll(dots, "-", "__"), dots, host} {
		if tok := c.getenv("TF_TOKEN_" + name); tok != "" {
			return tok
		}
	}
	return ""
}

// home returns the directory holding the CLI config file and directory: $HOME, or %APPDATA%
// on Windows.
func (c *credentials) home() string {
	if c.goos == "windows" {
		return c.getenv("APPDATA")
	}
	return c.getenv("HOME")
}

// load reads the CLI config file (TF_CLI_CONFIG_FILE, else ~/.terraformrc or
// %APPDATA%/terraform.rc) and, unless the file is overridden, the `*.tfrc` and `*.tfrc.json`
// files of the config directory (~/.terraform.d, %APPDATA%/terraform.d, credentials.tfrc.json
// included) on top of it, later files winning per host as in Terraform.
func (c *credentials) load() (terraform.CLIConfig, string, error) {
	cfg := terraform.CLIConfig{Credentials: map[string]string{}}
	home := c.home()
	dir := ""
	if home != "" {
		if c.goos == "windows" {
			dir = filepath.Join(home, "terraform.d")
		} else {
			dir = filepath.Join(home, ".terraform.d")
		}
	}
	main := c.getenv("TF_CLI_CONFIG_FILE")
	if main == "" {
		main = c.getenv("TERRAFORM_CONFIG")
	}
	override := main != ""
	if !override && home != "" {
		if c.goos == "windows" {
			main = filepath.Join(home, "terraform.rc")
		} else {
			main = filepath.Join(home, ".terraformrc")
		}
	}
	files := []string{}
	if main != "" {
		files = append(files, main)
	}
	if !override && dir != "" {
		entries, err := c.readDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return cfg, dir, fmt.Errorf("terraform CLI config: %w", err)
		}
		for _, e := range entries {
			if n := e.Name(); !e.IsDir() && (strings.HasSuffix(n, ".tfrc") || strings.HasSuffix(n, ".tfrc.json")) {
				files = append(files, filepath.Join(dir, n))
			}
		}
	}
	for _, f := range files {
		src, err := c.readFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			if override && f == main {
				slog.Warn("terraform CLI config file does not exist", "file", f)
			}
			continue
		}
		if err != nil {
			return cfg, dir, fmt.Errorf("terraform CLI config: %w", err)
		}
		parse := terraform.ParseCLIConfig
		if strings.HasSuffix(f, ".json") {
			parse = terraform.ParseCLIConfigJSON
		}
		fc, err := parse(src)
		if err != nil {
			return cfg, dir, fmt.Errorf("terraform CLI config %s: %w", f, err)
		}
		for h, tok := range fc.Credentials {
			cfg.Credentials[strings.ToLower(h)] = tok
		}
		if fc.Helper != "" {
			cfg.Helper, cfg.HelperArgs = fc.Helper, fc.HelperArgs
		}
	}
	return cfg, dir, nil
}

// fromHelper runs `terraform-credentials-<name> <args...> get <host>`, found in the plugin
// directories (<config dir>/plugins and <config dir>/plugins/<os>_<arch>) or else on PATH.
func (c *credentials) fromHelper(ctx context.Context, host string) (string, error) {
	name := "terraform-credentials-" + c.cfg.Helper
	bin := c.findHelper(name)
	if bin == "" {
		return "", fmt.Errorf("%s not found (needed for %s credentials)", name, host)
	}
	out, err := c.run(ctx, bin, append(append([]string{}, c.cfg.HelperArgs...), "get", host)...)
	if err != nil {
		return "", fmt.Errorf("%s get %s: %w", name, host, err)
	}
	var doc struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &doc); err != nil {
		return "", fmt.Errorf("%s get %s: malformed output: %w", name, host, err)
	}
	return doc.Token, nil
}

func (c *credentials) findHelper(name string) string {
	file := name
	if c.goos == "windows" {
		file += ".exe"
	}
	if c.dir != "" {
		plugins := filepath.Join(c.dir, "plugins")
		for _, d := range []string{plugins, filepath.Join(plugins, c.goos+"_"+runtime.GOARCH)} {
			p := filepath.Join(d, file)
			if fi, err := c.stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	if p, err := c.lookPath(name); err == nil {
		return p
	}
	return ""
}

// runHelper runs a credentials helper, returning its stdout; a failure carries its stderr.
func runHelper(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}
