package renovate

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pattern"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// Level is the severity of a Note.
type Level int

// Note levels: Info for settings without effect on which updates or PRs exist, Warn for
// settings deps cannot express.
const (
	Info Level = iota
	Warn
)

func (l Level) String() string {
	if l == Warn {
		return "warn"
	}
	return "info"
}

// Note reports a Renovate setting that was not translated, or translated approximately.
type Note struct {
	Level     Level
	Path, Msg string // Path: Renovate key path or origin, e.g. "packageRules[3]"
}

// Result is a translated Renovate config.
type Result struct {
	Path    string   // Renovate config file used
	YAML    []byte   // translated .deps.yaml with comments
	Notes   []Note   // in order of occurrence, without duplicates
	Windows []string // sorted distinct schedule time windows; the workflow must run inside each
}

// notes collects Notes without duplicates.
type notes struct {
	list []Note
	seen map[Note]bool
	// builtin downgrades warnings to info while a setting that only Renovate's built-in
	// presets set is translated: the repository did not ask for it.
	builtin bool
}

func (n *notes) add(l Level, path, format string, a ...any) {
	note := Note{Level: l, Path: path, Msg: fmt.Sprintf(format, a...)}
	if l == Warn && n.builtin {
		note.Level, note.Msg = Info, note.Msg+" (from a built-in preset)"
	}
	if n.seen[note] {
		return
	}
	if n.seen == nil {
		n.seen = map[Note]bool{}
	}
	n.seen[note] = true
	n.list = append(n.list, note)
}

func (n *notes) info(path, format string, a ...any) { n.add(Info, path, format, a...) }
func (n *notes) warn(path, format string, a ...any) { n.add(Warn, path, format, a...) }

// Load reads the Renovate config of dir (explicitPath if set), resolves its presets and
// translates it into a .deps.yaml document. f fetches remote presets; nil rejects them.
// Config problems are *config.Error.
func Load(ctx context.Context, dir, explicitPath string, f *Fetcher) (*Result, error) {
	p, raw, err := readConfig(dir, explicitPath)
	if err != nil {
		return nil, err
	}
	if enabled, ok := raw["enabled"].(bool); ok && !enabled {
		return nil, configErr("%s: enabled: false disables Renovate for this repository", p)
	}
	setOrigins(raw, "")
	n := &notes{}
	r := &resolver{ctx: ctx, fetcher: f, notes: n}
	cfg, err := r.resolve(raw, nil, nil)
	if err != nil {
		return nil, err
	}
	if enabled, ok := cfg["enabled"].(bool); ok && !enabled {
		return nil, configErr("%s: enabled: false disables Renovate for this repository", p)
	}
	t := &translator{n: n, monorepos: r.monorepos, repl: r.replacements, cdKept: map[string]bool{}, own: raw, remote: r.remoteKeys}
	doc := t.translate(cfg)

	shown := p
	if rel, err := filepath.Rel(dir, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		shown = filepath.ToSlash(rel)
	}
	parsed, err := config.Parse(mustMarshal(doc, ""), p)
	if err != nil {
		return nil, &config.Error{Err: fmt.Errorf("%s: translation is invalid (deps bug): %w", p, err)}
	}
	windows := windowsOf(parsed)
	header := []string{
		"Translated from " + shown + " by deps (Renovate compatibility).",
		"branchPrefix " + parsed.Prefix() + ": deps run closes open PRs under it that it does not manage; disable Renovate before the first deps run.",
	}
	if len(windows) > 0 {
		header = append(header, WindowAdvice)
	}
	if len(n.list) > 0 {
		header = append(header, "")
	}
	for _, note := range n.list {
		header = append(header, fmt.Sprintf("%s %s: %s", note.Level, note.Path, note.Msg))
	}
	out := mustMarshal(doc, strings.Join(header, "\n"))
	if _, err := config.Parse(out, p); err != nil {
		return nil, &config.Error{Err: fmt.Errorf("%s: translation is invalid (deps bug): %w", p, err)}
	}
	return &Result{Path: p, YAML: out, Notes: n.list, Windows: windows}, nil
}

// WindowAdvice explains what schedule time windows need from the workflow.
const WindowAdvice = "Schedules are time windows: the workflow must run inside each of them, e.g. hourly (on.schedule cron \"0 * * * *\")."

// windowsOf returns the sorted distinct schedule entries of c that restrict when PRs are
// created ("at any time" does not).
func windowsOf(c *config.Config) []string {
	set := map[string]bool{}
	add := func(l []string) {
		for _, s := range l {
			if !strings.EqualFold(s, "at any time") {
				set[s] = true
			}
		}
	}
	add(c.Schedule)
	for _, r := range c.Rules {
		add(r.Schedule)
	}
	if c.LockFileMaintenance.Enabled {
		add(c.LockFileMaintenance.Schedule)
	}
	return slices.Sorted(maps.Keys(set))
}

// omap is an ordered YAML mapping.
type omap []field

type field struct {
	k string
	v any
}

func (m *omap) set(k string, v any) { *m = append(*m, field{k, v}) }

// item is a rules or custom element with its provenance (the YAML head comment).
type item struct {
	origin string
	body   omap
}

