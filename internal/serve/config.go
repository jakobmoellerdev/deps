// Package serve is the long-running deps bot: a repository inventory kept in Valkey, a
// scheduler that smears cycles over their cadence, an in-process worker pool that runs them,
// forge webhooks that bring a repository forward, and an HTTP API with Prometheus metrics.
package serve

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/serve/store"
)

// Duration is a YAML duration written the way Go spells it: 30m, 24h, 1h30m.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is no duration (e.g. 30m, 24h)", s)
	}
	*d = Duration(v)
	return nil
}

// D is the duration as time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the serve configuration file.
type Config struct {
	Valkey Valkey `yaml:"valkey"`
	Listen string `yaml:"listen"`
	// APITokenEnv names the variable holding the bearer token of the write API; without it
	// the API is read-only to everyone who can reach it.
	APITokenEnv string `yaml:"apiTokenEnv"`
	// Workers is how many cycles run at once in this replica. Cycles run in process, so this
	// is also the memory multiplier: size it against the container limit.
	Workers int `yaml:"workers"`
	// Concurrency is --concurrency of one cycle.
	Concurrency        int               `yaml:"concurrency"`
	Interval           Duration          `yaml:"interval"`
	CycleTimeout       Duration          `yaml:"cycleTimeout"`
	CacheDir           string            `yaml:"cacheDir"`
	CloneCacheMaxBytes int64             `yaml:"cloneCacheMaxBytes"`
	ToolCacheMaxBytes  int64             `yaml:"toolCacheMaxBytes"`
	ToolPins           map[string]string `yaml:"toolPins"`
	InventoryInterval  Duration          `yaml:"inventoryInterval"`
	MaxNewPRs          int               `yaml:"maxNewPRs"`
	// AllowTasks is the postUpgradeTasks allowlist; empty leaves tasks off, so repository
	// code does not run in the daemon unless it is configured to.
	AllowTasks    []string `yaml:"allowTasks"`
	SigningKeyEnv string   `yaml:"signingKeyEnv"`
	Author        Author   `yaml:"author"`
	// CredentialLimit is the hourly API request budget of one credential; 0 = untracked.
	CredentialLimit int `yaml:"credentialLimit"`
	// ReportTTL is how long the machine-readable report of a run stays readable.
	ReportTTL Duration `yaml:"reportTtl"`
	Forges    []Forge  `yaml:"forges"`
}

// Valkey is where the control plane lives.
type Valkey struct {
	URL string `yaml:"url"`
	TLS struct {
		CAFile   string `yaml:"caFile"`
		CertFile string `yaml:"certFile"`
		KeyFile  string `yaml:"keyFile"`
	} `yaml:"tls"`
}

// Author is the commit author of the daemon's commits.
type Author struct {
	Name  string `yaml:"name"`
	Email string `yaml:"email"`
}

// Forge is one code forge and the credential that reaches it.
type Forge struct {
	Name      string `yaml:"name"`
	Kind      string `yaml:"kind"` // github | gitea | forgejo | gitlab
	APIURL    string `yaml:"apiUrl"`
	ServerURL string `yaml:"serverUrl"`
	// TokenEnv names the variable holding a static token. Mutually exclusive with App.
	TokenEnv string `yaml:"tokenEnv"`
	// App authenticates as a GitHub App installation, which is the only way to get an API
	// budget per organization rather than per account.
	App              *App       `yaml:"app"`
	WebhookSecretEnv string     `yaml:"webhookSecretEnv"`
	Owners           []string   `yaml:"owners"`
	Repositories     []RepoRule `yaml:"repositories"`
}

// App is a GitHub App registration.
type App struct {
	ID             int64  `yaml:"id"`
	PrivateKeyFile string `yaml:"privateKeyFile"`
}

// RepoRule selects repositories of a forge; the first matching rule wins, as the rules of
// .deps.yaml do.
type RepoRule struct {
	// Match is an owner/name glob with the semantics of --repositories.
	Match    string     `yaml:"match"`
	Mode     store.Mode `yaml:"mode"`
	Interval Duration   `yaml:"interval"`
	Base     string     `yaml:"base"`
	Enabled  *bool      `yaml:"enabled"`
}

// Defaults of the configuration.
const (
	DefaultListen            = ":8080"
	DefaultCacheDir          = "/var/lib/deps"
	DefaultInterval          = 24 * time.Hour
	DefaultCycleTimeout      = 30 * time.Minute
	DefaultInventoryInterval = time.Hour
	DefaultConcurrency       = 32
	DefaultMaxNewPRs         = 10
	DefaultReportTTL         = 7 * 24 * time.Hour
	DefaultAuthorName        = "deps[bot]"
	DefaultAuthorEmail       = "deps-bot@noreply.invalid"
)

// Load reads and validates a serve configuration. Unknown keys are errors, as in .deps.yaml:
// a misspelled key must not silently do nothing in a daemon that runs unattended.
func Load(file string) (*Config, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	c.setDefaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &c, nil
}

