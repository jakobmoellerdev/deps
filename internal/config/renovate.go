package config

// RenovateOption is the deps equivalent of a Renovate option (docs/migrating-from-renovate.md).
type RenovateOption struct {
	From  string // the Renovate option as shown, when it is more than the key (enabled: false)
	Scope string // scope of To: "", "rules[]", "rules[].postUpgradeTasks", "custom[]" or "customDatasources.<name>"
	To    string // the deps key (optionally with a value); "" when there is none
	Note  string
}

// RenovateOptions are the Renovate options that are no deps key of the same name and scope:
// unknown-key errors hint at their deps equivalent. `deps migrate` (internal/renovate)
// translates Renovate configs.
var RenovateOptions = map[string]RenovateOption{
	"packageRules":                {To: "rules"},
	"enabled":                     {From: "enabled: false", Scope: "rules[]", To: "ignore: true"},
	"ignoreDeps":                  {Note: "Renovate's ignoreDeps is a rule with names and ignore: true here"},
	"matchPackageNames":           {Scope: "rules[]", To: "names"},
	"matchPackagePatterns":        {Scope: "rules[]", To: "names", Note: "write each regex as /<RE2>/"},
	"matchPackagePrefixes":        {Scope: "rules[]", To: "names", Note: "doublestar globs such as prefix/**"},
	"matchDepNames":               {Scope: "rules[]", To: "names"},
	"matchDepPatterns":            {Scope: "rules[]", To: "names", Note: "write each regex as /<RE2>/"},
	"groupName":                   {Scope: "rules[]", To: "group"},
	"allowedVersions":             {Scope: "rules[]", To: "allow"},
	"matchUpdateTypes":            {Scope: "rules[]", To: "updateTypes"},
	"matchManagers":               {Scope: "rules[]", To: "managers"},
	"matchDatasources":            {Scope: "rules[]", To: "datasources"},
	"matchBaseBranches":           {Scope: "rules[]", To: "baseBranches"},
	"baseBranchPatterns":          {To: "baseBranches"},
	"matchPaths":                  {Scope: "rules[]", To: "paths"},
	"matchFileNames":              {Scope: "rules[]", To: "paths"},
	"matchDepTypes":               {Scope: "rules[]", To: "depTypes"},
	"versioning":                  {Scope: "rules[]", To: "versioning"},
	"semanticCommitType":          {Scope: "rules[]", To: "commitType"},
	"semanticCommitScope":         {Scope: "rules[]", To: "commitScope"},
	"matchCategories":             {Note: "Renovate's matchCategories is not supported; match managers or datasources instead"},
	"pinDigests":                  {To: "pin: digest"},
	"vulnerabilityAlerts":         {To: "security"},
	"osvVulnerabilityAlerts":      {To: "security"},
	"customManagers":              {To: "custom"},
	"customType":                  {Note: "custom[] entries are regex managers when match is set"},
	"managerFilePatterns":         {Scope: "custom[]", To: "files"},
	"fileMatch":                   {Scope: "custom[]", To: "files", Note: "write each regex as /<RE2>/"},
	"matchStrings":                {Scope: "custom[]", To: "match", Note: "one RE2 regexp"},
	"datasourceTemplate":          {Scope: "custom[]", To: "datasource"},
	"depNameTemplate":             {Scope: "custom[]", To: "name"},
	"packageNameTemplate":         {Scope: "custom[]", To: "name"},
	"versioningTemplate":          {Scope: "custom[]", To: "versioning"},
	"fileFilters":                 {Scope: "rules[].postUpgradeTasks", To: "files", Note: "doublestar globs, not minimatch"},
	"defaultRegistryUrlTemplate":  {Scope: "customDatasources.<name>", To: "url"},
	"transformTemplates":          {Scope: "customDatasources.<name>", To: "versions", Note: "a selector plus versionRegex instead of JSONata"},
	"postUpdateOptions":           {Note: "gomodTidy is always on; gomodUpdateImportPaths is the top-level key gomodUpdateImportPaths"},
	"semanticCommits":             {Note: "semantic commits are always on; see rules[].commitType and rules[].commitScope"},
	"hostRules":                   {Note: "not supported: credentials come from the environment, see deps help environment"},
	"platformAutomerge":           {Note: "platform automerge is always on"},
	"ignoreTests":                 {Note: "not supported: deps never merges without green checks"},
	"dependencyDashboardApproval": {To: "dashboardApproval"},
}

// ValidCustomDatasourceName reports whether name is an accepted customDatasources key
// (CustomDatasourceNamePattern).
func ValidCustomDatasourceName(name string) bool { return customDatasourceNameRe.MatchString(name) }