func toNode(v any) *yaml.Node {
	switch v := v.(type) {
	case omap:
		n := &yaml.Node{Kind: yaml.MappingNode}
		for _, f := range v {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: f.k}, toNode(f.v))
		}
		return n
	case []item:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		for _, it := range v {
			c := toNode(it.body)
			if it.origin != "" && len(c.Content) > 0 {
				c.Content[0].HeadComment = it.origin
			}
			n.Content = append(n.Content, c)
		}
		return n
	case []string:
		n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, s := range v {
			c := &yaml.Node{}
			_ = c.Encode(s)
			n.Content = append(n.Content, c)
		}
		return n
	}
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		panic(fmt.Sprintf("renovate: encode %T: %v", v, err)) // only plain values reach here
	}
	return n
}

// mustMarshal renders m as a YAML document with 2-space indentation.
func mustMarshal(m omap, headComment string) []byte {
	doc := &yaml.Node{Kind: yaml.DocumentNode, HeadComment: headComment, Content: []*yaml.Node{toNode(m)}}
	out, err := yaml.Dump(doc, yaml.WithV3Defaults(), yaml.WithIndent(2), yaml.WithLineWidth(-1))
	if err != nil {
		panic(fmt.Sprintf("renovate: marshal: %v", err)) // the nodes are built from plain values
	}
	return out
}

var elementPrefixRe = regexp.MustCompile(`^(rules\[0\]\.|custom\[0\]\.|customDatasources\.[^.:]+\.)`)

// check validates a .deps.yaml fragment; it returns "" or its problems as "<field>: <msg>",
// the field without the element prefix, joined with "; ".
func check(fragment omap) string {
	_, err := config.Parse(mustMarshal(fragment, ""), "renovate")
	if err == nil {
		return ""
	}
	ce, ok := errors.AsType[*config.Error](err)
	if !ok || len(ce.Problems()) == 0 {
		return err.Error()
	}
	var lines []string
	for _, p := range ce.Problems() {
		l := p.Msg
		if p.Field != "" {
			l = p.Field + ": " + l
		}
		lines = append(lines, elementPrefixRe.ReplaceAllString(l, ""))
	}
	return strings.Join(lines, "; ")
}

// expansion is the deps manager of a Renovate manager, restricted to depTypes (nil = all).
type expansion struct {
	manager  string
	depTypes []string
}

// managerMap maps the Renovate managers deps implements.
var managerMap = func() map[string][]expansion {
	m := map[string][]expansion{
		"github-actions":    {{"github-actions", nil}, {"docker", []string{"workflow", "action"}}},
		"dockerfile":        {{"docker", []string{"dockerfile"}}},
		"docker-compose":    {{"docker", []string{"compose"}}},
		"kubernetes":        {{"docker", []string{"kubernetes"}}},
		"helm-values":       {{"docker", []string{"helm"}}},
		"helmv3":            {{"helm", []string{"chart"}}},
		"helmfile":          {{"helm", []string{"helmfile"}}},
		"helm-requirements": {{"helm", []string{"requirements"}}},
		"flux":              {{"helm", []string{"flux"}}},
		"argocd":            {{"helm", []string{"argocd"}}},
		"sveltos":           {{"helm", []string{"sveltos"}}},
		"bun":               {{"npm", nil}},
		"pip-compile":       {{"pip_requirements", nil}},
		"gitlabci-include":  {{"gitlabci", []string{"include"}}},
		"regex":             {{"custom", nil}},
		"custom.regex":      {{"custom", nil}},
		"custom.jsonata":    {{"custom", nil}},
	}
	for n := range strings.FieldsSeq("gomod kustomize npm nvm nodenv asdf mise pre-commit gitlabci cargo pip_requirements pep621 pep723 setup-cfg pip_setup poetry pipenv pyenv runtime-version homeassistant-manifest terraform terraform-version tflint-plugin terragrunt maven gradle gradle-wrapper maven-wrapper swift bundler ruby-version git-submodules rust-toolchain devcontainer bitbucket-pipelines woodpecker droneci circleci azure-pipelines composer julia bazel-module bazelisk nuget homebrew ansible ansible-galaxy sbt scalafmt pub fvm mix gleam nix") {
		m[n] = []expansion{{n, nil}}
	}
	return m
}()

// depTypeUniverse lists the dep types of deps managers that several Renovate managers share.
var depTypeUniverse = map[string][]string{
	"docker":   {"dockerfile", "compose", "workflow", "action", "kubernetes", "helm"},
	"helm":     {"chart", "requirements", "helmfile", "k0s", "flux", "argocd", "sveltos"},
	"gitlabci": {"image", "service", "include", "component"},
}

