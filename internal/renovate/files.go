package renovate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

// ConfigFileNames are the repository config files Renovate reads, in lookup order
// (lib/config/app-strings.ts plus the .gitea and .forgejo variants of those platforms).
var ConfigFileNames = []string{"renovate.json", "renovate.jsonc", "renovate.json5",
	".github/renovate.json", ".github/renovate.jsonc", ".github/renovate.json5",
	".gitlab/renovate.json", ".gitlab/renovate.jsonc", ".gitlab/renovate.json5",
	".gitea/renovate.json", ".gitea/renovate.jsonc", ".gitea/renovate.json5",
	".forgejo/renovate.json", ".forgejo/renovate.jsonc", ".forgejo/renovate.json5",
	".renovaterc", ".renovaterc.json", ".renovaterc.jsonc", ".renovaterc.json5", "package.json"}

// IsConfigName reports whether base is the base name of a ConfigFileNames entry other than
// package.json.
func IsConfigName(base string) bool {
	for _, n := range ConfigFileNames {
		if n != "package.json" && path.Base(n) == base {
			return true
		}
	}
	return false
}

// readConfig reads explicitPath (relative to the working directory) or the first Renovate
// config file in dir and returns its path and the config object. All failures are
// *config.Error.
func readConfig(dir, explicitPath string) (string, map[string]any, error) {
	if explicitPath != "" {
		src, err := os.ReadFile(explicitPath)
		if err != nil {
			return "", nil, &config.Error{Err: err}
		}
		cfg, err := decodeConfig(explicitPath, src)
		return explicitPath, cfg, err
	}
	for _, n := range ConfigFileNames {
		p := filepath.Join(dir, filepath.FromSlash(n))
		src, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, &config.Error{Err: err}
		}
		if n == "package.json" {
			v, err := parseJSON5(src)
			if err != nil {
				return "", nil, &config.Error{Err: fmt.Errorf("%s: %w", p, err)}
			}
			obj, _ := v.(map[string]any)
			if _, ok := obj["renovate"]; !ok {
				continue
			}
		}
		cfg, err := decodeConfig(p, src)
		return p, cfg, err
	}
	return "", nil, &config.Error{Err: fmt.Errorf("no Renovate config in %s (looked for %s)", dir, strings.Join(ConfigFileNames, ", "))}
}

// decodeConfig parses a config file; for package.json its renovate value is the config.
func decodeConfig(p string, src []byte) (map[string]any, error) {
	v, err := parseJSON5(src)
	if err != nil {
		return nil, &config.Error{Err: fmt.Errorf("%s: %w", p, err)}
	}
	if filepath.Base(p) == "package.json" {
		obj, _ := v.(map[string]any)
		v = obj["renovate"]
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, &config.Error{Err: fmt.Errorf("%s: Renovate config must be a JSON object", p)}
	}
	return obj, nil
}