func (c *Config) setDefaults() {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.Workers <= 0 {
		c.Workers = max(4, runtime.GOMAXPROCS(0))
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.Interval <= 0 {
		c.Interval = Duration(DefaultInterval)
	}
	if c.CycleTimeout <= 0 {
		c.CycleTimeout = Duration(DefaultCycleTimeout)
	}
	if c.InventoryInterval <= 0 {
		c.InventoryInterval = Duration(DefaultInventoryInterval)
	}
	if c.CacheDir == "" {
		c.CacheDir = DefaultCacheDir
	}
	if c.MaxNewPRs == 0 {
		c.MaxNewPRs = DefaultMaxNewPRs
	}
	if c.ReportTTL <= 0 {
		c.ReportTTL = Duration(DefaultReportTTL)
	}
	if c.Author.Name == "" {
		c.Author.Name = DefaultAuthorName
	}
	if c.Author.Email == "" {
		c.Author.Email = DefaultAuthorEmail
	}
	for i := range c.Forges {
		f := &c.Forges[i]
		if f.ServerURL != "" {
			f.ServerURL = strings.TrimSuffix(f.ServerURL, "/")
		}
		for j := range f.Repositories {
			if f.Repositories[j].Mode == "" {
				f.Repositories[j].Mode = store.ModeRun
			}
		}
	}
}

var kinds = []string{"github", "gitea", "forgejo", "gitlab"}

func (c *Config) validate() error {
	if c.Valkey.URL == "" {
		return errors.New("valkey.url is required")
	}
	if len(c.Forges) == 0 {
		return errors.New("at least one forge is required")
	}
	seen := map[string]bool{}
	for i := range c.Forges {
		f := &c.Forges[i]
		switch {
		case f.Name == "":
			return fmt.Errorf("forges[%d]: name is required", i)
		case seen[f.Name]:
			return fmt.Errorf("forges[%d]: duplicate name %q", i, f.Name)
		case !contains(kinds, f.Kind):
			return fmt.Errorf("forge %s: kind must be one of %s, got %q", f.Name, strings.Join(kinds, ", "), f.Kind)
		case f.ServerURL == "":
			return fmt.Errorf("forge %s: serverUrl is required", f.Name)
		case f.TokenEnv == "" && f.App == nil:
			return fmt.Errorf("forge %s: one of tokenEnv or app is required", f.Name)
		case f.TokenEnv != "" && f.App != nil:
			return fmt.Errorf("forge %s: tokenEnv and app are mutually exclusive", f.Name)
		case f.App != nil && f.Kind != "github":
			return fmt.Errorf("forge %s: app authentication exists only on GitHub", f.Name)
		case f.App != nil && (f.App.ID == 0 || f.App.PrivateKeyFile == ""):
			return fmt.Errorf("forge %s: app needs id and privateKeyFile", f.Name)
		case len(f.Repositories) == 0:
			return fmt.Errorf("forge %s: at least one repositories entry is required", f.Name)
		}
		seen[f.Name] = true
		for j, r := range f.Repositories {
			switch {
			case r.Match == "":
				return fmt.Errorf("forge %s: repositories[%d]: match is required", f.Name, j)
			case r.Mode != store.ModeRun && r.Mode != store.ModePlan:
				return fmt.Errorf("forge %s: repositories[%d]: mode must be run or plan, got %q", f.Name, j, r.Mode)
			}
			for seg := range strings.SplitSeq(r.Match, "/") {
				if _, err := path.Match(seg, ""); seg != "**" && err != nil {
					return fmt.Errorf("forge %s: repositories[%d]: %q: %w", f.Name, j, r.Match, err)
				}
			}
		}
	}
	return nil
}

func contains(all []string, v string) bool { return slices.Contains(all, v) }

// Match returns the rule that governs fullName on this forge, nil when none does: a
// repository matching no rule is not managed.
func (f *Forge) Match(fullName string) *RepoRule {
	name := strings.Split(fullName, "/")
	for i := range f.Repositories {
		r := &f.Repositories[i]
		if matchGlob(strings.Split(r.Match, "/"), name) {
			return r
		}
	}
	return nil
}

// matchGlob matches path segments against a pattern whose segments are path.Match patterns,
// with ** standing for any number of segments. It is the same shape as the --repositories
// globs of the CLI.
func matchGlob(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for i := range len(name) + 1 {
			if matchGlob(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	if m, _ := path.Match(pattern[0], name[0]); !m {
		return false
	}
	return matchGlob(pattern[1:], name[1:])
}

// Managed reports whether the rule manages its repositories; `enabled: false` turns a
// matching group off without listing it anywhere else.
func (r *RepoRule) Managed() bool { return r.Enabled == nil || *r.Enabled }

// Owner globs of a forge: the owners whose repositories the inventory lists. They are
// derived from the repository rules, so a deployment names its scope once.
func (f *Forge) OwnerGlobs() []string {
	if len(f.Owners) > 0 {
		return f.Owners
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range f.Repositories {
		owner, _, ok := strings.Cut(r.Match, "/")
		if !ok || owner == "" || strings.ContainsAny(owner, "*?[") {
			continue // a glob owner cannot be listed; use owners:
		}
		if !seen[owner] {
			seen[owner] = true
			out = append(out, owner)
		}
	}
	return out
}