// categories maps Renovate manager categories (lib/modules/manager/*/index.ts) to the
// mapped Renovate managers.
var categories = map[string][]string{
	"ci":         {"github-actions", "gitlabci", "gitlabci-include", "bitbucket-pipelines", "woodpecker", "droneci", "circleci", "azure-pipelines"},
	"golang":     {"gomod"},
	"docker":     {"dockerfile", "docker-compose", "devcontainer"},
	"kubernetes": {"kubernetes", "helm-requirements", "helm-values", "helmv3", "helmfile", "flux", "argocd", "sveltos", "kustomize"},
	"helm":       {"helm-requirements", "helm-values", "helmv3", "helmfile"},
	"cd":         {"helmfile", "flux", "argocd", "sveltos"},
	"js":         {"npm", "bun", "nvm", "nodenv"},
	"node":       {"nvm", "nodenv"},
	"rust":       {"cargo", "rust-toolchain"},
	"python":     {"pip_requirements", "pip-compile", "pep621", "pep723", "setup-cfg", "pip_setup", "poetry", "pipenv", "pyenv", "runtime-version", "homeassistant-manifest"},
	"terraform":  {"terraform", "terraform-version", "tflint-plugin", "terragrunt"},
	"iac":        {"terraform", "terragrunt", "ansible", "ansible-galaxy"},
	"ansible":    {"ansible", "ansible-galaxy"},
	"java":       {"maven", "gradle", "gradle-wrapper", "maven-wrapper", "sbt", "scalafmt"},
	"ruby":       {"bundler", "ruby-version"},
	"swift":      {"swift"},
	"php":        {"composer"},
	"julia":      {"julia"},
	"bazel":      {"bazel-module", "bazelisk"},
	"dotnet":     {"nuget"},
	"custom":     {"custom.regex", "custom.jsonata"},
	"dart":       {"pub", "fvm"},
	"elixir":     {"mix", "gleam"},
}

// datasourceMap maps the Renovate datasources whose deps datasource has another name that
// config.DatasourceAliases does not cover.
var datasourceMap = map[string]string{"julia-general-metadata": "julia-registry"}

// infoKeys have no effect on which updates or PRs exist.
var infoKeys = func() map[string]bool {
	m := map[string]bool{}
	for k := range strings.FieldsSeq(`assigneesFromCodeOwners assignAutomerge
		reviewersFromCodeOwners additionalReviewers automergeComment keepUpdatedLabel milestone
		commitBody commitBodyTable commitTrailers prTitle prTitleStrict prPriority
		prNotPendingHours configMigration onboarding onboardingConfig onboardingConfigFileName requireConfig
		printConfig dependencyDashboardHeader dependencyDashboardFooter dependencyDashboardAutoclose
		dependencyDashboardOSVVulnerabilitySummary dependencyDashboardCategory customizeDashboard userStrings
		branchConcurrentLimit commitHourlyLimit branchNameStrict branchTopic hashedBranchLength additionalBranchPrefix
		groupSlug fetchChangeLogs recreateWhen suppressNotifications ignoreTests internalChecksFilter
		internalChecksAsSuccess gitNoVerify statusCheckNames cloneSubmodules persistRepoData confidential
		respectLatest hostRules postUpdateOptions semanticCommits`) {
		m[k] = true
	}
	return m
}()

func isInfoKey(k string) bool {
	return infoKeys[k] || strings.HasPrefix(k, "commitMessage") || strings.HasPrefix(k, "prBody")
}

// silentKeys are consumed without a note.
func silentKey(k string) bool { return k == "$schema" || k == "description" || k == originKey }

type translator struct {
	n         *notes
	monorepos bool
	repl      replacementPresets
	cds       omap            // translated customDatasources
	cdKept    map[string]bool // names in cds
	custom    []item
	rules     []item
	// exclusionRules are appended after every other rule (see exclusions).
	exclusionRules []item
	dashboard      bool            // the translated dependencyDashboard; rule settings like dashboardApproval need it
	own            map[string]any  // the repository's config before presets
	remote         map[string]bool // top-level keys set by fetched presets
	titleTop       map[string]any  // top-level title settings (titleKeys) the repository sets
	semType        any             // top-level semanticCommitType, nil = unset
}

// builtinOrigin reports whether origin ("<preset> packageRules[i]") is a rule of one of
// Renovate's built-in presets.
func builtinOrigin(origin string) bool {
	name, _, ok := strings.Cut(origin, " ")
	if !ok {
		return false
	}
	ref, err := parsePreset(name)
	return err == nil && ref.source == "internal"
}

// builtinOnly reports whether top-level key k comes from Renovate's built-in presets only:
// neither the repository nor a fetched preset sets it.
func (t *translator) builtinOnly(k string) bool {
	_, own := t.own[k]
	return !own && !t.remote[k]
}

// untranslated reports key k (at path) that has no translation.
func (t *translator) untranslated(path, k string) {
	switch {
	case silentKey(k):
	case k == "hostRules":
		t.n.info(path, "credentials come from the environment (deps help environment)")
	case isInfoKey(k):
		t.n.info(path, "no deps equivalent; ignored")
	default:
		t.n.warn(path, "not supported; ignored")
	}
}

// strs returns v as a string list (a string is one entry).
func strs(v any) ([]string, bool) {
	switch v := v.(type) {
	case string:
		return []string{v}, true
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// translate converts the resolved config into the .deps.yaml document.
func (t *translator) translate(cfg map[string]any) omap {
	done := map[string]bool{"enabled": true}
	get := func(k string) (any, bool) {
		v, ok := cfg[k]
		done[k] = true
		return v, ok
	}
	var doc omap
	// emit validates the top-level key alone and adds it, or drops it with a note.
	emit := func(k string, v any) {
		frag := omap{{k, v}}
		if k == "dashboardApproval" || k == "prCreation" {
			frag = t.ruleContext(frag)
		}
		if msg := check(frag); msg != "" {
			t.n.warn(k, "dropped: %s", msg)
			return
		}
		doc.set(k, v)
	}

	prefix := "renovate/"
	if v, ok := get("branchPrefix"); ok {
		if s, ok := v.(string); ok {
			prefix = s
		}
	}
	emit("branchPrefix", prefix)
	t.n.warn("branchPrefix", "deps run closes open PRs under %s that it does not manage; disable Renovate before the first deps run", prefix)
	for _, k := range []string{"baseBranchPatterns", "baseBranches"} {
		if v, ok := get(k); ok {
			l, ok := strs(v)
			if !ok {
				t.n.warn(k, "must be a list of strings; ignored")
				continue
			}
			if l = t.baseBranches(k, l); len(l) > 0 {
				emit("baseBranches", l)
			}
			break
		}
	}
	if v, ok := get("gitAuthor"); ok {
		emit("gitAuthor", v)
	}
	if v, ok := get("platformCommit"); ok {
		switch v {
		case true:
			v = "enabled"
		case false:
			v = "disabled"
		}
		emit("platformCommit", v)
	}
	if v, ok := get("gitIgnoredAuthors"); ok {
		if l, ok := strs(v); ok {
			emit("gitIgnoredAuthors", l)
		}
	}
	for _, lim := range []struct {
		k   string
		def int
	}{{"prConcurrentLimit", 10}, {"prHourlyLimit", 2}} {
		v, ok := get(lim.k)
		if !ok {
			emit(lim.k, lim.def)
			continue
		}
		if f, isNum := v.(float64); isNum && f >= 0 && f == math.Trunc(f) && f <= math.MaxInt32 {
			emit(lim.k, int(f))
		} else {
			t.n.warn(lim.k, "must be a non-negative integer, got %v; dropped", v)
		}
	}
	if v, _ := get("pinDigests"); v == true {
		emit("pin", config.PinDigest)
	}
	if v, ok := get("rangeStrategy"); ok {
		if s, ok := t.rangeStrategy("rangeStrategy", v); ok {
			emit("rangeStrategy", s)
		}
	}
	va, _ := get("vulnerabilityAlerts")
	osv, _ := get("osvVulnerabilityAlerts")
	vam, isMap := va.(map[string]any)
	if va != nil && !isMap {
		t.n.warn("vulnerabilityAlerts", "must be an object; ignored")
	}
	security := vam["enabled"] != false || osv == true
	emit("security", security)
	t.n.info("vulnerabilityAlerts", "security updates come from OSV")
	t.n.builtin = t.builtinOnly("vulnerabilityAlerts")
	securityGroup := t.vulnerabilityAlerts(vam)
	t.n.builtin = false
	emit("monorepos", t.monorepos)
	emit("replacements", t.replacements(t.repl))
	automerge, _ := cfg["automerge"].(bool)
	rebase := ""
	if v, ok := get("rebaseWhen"); ok {
		switch s, _ := v.(string); s {
		case config.RebaseConflicted, config.RebaseBehindBase, config.RebaseNever:
			rebase = s
		case "auto":
		default:
			t.n.warn("rebaseWhen", "unsupported value %v; auto used", v)
		}
	}
	if rebase == "" {
		rebase = config.RebaseConflicted
		if automerge {
			rebase = config.RebaseBehindBase
		}
	}
	emit("rebaseWhen", rebase)
	for _, k := range []string{"separateMajorMinor", "separateMultipleMajor", "separateMinorPatch", "updateNotScheduled"} {
		if v, ok := get(k); ok {
			emit(k, v)
		}
	}
	mra, hasMRA := get("minimumReleaseAge")
	sd, hasSD := get("stabilityDays")
	switch {
	case hasMRA && mra != nil:
		emit("minimumReleaseAge", mra)
	case !hasMRA && hasSD:
		if f, ok := sd.(float64); ok && f > 0 {
			emit("minimumReleaseAge", strconv.FormatFloat(f, 'f', -1, 64)+" days")
		}
	}
	// Renovate 42+ holds back versions without a release time (timestamp-required).
	mrab := any("timestamp-required")
	if v, ok := get("minimumReleaseAgeBehaviour"); ok {
		mrab = v
	}
	emit("minimumReleaseAgeBehaviour", mrab)
	if v, ok := get("schedule"); ok {
		if s, ok := t.schedule("schedule", v); ok {
			emit("schedule", s)
		}
	}
	if v, ok := get("timezone"); ok {
		emit("timezone", v)
	}
	if v, ok := get("registryAliases"); ok {
		if m, isMap := v.(map[string]any); isMap {
			var aliases omap
			for _, k := range slices.Sorted(maps.Keys(m)) {
				aliases.set(k, m[k])
			}
			emit("registryAliases", aliases)
		} else {
			t.n.warn("registryAliases", "must be an object; ignored")
		}
	}
	ignorePaths := []string{"**/node_modules/**", "**/bower_components/**"}
	if v, ok := get("ignorePaths"); ok {
		l, _ := strs(v)
		ignorePaths = t.ignorePaths(l)
	}
	emit("ignorePaths", ignorePaths)
	if v, ok := get("automerge"); ok {
		emit("automerge", v)
	}
	if v, ok := get("automergeStrategy"); ok {
		if s, ok := t.automergeStrategy("automergeStrategy", v); ok {
			emit("automergeStrategy", s)
		}
	}
	if v, ok := get("automergeType"); ok {
		if s, ok := t.automergeType("automergeType", v); ok {
			emit("automergeType", s)
		}
	}
	labels, _ := strs(cfg["labels"])
	if add, ok := get("addLabels"); ok {
		l, _ := strs(add)
		for _, a := range l {
			labels = appendNew(labels, a)
		}
	}
	if _, ok := get("labels"); ok || len(labels) > 0 {
		emit("labels", labels)
	}
	for _, k := range []string{"reviewers", "assignees"} {
		if v, ok := get(k); ok {
			l, _ := strs(v)
			emit(k, l)
		}
	}
	// PR titles are composed only of the repository's settings (semanticCommits also from
	// :semanticCommitsDisabled), not from Renovate's built-in defaults.
	t.titleTop = map[string]any{}
	for k := range titleKeys {
		if v, ok := cfg[k]; ok && (k == "semanticCommits" || !t.builtinOnly(k)) {
			done[k] = true
			t.titleTop[k] = v
		}
	}
	t.semType = cfg["semanticCommitType"]
	if s, ok := t.title("", t.titleTop, nil); ok {
		emit("prTitle", s)
	}
	for _, k := range []string{"prHeader", "prFooter"} {
		if v, ok := get(k); ok {
			if s, ok := t.prText(k, v); ok && s != "" {
				emit(k, s)
			}
		}
	}
	rebaseLabel := config.DefaultRebaseLabel
	if v, ok := get("rebaseLabel"); ok {
		if msg := check(omap{{"rebaseLabel", v}}); msg != "" {
			t.n.warn("rebaseLabel", "dropped: %s", msg)
		} else {
			rebaseLabel, _ = v.(string)
			doc.set("rebaseLabel", v)
		}
	}
	if v, ok := get("stopUpdatingLabel"); ok {
		// Validated together with rebaseLabel: the two must differ.
		if msg := check(omap{{"rebaseLabel", rebaseLabel}, {"stopUpdatingLabel", v}}); msg != "" {
			t.n.warn("stopUpdatingLabel", "dropped: %s", msg)
		} else {
			doc.set("stopUpdatingLabel", v)
		}
	}
	for _, k := range []string{"dependencyDashboard", "dependencyDashboardTitle", "dependencyDashboardLabels"} {
		if v, ok := get(k); ok {
			if l, isList := strs(v); isList && k == "dependencyDashboardLabels" {
				v = l
			}
			emit(k, v)
			if k == "dependencyDashboard" {
				t.dashboard = v == true
			}
		}
	}
	if v, ok := get("dependencyDashboardApproval"); ok {
		emit("dashboardApproval", v)
	}
	if v, ok := get("draftPR"); ok {
		emit("draftPR", v)
	}
	if v, ok := get("prCreation"); ok {
		if s, ok := t.prCreation("prCreation", v); ok {
			emit("prCreation", s)
		}
	}
	if v, ok := get("prNotPendingHours"); ok {
		if f, isNum := v.(float64); isNum && f >= 0 && f == math.Trunc(f) && f <= math.MaxInt32 {
			emit("prNotPendingHours", int(f))
		} else {
			t.n.warn("prNotPendingHours", "must be a non-negative integer, got %v; dropped", v)
		}
	}
	if v, ok := get("abandonmentThreshold"); ok && v != nil {
		emit("abandonmentThreshold", v) // null: Renovate's default, no abandonment check
	}
	if v, ok := get("composerIgnorePlatformReqs"); ok {
		// Renovate: [] (its default) ignores ext-* and lib-*, null none, a list those.
		switch l, isList := strs(v); {
		case v == nil:
			emit("composerIgnorePlatformReqs", []string{})
		case !isList:
			t.n.warn("composerIgnorePlatformReqs", "must be a list of strings or null; ignored")
		case len(l) == 0:
			emit("composerIgnorePlatformReqs", []string{"ext-*", "lib-*"})
		default:
			emit("composerIgnorePlatformReqs", l)
		}
	}
	if v, ok := get("lockFileMaintenance"); ok {
		if l, ok := t.lockFileMaintenance(v); ok {
			emit("lockFileMaintenance", l)
		}
	}
	if v, _ := get("platformAutomerge"); v == false {
		t.n.warn("platformAutomerge", "deps always uses platform auto-merge")
	}
	if v, ok := get("postUpdateOptions"); ok {
		l, _ := strs(v)
		for _, o := range l {
			if o == "gomodUpdateImportPaths" {
				t.n.info("postUpdateOptions", "deps default (gomodUpdateImportPaths: true)")
			} else {
				t.n.info("postUpdateOptions", "%s: no deps equivalent; ignored", o)
			}
		}
	}

	if v, ok := get("customDatasources"); ok {
		t.customDatasources(v)
	}
	excluded := t.exclusions(cfg, done)
	if !excluded["custom"] {
		for _, k := range []string{"customManagers", "regexManagers"} {
			if v, ok := get(k); ok {
				list, _ := v.([]any)
				for i, el := range list {
					m, _ := el.(map[string]any)
					t.customManager(m, k == "regexManagers", originOf(m, fmt.Sprintf("%s[%d]", k, i)))
				}
			}
		}
	} else {
		for _, k := range []string{"customManagers", "regexManagers"} {
			if _, ok := get(k); ok {
				t.n.info(k, "the custom regex manager is disabled; dropped")
			}
		}
	}
	t.rulesFrom(cfg, done)
	// Renovate forces vulnerabilityAlerts over every packageRule: the last rule wins in deps.
	if security && securityGroup != "" {
		t.addRule("vulnerabilityAlerts", omap{{"securityGroup", securityGroup}})
	}
	t.rules = append(t.rules, t.exclusionRules...)

	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range cfg {
			if !yield(k) {
				return
			}
		}
	}) {
		if !done[k] {
			t.n.builtin = t.builtinOnly(k)
			t.untranslated(k, k)
			t.n.builtin = false
		}
	}
	if len(t.cds) > 0 {
		doc.set("customDatasources", t.cds)
	}
	if len(t.custom) > 0 {
		doc.set("custom", t.custom)
	}
	if len(t.rules) > 0 {
		doc.set("rules", t.rules)
	}
	return doc
}

func originOf(m map[string]any, def string) string {
	if s, ok := m[originKey].(string); ok {
		return s
	}
	return def
}

// vulnerabilityAlerts reports the keys of the vulnerabilityAlerts object, the config
// Renovate forces on its security-update PRs, and returns its groupName ("" = none):
// rules[].securityGroup without matchers, since Renovate groups all of them.
func (t *translator) vulnerabilityAlerts(va map[string]any) string {
	var group string
	for _, k := range slices.Sorted(mapKeys(va)) {
		v, p := va[k], "vulnerabilityAlerts."+k
		switch k {
		case "enabled": // security
		case "groupName":
			switch s, ok := v.(string); {
			case ok:
				group = s
			case v != nil:
				t.n.warn(p, "must be a string; ignored")
			}
		case "schedule":
			t.n.info(p, "Renovate and deps both ignore schedules for security updates")
		case "minimumReleaseAge":
			if v != nil {
				t.n.warn(p, "deps security updates bypass minimumReleaseAge; ignored")
			}
		case "dependencyDashboardApproval":
			if v != false {
				t.n.info(p, "deps never holds back security updates for approval")
			}
		case "vulnerabilityFixStrategy":
			if v == "lowest" {
				t.n.info(p, "deps default: the lowest update at or above the minimum fixed version")
			} else {
				t.n.warn(p, "not supported; deps picks the lowest update at or above the minimum fixed version")
			}
		case "labels", "addLabels", "assignees", "reviewers":
			t.n.info(p, "no deps equivalent for security updates only; ignored")
		default:
			if silentKey(k) {
				continue
			}
			if isInfoKey(k) {
				t.n.info(p, "no deps equivalent for security updates only; ignored")
			} else {
				t.n.warn(p, "no deps equivalent for security updates only; ignored")
			}
		}
	}
	return group
}

// rangeStrategy maps a Renovate rangeStrategy.
func (t *translator) rangeStrategy(path string, v any) (string, bool) {
	s, _ := v.(string)
	if slices.Contains(config.RangeStrategies, s) {
		return s, true
	}
	t.n.warn(path, "rangeStrategy %v is not supported; dropped", v)
	return "", false
}

func (t *translator) automergeStrategy(path string, v any) (string, bool) {
	switch s, _ := v.(string); s {
	case config.StrategyAuto, config.StrategySquash, config.StrategyRebase:
		return s, true
	case "merge-commit":
		return config.StrategyMerge, true
	}
	t.n.warn(path, "automergeStrategy %v is not supported; dropped", v)
	return "", false
}

func (t *translator) automergeType(path string, v any) (string, bool) {
	switch s, _ := v.(string); s {
	case config.AutomergeTypePR:
		return s, true
	case "branch":
		t.n.warn(path, "deps automerges through the PR; dropped")
		return "", false
	}
	t.n.warn(path, "automergeType %v is not supported; dropped", v)
	return "", false
}

// schedule translates Renovate schedule entries into workflow trigger crons; ok is false
// when no entry could be translated, so the key is omitted.
func (t *translator) schedule(path string, v any) ([]string, bool) {
	entries, ok := strs(v)
	if !ok {
		t.n.warn(path, "schedule must be a list of strings; dropped")
		return nil, false
	}
	// deps evaluates Renovate's time windows (text and crons with minute *) itself, so they are
	// kept as written. A cron with a fixed minute would be a workflow trigger in deps, but
	// Renovate rejects it as a schedule.
	out := []string{}
	for _, e := range entries {
		e = schedule.Normalize(e)
		switch f := strings.Fields(e); {
		case len(f) == 5 && f[0] != "*" && schedule.ValidateCron(e) == nil:
			t.n.warn(path, "schedule %q: a cron needs * as minutes in Renovate (it rejects this one); entry dropped", e)
			continue
		case schedule.Validate(e) != nil:
			t.n.warn(path, "schedule %q: %v; entry dropped", e, schedule.Validate(e))
			continue
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	if len(entries) > 0 && len(out) == 0 {
		t.n.warn(path, "no schedule entry could be translated; the schedule is omitted (a rule inherits the top-level schedule)")
		return nil, false
	}
	return out, true
}

// ignorePaths converts Renovate ignorePaths entries: plain strings match as substrings in
// Renovate, so they become regexes.
func (t *translator) ignorePaths(in []string) []string {
	out := []string{}
	for i, p := range in {
		path := fmt.Sprintf("ignorePaths[%d]", i)
		switch {
		case strings.HasPrefix(p, "!"):
			t.n.warn(path, "negation %q is not supported in ignorePaths; dropped", p)
			continue
		case strings.HasPrefix(p, "/"):
			if _, _, ok := pattern.Regex(p); ok {
				break
			}
			fallthrough
		case !strings.ContainsAny(p, "*?[{"):
			p = "/" + regexp.QuoteMeta(p) + "/"
		}
		if _, err := pattern.Compile([]string{p}); err != nil {
			t.n.warn(path, "%v; dropped", err)
			continue
		}
		out = append(out, p)
	}
	return out
}

func (t *translator) lockFileMaintenance(v any) (omap, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		t.n.warn("lockFileMaintenance", "must be an object; dropped")
		return nil, false
	}
	enabled, _ := m["enabled"].(bool)
	out := omap{{"enabled", enabled}}
	for _, k := range slices.Sorted(mapKeys(m)) {
		switch k {
		case "enabled":
		case "schedule":
			if enabled {
				if s, ok := t.schedule("lockFileMaintenance.schedule", m[k]); ok {
					out.set("schedule", s)
				}
			}
		default:
			t.n.info("lockFileMaintenance."+k, "no deps equivalent; ignored")
		}
	}
	if _, ok := m["schedule"]; enabled && !ok {
		if s, ok := t.schedule("lockFileMaintenance.schedule", "before 4am on monday"); ok {
			out.set("schedule", s)
		}
	}
	return out, true
}

func mapKeys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// customDatasources translates customDatasources (name → {url, format}).
func (t *translator) customDatasources(v any) {
	m, _ := v.(map[string]any)
	for _, name := range slices.Sorted(mapKeys(m)) {
		path := "customDatasources." + name
		cd, _ := m[name].(map[string]any)
		out := omap{}
		ok := true
		for _, k := range slices.Sorted(mapKeys(cd)) {
			v := cd[k]
			switch k {
			case "defaultRegistryUrlTemplate":
				s, _ := v.(string)
				if strings.Contains(s, "{{") {
					t.n.warn(path, "templated defaultRegistryUrlTemplate has no deps equivalent; datasource dropped")
					ok = false
				}
				out = append(omap{{"url", s}}, out...)
			case "format":
				switch s, _ := v.(string); s {
				case "json", "yaml", "plain":
					out.set("format", s)
				default:
					t.n.warn(path, "format %v is not supported; datasource dropped", v)
					ok = false
				}
			case "transformTemplates":
				l, _ := strs(v)
				switch {
				case len(l) == 0:
				case len(l) == 1:
					versions, versionRegex, translated := jsonataVersions(l[0])
					if !translated {
						t.n.warn(path, "JSONata transformTemplates %q has no deps equivalent; write versions/versionRegex by hand; datasource dropped", l[0])
						ok = false
						break
					}
					out.set("versions", versions)
					if versionRegex != "" {
						out.set("versionRegex", versionRegex)
					}
					t.n.info(path, "transformTemplates translated to versions %q", versions)
				default:
					t.n.warn(path, "several JSONata transformTemplates have no deps equivalent; write versions/versionRegex by hand; datasource dropped")
					ok = false
				}
			default:
				t.untranslated(path+"."+k, k)
			}
		}
		if !ok {
			continue
		}
		if msg := check(omap{{"customDatasources", omap{{name, out}}}}); msg != "" {
			t.n.warn(path, "dropped: %s", msg)
			continue
		}
		t.cds.set(name, out)
		t.cdKept[name] = true
	}
}

// customManager translates one customManagers (or regexManagers) element. A regex manager
// becomes one regex entry per matchStrings element, a jsonata manager one JSONata entry per
// query. A jsonata manager's literal (non-template) templates override the query's fields in
// Renovate, while deps' entry fields are defaults the query overrides, so they are merged into
// each query's results instead.
func (t *translator) customManager(m map[string]any, legacy bool, origin string) {
	t.n.builtin = builtinOrigin(origin)
	defer func() { t.n.builtin = false }()
	ct, _ := m["customType"].(string)
	jsonata := !legacy && ct == "jsonata"
	if !jsonata && ct != "regex" && (!legacy || m["customType"] != nil) {
		t.n.warn(origin, "customType %v is not supported; dropped", m["customType"])
		return
	}
	var files, matches []string
	var datasource, depName, pkgName, versioning, registry, fileFormat string
	var extractVersion, registryExpr omap // jsonata: extractVersionTemplate, templated registryUrlTemplate
	for _, k := range slices.Sorted(mapKeys(m)) {
		v := m[k]
		s, _ := v.(string)
		switch k {
		case "customType":
		case "managerFilePatterns":
			l, _ := strs(v)
			for _, f := range l {
				body := strings.TrimPrefix(f, "!")
				if _, flags, re := pattern.Regex(body); strings.HasPrefix(body, "/") && (!re || (flags != "" && flags != "i")) {
					t.n.warn(origin, "managerFilePatterns %q is a glob starting with / (not /regex/): it matches no repository path, in Renovate neither", f)
				}
			}
			files = append(files, l...)
		case "fileMatch":
			l, _ := strs(v)
			for _, f := range l {
				files = append(files, "/"+f+"/")
			}
		case "matchStrings":
			matches, _ = strs(v)
		case "matchStringsStrategy":
			if s != "any" {
				t.n.warn(origin, "matchStringsStrategy %v is not supported; dropped", v)
				return
			}
		case "datasourceTemplate", "packageNameTemplate", "depNameTemplate", "versioningTemplate":
			if groupTemplate(k, s) {
				continue // the match's own group, which deps reads anyway
			}
			if strings.Contains(s, "{{") {
				t.n.warn(origin, "%s %q is a template; dropped", k, s)
				return
			}
			switch k {
			case "datasourceTemplate":
				datasource = depsDatasource(s)
			case "versioningTemplate":
				versioning = s
			case "packageNameTemplate":
				pkgName = s
			case "depNameTemplate":
				depName = s
			}
		case "extractVersionTemplate":
			if !jsonata {
				t.n.warn(origin, "%s has no deps equivalent in regex entries (only JSONata results carry extractVersion); dropped", k)
				return
			}
			if !strings.Contains(s, "{{") {
				extractVersion.set("extractVersion", s)
				continue
			}
			e, bad := templateJSONata(s)
			if bad != "" {
				t.n.warn(origin, "%s %q: %s has no JSONata translation (only {{field}} references do); dropped", k, s, bad)
				return
			}
			extractVersion.set("extractVersion", e)
		case "currentValueTemplate", "autoReplaceStringTemplate":
			t.n.warn(origin, "%s has no deps equivalent; dropped", k)
			return
		case "registryUrlTemplate":
			switch {
			case groupTemplate(k, s):
			case !strings.Contains(s, "{{"):
				registry = s
			case !jsonata:
				t.n.info(origin+"."+k, "templated registryUrlTemplate %q has no deps equivalent in regex entries; ignored (capture the URL in a (?<registryUrl>) group instead)", s)
			default:
				if e, bad := templateJSONata(s); bad != "" {
					t.n.info(origin+"."+k, "templated registryUrlTemplate %q: %s has no JSONata translation (only {{field}} references do); ignored", s, bad)
				} else {
					registryExpr.set("registryUrl", e)
				}
			}
		case "depTypeTemplate":
			t.n.info(origin+"."+k, "no deps equivalent; ignored")
		case "fileFormat":
			if !jsonata {
				t.untranslated(origin+"."+k, k)
				continue
			}
			fileFormat = s
		default:
			t.untranslated(origin+"."+k, k)
		}
	}
	nonEmpty := func(fs omap) omap { return slices.DeleteFunc(fs, func(f field) bool { return f.v == "" }) }
	fields := nonEmpty(omap{{"datasource", datasource}, {"name", cmp.Or(pkgName, depName)}, {"versioning", versioning}, {"registryUrl", registry}})
	var merge omap
	if jsonata {
		merge = append(nonEmpty(omap{{"datasource", datasource}, {"depName", depName}, {"packageName", pkgName}, {"versioning", versioning}, {"registryUrl", registry}}), append(registryExpr, extractVersion...)...)
	}
	for i, match := range matches {
		c := append(omap{{"files", files}, {"match", match}}, fields...)
		checked := c
		if jsonata {
			c = omap{{"files", files}, {"fileFormat", fileFormat}, {"jsonata", jsonataMerge(match, merge)}}
			checked = append(slices.Clip(c), fields...) // validate the merged values like entry fields
		}
		o := origin
		if len(matches) > 1 {
			o = fmt.Sprintf("%s.matchStrings[%d]", origin, i)
		}
		if msg := check(omap{{"customDatasources", t.cds}, {"custom", []item{{body: checked}}}}); msg != "" {
			t.n.warn(o, "dropped: %s", msg)
			continue
		}
		t.custom = append(t.custom, item{origin: o, body: c})
	}
}

// groupTemplate reports whether the <field>Template value s only renders the match group of
// the same field ({{{datasource}}} as datasourceTemplate), which deps' regex mode reads
// without a template.
func groupTemplate(key, s string) bool {
	field := strings.TrimSuffix(key, "Template")
	inner, ok := strings.CutPrefix(s, "{{{")
	if ok {
		inner, ok = strings.CutSuffix(inner, "}}}")
	} else if inner, ok = strings.CutPrefix(s, "{{"); ok {
		inner, ok = strings.CutSuffix(inner, "}}")
	}
	return ok && strings.TrimSpace(inner) == field
}

// ruleContext adds the top-level settings rule validation depends on to a check fragment.
func (t *translator) ruleContext(frag omap) omap {
	if t.dashboard {
		return append(omap{{"dependencyDashboard", true}}, frag...)
	}
	return frag
}

// prCreation translates a prCreation value.
func (t *translator) prCreation(path string, v any) (string, bool) {
	if s, _ := v.(string); slices.Contains(config.PRCreations, s) {
		return s, true
	}
	t.n.warn(path, "unsupported value %v; dropped", v)
	return "", false
}
