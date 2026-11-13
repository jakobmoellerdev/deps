package renovate

import (
	"cmp"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pattern"
)

// updateTypeBlocks are the top-level update-type objects that become rules, with their deps
// update type.
var updateTypeBlocks = []struct{ key, typ string }{
	{"major", "major"}, {"minor", "minor"}, {"patch", "patch"}, {"digest", "digest"}, {"pin", "pin"}, {"pinDigest", "pin"},
	{"replacement", "replacement"},
}

// effectOrder is the output order of rule effects (the field order of config.Rule).
var effectOrder = []string{"ignore", "pin", "allow", "rangeStrategy", "versioning", "extractVersion", "registryUrls", "replacementName",
	"replacementVersion", "replacementPrefix", "separate", "group", "commitType", "commitScope", "schedule", "additionalBranchPrefix",
	"minimumReleaseAge", "minimumReleaseAgeBehaviour", "abandonmentThreshold", "automerge", "automergeStrategy", "automergeType",
	"labels", "addLabels", "reviewers", "assignees", "prTitle", "prHeader", "prFooter", "postUpgradeTasks", "dashboardApproval",
	"draftPR", "prCreation"}

// managerDepTypes are the dep types of each deps manager.
var managerDepTypes = config.ManagerDepTypes()

// rulesFrom translates the rule sources in order: top-level semantic commit settings,
// manager blocks, update-type blocks, packageRules and ignoreDeps.
func (t *translator) rulesFrom(cfg map[string]any, done map[string]bool) {
	lead := map[string]any{}
	for _, k := range []string{"semanticCommitType", "semanticCommitScope"} {
		if v, ok := cfg[k]; ok {
			done[k] = true
			lead[k] = v
		}
	}
	if v, ok := cfg["postUpgradeTasks"]; ok {
		// A top-level postUpgradeTasks applies to every update; packageRules override it.
		done["postUpgradeTasks"] = true
		t.rule("postUpgradeTasks", map[string]any{"postUpgradeTasks": v})
	}
	if len(lead) > 0 {
		t.rule("semanticCommitType", lead)
	}
	for _, k := range slices.Sorted(mapKeys(cfg)) {
		if _, ok := managerMap[k]; !ok {
			continue
		}
		done[k] = true
		block, ok := cfg[k].(map[string]any)
		if !ok {
			t.n.warn(k, "must be an object; ignored")
			continue
		}
		r := map[string]any{}
		for bk, bv := range block {
			switch bk {
			case "enabled":
				// Renovate disables git-submodules and azure-pipelines by default, deps their
				// submodules and tasks: opting in needs a rule. Every other manager's enabled is
				// handled by exclusions.
				if (k == "git-submodules" || k == "azure-pipelines") && bv == true {
					r[bk] = bv
				}
			case "fileMatch", "managerFilePatterns":
				t.n.warn(k+"."+bk, "extra files for built-in managers are not supported")
			default:
				r[bk] = bv
			}
		}
		if len(r) > 0 {
			r["matchManagers"] = []any{k}
			t.rule(k, r)
		}
	}
	for _, b := range updateTypeBlocks {
		v, ok := cfg[b.key]
		if !ok {
			continue
		}
		done[b.key] = true
		block, _ := v.(map[string]any)
		if len(block) == 0 {
			continue
		}
		r := map[string]any{"matchUpdateTypes": []any{b.key}}
		maps.Copy(r, block)
		t.rule(b.key, r)
	}
	if _, ok := cfg["rollback"]; ok {
		done["rollback"] = true
		t.n.info("rollback", "deps never opens rollback PRs; ignored")
	}
	if v, ok := cfg["packageRules"]; ok {
		done["packageRules"] = true
		list, _ := v.([]any)
		for i, el := range list {
			m, ok := el.(map[string]any)
			if !ok {
				t.n.warn(fmt.Sprintf("packageRules[%d]", i), "must be an object; ignored")
				continue
			}
			t.rule(originOf(m, fmt.Sprintf("packageRules[%d]", i)), m)
		}
	}
	if v, ok := cfg["ignoreDeps"]; ok {
		done["ignoreDeps"] = true
		if l, ok := strs(v); ok && len(l) > 0 {
			t.rule("ignoreDeps", map[string]any{"matchDepNames": exactEntries(l), "enabled": false})
		}
	}
}

// exactEntries converts ignoreDeps names, which Renovate compares
// verbatim, into matcher entries; anything deps would read as a
// negation, regex or glob is anchored and quoted.
func exactEntries(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		if strings.HasPrefix(s, "!") || strings.HasPrefix(s, "/") || strings.ContainsAny(s, "*?[]{}") {
			s = "/^" + regexp.QuoteMeta(s) + "$/"
		}
		out[i] = s
	}
	return out
}

// regexEntry converts a deprecated *Patterns entry into the /re/ form. Renovate treats "*",
// ".*" and "^*$" as match-all (packageRules matchesRule); "^*$" is no valid RE2 and would
// drop the whole rule.
func regexEntry(p string) string {
	if p == "*" || p == ".*" || p == "^*$" {
		return "**"
	}
	return "/" + p + "/"
}

func prefixEntry(p string) string { return "/^" + regexp.QuoteMeta(p) + "/" }

// currentValue converts a matchCurrentValue pattern into the currentVersion regex form.
// Renovate's regexes (/re/, /re/i, !/re/) carry over; another value is a case-insensitive
// minimatch glob, which deps can express only when it is an exact string (a leading ! negates
// it).
func currentValue(v string) (string, bool) {
	if body := strings.TrimPrefix(v, "!"); len(body) > 1 && body[0] == '/' &&
		(strings.HasSuffix(body, "/") || strings.HasSuffix(body, "/i")) {
		return v, true
	}
	neg, body := "", v
	if rest, ok := strings.CutPrefix(v, "!"); ok {
		neg, body = "!", rest
	}
	if body == "" || strings.ContainsAny(body, `*?[]{}()\!+@`) {
		return "", false
	}
	return neg + "/^" + regexp.QuoteMeta(body) + "$/i", true
}

// depsDatasource returns the deps name of Renovate datasource ds (datasourceMap), else ds.
func depsDatasource(ds string) string {
	if n, ok := datasourceMap[ds]; ok {
		return n
	}
	return ds
}

// knownDatasource reports whether ds is a deps datasource name or custom.<name> of a
// translated custom datasource.
func (t *translator) knownDatasource(ds string) bool {
	if n, ok := strings.CutPrefix(ds, config.CustomDatasourcePrefix); ok {
		return t.cdKept[n]
	}
	return slices.Contains(config.DatasourceNames, ds)
}

type effect struct {
	src string // Renovate key
	v   any
}

// listMatchers are the matcher keys deps translates; their values are string lists.
var listMatchers = func() map[string]bool {
	m := map[string]bool{}
	for k := range strings.FieldsSeq(`matchPackageNames matchDepNames matchPackagePatterns matchDepPatterns
		matchPackagePrefixes matchDepPrefixes excludePackageNames excludePackagePatterns excludePackagePrefixes
		excludeDepNames excludeDepPatterns excludeDepPrefixes matchFileNames matchPaths matchDepTypes
		matchDatasources matchUpdateTypes matchManagers matchCategories matchBaseBranches`) {
		m[k] = true
	}
	return m
}()

// isBlock reports whether top-level key k is a manager or update-type block.
func isBlock(k string) bool {
	_, manager := managerMap[k]
	return manager || slices.ContainsFunc(updateTypeBlocks, func(b struct{ key, typ string }) bool { return b.key == k })
}

// baseBranches keeps the base branch entries deps supports (names, $default and
// /regex/); Renovate's globs are dropped with a note. Without builtin (matchBaseBranches,
// which Renovate matches against the resolved branch name, never unfolding $default)
// $default is dropped too.
func (t *translator) baseBranches(path string, l []string, builtin bool) []string {
	var out []string
	for _, b := range l {
		name := strings.TrimPrefix(b, "!")
		if !(len(name) > 1 && name[0] == '/' && strings.LastIndexByte(name, '/') > 0) && strings.ContainsAny(name, "*?[]{}") {
			t.n.warn(path, "glob %q has no deps equivalent (names and /regex/ only); dropped", b)
			continue
		}
		if !builtin && name == config.DefaultBaseBranch {
			t.n.warn(path, "%q matches no branch in Renovate either (only baseBranches unfolds it); dropped", b)
			continue
		}
		out = append(out, b)
	}
	return out
}

// rule translates one Renovate rule (a packageRules element or a block) into rules.
func (t *translator) rule(origin string, r map[string]any) {
	// A manager or update-type block only built-in presets set (nuget of
	// :ignoreModulesAndTests) is reported like their packageRules.
	t.n.builtin = builtinOrigin(origin) || (isBlock(origin) && t.builtinOnly(origin))
	defer func() { t.n.builtin = false }()
	var pkg, dep, paths, depTypes, datasources, updateTypes, managers, cats, bases []string
	var current omap // currentVersion, currentAge
	hasManagers, hasCats := false, false
	keys := slices.Sorted(mapKeys(r))
	if _, ok := r["matchCurrentVersion"]; ok {
		if _, ok := r["matchCurrentValue"]; ok {
			t.n.warn(origin, "matchCurrentVersion and matchCurrentValue together cannot be expressed (one currentVersion); rule dropped")
			return
		}
	}
	list := func(k string) ([]string, bool) {
		l, ok := strs(r[k])
		if !ok {
			t.n.warn(origin, "%s must be a list of strings; rule dropped", k)
		}
		return l, ok
	}
	for _, k := range keys {
		var l []string
		if listMatchers[k] {
			var ok bool
			if l, ok = list(k); !ok {
				return
			}
		}
		switch k {
		case "matchPackageNames":
			pkg = append(pkg, l...)
		case "matchDepNames":
			dep = append(dep, l...)
		case "matchPackagePatterns", "matchDepPatterns", "matchPackagePrefixes", "matchDepPrefixes",
			"excludePackageNames", "excludePackagePatterns", "excludePackagePrefixes",
			"excludeDepNames", "excludeDepPatterns", "excludeDepPrefixes":
			conv := func(s string) string { return s }
			switch {
			case strings.HasSuffix(k, "Patterns"):
				conv = regexEntry
			case strings.HasSuffix(k, "Prefixes"):
				conv = prefixEntry
			}
			for _, e := range l {
				e = conv(e)
				if strings.HasPrefix(k, "exclude") {
					e = "!" + e
				}
				if strings.Contains(k, "Package") {
					pkg = append(pkg, e)
				} else {
					dep = append(dep, e)
				}
			}
		case "matchFileNames", "matchPaths":
			paths = append(paths, l...)
		case "matchDepTypes":
			for _, d := range l {
				switch d {
				case "golang":
					d = "go"
				case "require":
					d = "direct"
				case "exports": // the ocb manager's exporters
					d = "exporters"
				}
				if slices.Contains(config.DepTypes, d) {
					depTypes = appendNew(depTypes, d)
				} else {
					t.n.warn(origin, "depType %s has no deps equivalent; dropped", d)
				}
			}
			if len(depTypes) == 0 {
				t.n.warn(origin, "matches no deps depType; rule dropped")
				return
			}
		case "matchDatasources":
			for _, d := range l {
				d = depsDatasource(d)
				if t.knownDatasource(d) {
					datasources = appendNew(datasources, d)
				} else {
					t.n.warn(origin, "datasource %s has no deps equivalent; dropped", d)
				}
			}
			if len(datasources) == 0 {
				t.n.warn(origin, "matches no deps datasource; rule dropped")
				return
			}
		case "matchUpdateTypes":
			for _, u := range l {
				switch u {
				case "major", "minor", "patch", "digest":
					updateTypes = appendNew(updateTypes, u)
				case "pin", "pinDigest":
					// deps has one pin type: the pin of a range (rangeStrategy pin) and the
					// digest pin of a version both carry it.
					updateTypes = appendNew(updateTypes, "pin")
				case "replacement":
					updateTypes = appendNew(updateTypes, u)
				default:
					t.n.info(origin, "update type %s has no deps equivalent; dropped", u)
				}
			}
			if len(updateTypes) == 0 {
				t.n.info(origin, "matches no deps update type; rule dropped")
				return
			}
		case "matchCurrentVersion", "matchCurrentAge":
			v, ok := r[k].(string)
			if !ok {
				t.n.warn(origin, "%s must be a string; rule dropped", k)
				return
			}
			current.set(map[string]string{"matchCurrentVersion": "currentVersion", "matchCurrentAge": "currentAge"}[k], v)
		case "matchCurrentValue":
			v, ok := r[k].(string)
			if !ok {
				t.n.warn(origin, "%s must be a string; rule dropped", k)
				return
			}
			cv, ok := currentValue(v)
			if !ok {
				t.n.warn(origin, "matchCurrentValue %q: globs have no deps equivalent; rule dropped", v)
				return
			}
			current.set("currentVersion", cv)
		case "matchManagers":
			hasManagers, managers = true, l
		case "matchCategories":
			hasCats, cats = true, l
		case "matchBaseBranches":
			if bases = t.baseBranches(origin+".matchBaseBranches", l, false); len(bases) == 0 {
				t.n.warn(origin, "matches no base branch deps supports; rule dropped")
				return
			}
		case "stabilityDays":
		default:
			if strings.HasPrefix(k, "match") || strings.HasPrefix(k, "exclude") {
				t.n.warn(origin, "%s has no deps equivalent; rule dropped", k)
				return
			}
		}
	}
	if len(pkg) > 0 && len(dep) > 0 {
		t.n.warn(origin, "matchPackageNames and matchDepNames together cannot be expressed; rule dropped")
		return
	}
	names := append(pkg, dep...) //nolint:gocritic // one of them is empty
	for i, n := range names {
		if n == "*" {
			names[i] = "**"
		}
	}
	for _, m := range []struct {
		key  string
		list []string
	}{{"names", names}, {"paths", paths}} {
		for _, e := range m.list {
			if _, err := pattern.Compile([]string{e}); err != nil {
				t.n.warn(origin, "%s: %v; rule dropped", m.key, err)
				return
			}
		}
	}

	eff := t.effects(origin, r)
	if len(updateTypes) > 0 {
		for _, k := range config.PrePolicyKeys {
			if e, ok := eff[k]; ok {
				t.n.warn(origin+"."+e.src, "deps decides %s before the update type is known; dropped", k)
				delete(eff, k)
			}
		}
	}
	// deps compares currentVersion and currentAge under the versioning other rules decide.
	for _, c := range current {
		ks := []string{"versioning"}
		if c.k == "currentAge" {
			ks = append(ks, "extractVersion", "registryUrls")
		}
		for _, k := range ks {
			if e, ok := eff[k]; ok {
				t.n.warn(origin+"."+e.src, "deps cannot combine %s with %s; dropped", k, c.k)
				delete(eff, k)
			}
		}
	}
	if e, ok := eff["registryUrls"]; ok {
		switch {
		case len(datasources) > 0 && !slices.ContainsFunc(datasources, func(ds string) bool { return slices.Contains(config.RegistryURLDatasources, ds) }):
			t.n.warn(origin+"."+e.src, "deps takes registryUrls for %s deps only; dropped", strings.Join(config.RegistryURLDatasources, ", "))
			delete(eff, "registryUrls")
		case len(datasources) == 0:
			t.n.info(origin+"."+e.src, "deps applies registryUrls to %s deps only", strings.Join(config.RegistryURLDatasources, ", "))
		}
	}
	if len(eff) == 0 {
		t.n.info(origin, "no deps effect left; rule dropped")
		return
	}
	build := func(managers, depTypes []string) omap {
		var b omap
		if len(managers) > 0 {
			b.set("managers", managers)
		}
		for _, m := range []struct {
			k string
			l []string
		}{{"names", names}, {"depTypes", depTypes}, {"paths", paths}, {"datasources", datasources}, {"baseBranches", bases}} {
			if len(m.l) > 0 {
				b.set(m.k, m.l)
			}
		}
		for _, k := range []string{"currentVersion", "currentAge"} {
			for _, f := range current {
				if f.k == k {
					b.set(k, f.v)
				}
			}
		}
		if len(updateTypes) > 0 {
			b.set("updateTypes", updateTypes)
		}
		for _, k := range effectOrder {
			if e, ok := eff[k]; ok {
				b.set(k, e.v)
			}
		}
		return b
	}
	if !hasManagers && !hasCats {
		t.addRule(origin, build(nil, depTypes))
		return
	}
	rms := t.renovateManagers(origin, managers, cats, hasManagers, hasCats)
	type group struct {
		manager string
		all     bool
		types   []string
	}
	var groups []*group
	for _, rm := range rms {
		for _, x := range managerMap[rm] {
			i := slices.IndexFunc(groups, func(g *group) bool { return g.manager == x.manager })
			if i < 0 {
				groups = append(groups, &group{manager: x.manager})
				i = len(groups) - 1
			}
			g := groups[i]
			if x.depTypes == nil {
				g.all = true
			}
			for _, d := range x.depTypes {
				g.types = appendNew(g.types, d)
			}
		}
	}
	// One rule per set of dep types: the managers matched with all of theirs (or the rule's)
	// share one.
	type rule struct{ managers, depTypes []string }
	var out []*rule
	for _, g := range groups {
		dt := depTypes
		switch {
		case g.all:
		case dt == nil:
			dt = g.types
		default:
			dt = slices.DeleteFunc(slices.Clone(dt), func(d string) bool { return !slices.Contains(g.types, d) })
		}
		if dt != nil {
			// Only the dep types the deps manager produces (Renovate's poetry has extras, deps' not).
			dt = slices.DeleteFunc(slices.Clone(dt), func(d string) bool { return !slices.Contains(managerDepTypes[g.manager], d) })
			if len(dt) == 0 {
				continue
			}
		}
		if i := slices.IndexFunc(out, func(r *rule) bool { return slices.Equal(r.depTypes, dt) }); i >= 0 {
			out[i].managers = append(out[i].managers, g.manager)
			continue
		}
		out = append(out, &rule{managers: []string{g.manager}, depTypes: dt})
	}
	if len(out) == 0 {
		t.n.warn(origin, "matches no deps manager; rule dropped")
	}
	for _, r := range out {
		t.addRule(origin, build(r.managers, r.depTypes))
	}
}

// renovateManagers returns the mapped Renovate managers a rule matches: matchManagers,
// matchCategories, or their intersection.
func (t *translator) renovateManagers(origin string, managers, cats []string, hasManagers, hasCats bool) []string {
	var byName, byCat []string
	for _, m := range managers {
		if _, ok := managerMap[m]; ok {
			byName = appendNew(byName, m)
		} else {
			t.n.warn(origin, "manager %s has no deps equivalent; dropped", m)
		}
	}
	for _, c := range cats {
		ms, ok := categories[c]
		if !ok {
			t.n.warn(origin, "category %s has no deps equivalent; dropped", c)
		}
		for _, m := range ms {
			byCat = appendNew(byCat, m)
		}
	}
	switch {
	case !hasCats:
		return byName
	case !hasManagers:
		return byCat
	}
	return slices.DeleteFunc(byName, func(m string) bool { return !slices.Contains(byCat, m) })
}

// effects translates the effect keys of a rule; they are validated together, and one that
// deps rejects on its own is dropped with a note.
func (t *translator) effects(origin string, r map[string]any) map[string]effect {
	eff := map[string]effect{}
	for _, k := range slices.Sorted(mapKeys(r)) {
		v := r[k]
		p := origin + "." + k
		set := func(key string, v any) { eff[key] = effect{k, v} }
		switch k {
		case "enabled":
			if b, ok := v.(bool); ok {
				set("ignore", !b)
			} else {
				t.n.warn(p, "must be a boolean; ignored")
			}
		case "registryUrls":
			if l, ok := strs(v); ok && len(l) > 0 {
				set(k, l)
			} else {
				t.n.warn(p, "must be a non-empty list of strings; ignored")
			}
		case "labels", "addLabels", "reviewers", "assignees":
			if l, ok := strs(v); ok {
				set(k, l)
			} else {
				t.n.warn(p, "must be a list of strings; ignored")
			}
		case "prHeader", "prFooter":
			if s, ok := t.prText(p, v); ok {
				set(k, s)
			}
		case "schedule":
			if s, ok := t.schedule(p, v); ok {
				set("schedule", s)
			}
		case "additionalBranchPrefix":
			if s, ok := t.additionalBranchPrefix(p, v); ok {
				set(k, s)
			}
		case "minimumReleaseAge":
			if v == nil {
				v = "0"
			}
			set("minimumReleaseAge", v)
		case "stabilityDays":
			if _, ok := r["minimumReleaseAge"]; !ok {
				if f, ok := v.(float64); ok {
					set("minimumReleaseAge", strconv.FormatFloat(f, 'f', -1, 64)+" days")
				}
			}
		case "separateMajorMinor", "separateMultipleMajor", "separateMinorPatch":
			if _, ok := v.(bool); !ok {
				t.n.warn(p, "must be a boolean; ignored")
			}
		case "automergeType":
			if s, ok := t.automergeType(p, v); ok {
				set(k, s)
			}
		case "automergeStrategy":
			if s, ok := t.automergeStrategy(p, v); ok {
				set(k, s)
			}
		case "pinDigests":
			if b, ok := v.(bool); ok {
				set("pin", map[bool]string{true: config.PinDigest, false: config.PinKeep}[b])
			}
		case "rangeStrategy":
			if s, ok := t.rangeStrategy(p, v); ok {
				set(k, s)
			}
		case "prCreation":
			if s, ok := t.prCreation(p, v); ok {
				set(k, s)
			}
		case "abandonmentThreshold":
			if v == nil {
				v = "0" // null turns the check off
			}
			set(k, v)
		case "postUpgradeTasks":
			if tasks, ok := t.postUpgradeTasks(p, v); ok {
				set(k, tasks)
			}
		case "replacementNameTemplate":
			switch from, to, ok := prefixTemplate(v); {
			case r["replacementName"] != nil:
				t.n.info(p, "replacementName takes precedence; ignored")
			case ok:
				set("replacementPrefix", from)
				set("replacementName", to)
			default:
				t.n.warn(p, "only the prefix form {{{replace '<prefix>' '<new prefix>' packageName}}} is supported; dropped")
			}
		case "replacementVersionTemplate":
			t.n.warn(p, "not supported; dropped")
		case "semanticCommitScope":
			if v == nil {
				v = ""
			}
			set("commitScope", v)
		default:
			if to, ok := ruleCopies[k]; ok {
				set(to, v)
				continue
			}
			if titleKeys[k] && !builtinOrigin(origin) {
				continue // composed below
			}
			if !strings.HasPrefix(k, "match") && !strings.HasPrefix(k, "exclude") && k != "matchManagers" {
				t.untranslated(p, k, v)
			}
		}
	}
	// The bucket options a rule leaves out keep their top-level values (Renovate's earlier
	// rules are not taken into account).
	sep, src := t.sep, ""
	for i, k := range separateKeys {
		if b, ok := r[k].(bool); ok {
			sep[i], src = b, cmp.Or(src, k)
		}
	}
	if src != "" {
		eff["separate"] = effect{src, separate(sep)}
	}
	if !builtinOrigin(origin) {
		own := map[string]any{}
		for k, v := range r {
			if titleKeys[k] {
				own[k] = v
			}
		}
		if s, ok := t.title(origin, own, r["semanticCommitType"]); ok {
			eff["prTitle"] = effect{slices.Sorted(mapKeys(own))[0], s}
		}
	}
	var repl omap
	for _, key := range []string{"replacementName", "replacementVersion", "replacementPrefix"} {
		if e, ok := eff[key]; ok {
			repl.set(key, e.v)
		}
	}
	// A rule body deps accepts as a whole accepts every effect in it, so the common case
	// needs one validation; only a body with a problem is taken apart to locate it.
	if len(eff) > 1 {
		whole := make(omap, 0, len(eff))
		for _, key := range slices.Sorted(maps.Keys(eff)) {
			whole.set(key, eff[key].v)
		}
		if check(t.ruleContext(omap{{"rules", []item{{body: whole}}}})) == "" {
			return eff
		}
	}
	if len(repl) > 0 {
		if msg := check(t.ruleContext(omap{{"rules", []item{{body: repl}}}})); msg != "" {
			for _, f := range repl {
				t.n.warn(origin+"."+eff[f.k].src, "dropped: %s", msg)
				delete(eff, f.k)
			}
		}
	}
	for key, e := range eff {
		if strings.HasPrefix(key, "replacement") {
			continue // validated together above
		}
		if msg := check(t.ruleContext(omap{{"rules", []item{{body: omap{{key, e.v}}}}}})); msg != "" {
			t.n.warn(origin+"."+e.src, "dropped: %s", msg)
			delete(eff, key)
		}
	}
	return eff
}

// addRule validates a translated rule and adds it, or drops it with a note. The rule is
// validated against the custom datasources, whose rendering (cdsYAML) every rule shares.
func (t *translator) addRule(origin string, body omap) {
	rule := mustMarshal(t.ruleContext(omap{{"rules", []item{{body: body}}}}), "")
	cds := t.cdsYAML()
	src := make([]byte, 0, len(cds)+len(rule))
	if msg := checkYAML(append(append(src, cds...), rule...)); msg != "" {
		t.n.warn(origin, "dropped: %s", msg)
		return
	}
	t.rules = append(t.rules, item{origin: origin, body: body})
}

// cdsYAML returns the translated customDatasources as the YAML every rule check carries,
// rendered once per change: the rules of a config are validated against the same ones.
func (t *translator) cdsYAML() []byte {
	if t.cdsSrc == nil {
		t.cdsSrc = mustMarshal(omap{{"customDatasources", t.cds}}, "")
	}
	return t.cdsSrc
}

func appendNew(l []string, s string) []string {
	if slices.Contains(l, s) {
		return l
	}
	return append(l, s)
}

// get returns the value of key k of m.
func (m omap) get(k string) (any, bool) {
	for _, f := range m {
		if f.k == k {
			return f.v, true
		}
	}
	return nil, false
}

// groupOf returns the group a translated rule sets, "" for none.
func groupOf(it item) string {
	v, _ := it.body.get("group")
	s, _ := v.(string)
	return s
}

// hoistGroupSettings moves the settings the rules of a group share into groups.<group>: a key
// (config.GroupKeys) every rule setting the group, at least two, sets to one value. groups
// entries apply after every rule (config.Policy), so a key moves only when that changes no
// policy (hoistable); groups of built-in names are left alone, as built-in group rules give
// them deps no rule names. It returns the groups mapping, in the order the groups first
// appear, nil when nothing moved; the moved keys are removed from rules.
func hoistGroupSettings(rules []item) omap {
	var names []string
	count := map[string]int{}
	for _, it := range rules {
		g := groupOf(it)
		if g == "" {
			continue
		}
		if count[g] == 0 {
			names = append(names, g)
		}
		count[g]++
	}
	moved := map[string]omap{}
	for _, k := range effectOrder {
		if !slices.Contains(config.GroupKeys, k) {
			continue
		}
		shared := map[string]any{}
		for _, g := range names {
			if count[g] < 2 || config.BuiltinGroup(g) {
				continue
			}
			if v, ok := sharedValue(rules, g, k); ok {
				shared[g] = v
			}
		}
		// Dropping a group can make another one unhoistable; the greatest set that holds is
		// the same in any order.
		for changed := true; changed; {
			changed = false
			for g, v := range shared {
				if !hoistable(rules, g, k, v, shared) {
					delete(shared, g)
					changed = true
				}
			}
		}
		for g, v := range shared {
			moved[g] = append(moved[g], field{k, v})
		}
	}
	var out omap
	for _, g := range names {
		if len(moved[g]) == 0 {
			continue
		}
		out.set(g, moved[g])
		for i, it := range rules {
			if groupOf(it) == g {
				rules[i].body = slices.DeleteFunc(slices.Clone(it.body), func(f field) bool {
					_, ok := moved[g].get(f.k)
					return ok
				})
			}
		}
	}
	return out
}

// sharedValue returns the value every rule setting group g sets key k to; false when one
// does not set it, two differ, or (for labels) one adds labels after setting them.
func sharedValue(rules []item, g, k string) (any, bool) {
	var shared any
	found := false
	for _, it := range rules {
		if groupOf(it) != g {
			continue
		}
		v, ok := it.body.get(k)
		if _, adds := it.body.get("addLabels"); !ok || (k == "labels" && adds) || (found && !reflect.DeepEqual(v, shared)) {
			return nil, false
		}
		shared, found = v, true
	}
	return shared, found
}

// hoistable reports whether moving key k (value v) from the rules of group g into groups.<g>
// keeps every policy, given that the groups in shared move it too. A dep whose policy has
// group g got it from a rule of g, which set v: it keeps v unless a later rule matching it
// sets k to another value (or adds labels), which groups.<g> would now override. A dep a rule
// of g matched keeps its value unless a later rule moves it to a group that does not move k
// and does not set it itself, as groups.<g> no longer applies to it. Rules that cannot match
// the same dep as an earlier rule of g (disjoint) do neither.
func hoistable(rules []item, g, k string, v any, shared map[string]any) bool {
	for j, it := range rules {
		w, sets := it.body.get(k)
		var conflict bool
		switch h := groupOf(it); {
		case h == g:
			continue
		case h != "":
			_, moves := shared[h]
			conflict = !moves && !sets
		default:
			_, adds := it.body.get("addLabels")
			conflict = (sets && !reflect.DeepEqual(w, v)) || (adds && k == "labels")
		}
		if conflict && slices.ContainsFunc(rules[:j], func(e item) bool { return groupOf(e) == g && !disjoint(e.body, it.body) }) {
			return false
		}
	}
	return true
}

// disjoint reports whether no update can match both translated rules a and b: a match field
// both set without a common value (managers, depTypes, datasources, updateTypes), or names no
// name matches in both (namesDisjoint). False when it cannot tell.
func disjoint(a, b omap) bool {
	for _, k := range []string{"managers", "depTypes", "datasources", "updateTypes"} {
		x, okA := strList(a, k)
		y, okB := strList(b, k)
		if k == "datasources" {
			x, y = canonicalDatasources(x), canonicalDatasources(y)
		}
		if okA && okB && !slices.ContainsFunc(x, func(s string) bool { return slices.Contains(y, s) }) {
			return true
		}
	}
	x, okA := strList(a, "names")
	y, okB := strList(b, "names")
	return okA && okB && namesDisjoint(x, y)
}

// strList returns the string list of key k of m.
func strList(m omap, k string) ([]string, bool) {
	v, ok := m.get(k)
	if !ok {
		return nil, false
	}
	if l, ok := v.([]string); ok {
		return l, true
	}
	return strs(v)
}

func canonicalDatasources(l []string) []string {
	out := make([]string, len(l))
	for i, ds := range l {
		out[i] = config.CanonicalDatasource(ds)
	}
	return out
}

// namesDisjoint reports whether no name matches both names lists (rules[].names): each list
// has entries other than exclusions, which only narrow a list, and every such entry of one
// is disjoint from every one of the other. Two literal names are when they differ (names
// compare case-insensitively), a literal name and a glob when the glob does not match it, and
// two globs when neither literal prefix (up to the first metacharacter) starts the other.
// Regexes are never known to be disjoint.
func namesDisjoint(x, y []string) bool {
	positive := func(l []string) []string {
		return slices.DeleteFunc(slices.Clone(l), func(e string) bool { return strings.HasPrefix(e, "!") })
	}
	px, py := positive(x), positive(y)
	if len(px) == 0 || len(py) == 0 {
		return false
	}
	for _, a := range px {
		for _, b := range py {
			if !globsDisjoint(a, b) {
				return false
			}
		}
	}
	return true
}

// globsDisjoint reports whether no name matches both names entries a and b (namesDisjoint).
func globsDisjoint(a, b string) bool {
	if _, _, re := pattern.Regex(a); re {
		return false
	}
	if _, _, re := pattern.Regex(b); re {
		return false
	}
	const meta = `*?[]{}\`
	litA, litB := !strings.ContainsAny(a, meta), !strings.ContainsAny(b, meta)
	switch {
	case litA && litB:
		return !strings.EqualFold(a, b)
	case litA || litB:
		lit, glob := a, b
		if litB {
			lit, glob = b, a
		}
		l, err := pattern.Compile([]string{glob})
		return err == nil && !l.Match(lit)
	}
	pa, pb := strings.ToLower(a[:strings.IndexAny(a, meta)]), strings.ToLower(b[:strings.IndexAny(b, meta)])
	return !strings.HasPrefix(pa, pb) && !strings.HasPrefix(pb, pa)
}

// exclusions computes the rules for managers Renovate does not run (enabledManagers,
// enabled: false blocks, pre-commit, azure-pipelines and nix off by default, deps managers
// without a Renovate counterpart) into t.exclusionRules and returns the fully excluded deps
// managers.
func (t *translator) exclusions(cfg map[string]any, done map[string]bool) map[string]bool {
	var enabledSet map[string]bool
	if v, ok := cfg["enabledManagers"]; ok {
		done["enabledManagers"] = true
		l, _ := strs(v)
		enabledSet = map[string]bool{}
		for _, m := range l {
			if _, ok := managerMap[m]; ok {
				enabledSet[m] = true
			} else {
				t.n.warn("enabledManagers", "manager %s has no deps equivalent; ignored", m)
			}
		}
	}
	// reason returns why Renovate does not run rm, or "".
	reason := func(rm string) string {
		block, _ := cfg[rm].(map[string]any)
		be, hasBE := block["enabled"].(bool)
		switch {
		case hasBE && !be:
			return rm + ".enabled: false"
		case enabledSet != nil && !enabledSet[rm]:
			return "enabledManagers"
		case (rm == "pre-commit" || rm == "azure-pipelines" || rm == "nix") && !(hasBE && be) && !enabledSet[rm]:
			return rm + " is disabled by default in Renovate"
		}
		return ""
	}
	type agg struct {
		enabled, disabled       []string
		enabledAll, disabledAll bool
		anyEnabled, anyDisabled bool
		why                     string
	}
	aggs := map[string]*agg{}
	for _, rm := range slices.Sorted(func(yield func(string) bool) {
		for k := range managerMap {
			if !yield(k) {
				return
			}
		}
	}) {
		why := reason(rm)
		for _, x := range managerMap[rm] {
			a := aggs[x.manager]
			if a == nil {
				a = &agg{}
				aggs[x.manager] = a
			}
			if why == "" {
				a.anyEnabled = true
				a.enabledAll = a.enabledAll || x.depTypes == nil
				for _, d := range x.depTypes {
					a.enabled = appendNew(a.enabled, d)
				}
				continue
			}
			a.anyDisabled = true
			if a.why == "" {
				a.why = why
			}
			a.disabledAll = a.disabledAll || x.depTypes == nil
			for _, d := range x.depTypes {
				a.disabled = appendNew(a.disabled, d)
			}
		}
	}
	full := map[string]bool{}
	for _, m := range config.Managers {
		a := aggs[m]
		if a == nil {
			// Renovate has no such manager, so it never updates these files.
			full[m] = true
			t.exclusionRules = append(t.exclusionRules, item{origin: "Renovate has no " + m + " manager", body: omap{{"managers", []string{m}}, {"ignore", true}}})
			continue
		}
		if !a.anyDisabled || a.enabledAll {
			continue
		}
		universe := depTypeUniverse[m]
		var excl []string
		switch {
		case enabledSet != nil || a.disabledAll:
			excl = universe
		default:
			excl = a.disabled
		}
		excl = slices.DeleteFunc(slices.Clone(excl), func(d string) bool { return slices.Contains(a.enabled, d) })
		if !a.anyEnabled && (universe == nil || len(excl) == len(universe)) {
			full[m] = true
			if m != "custom" {
				t.exclusionRules = append(t.exclusionRules, item{origin: a.why, body: omap{{"managers", []string{m}}, {"ignore", true}}})
			}
			continue
		}
		if len(excl) > 0 {
			t.exclusionRules = append(t.exclusionRules, item{origin: a.why, body: omap{{"managers", []string{m}}, {"depTypes", excl}, {"ignore", true}}})
		}
	}
	return full
}

// prefixTemplateRe is the replacementNameTemplate form of Renovate's registry-move presets.
var prefixTemplateRe = regexp.MustCompile(`^\{{3}replace '([^']+)' '([^']*)' packageName\}{3}$`)

// prefixTemplate returns the prefix a replacementNameTemplate of the form
// {{{replace '<prefix regex>' '<new prefix>' packageName}}} moves and its new prefix; the
// regex must be a literal (escaped metacharacters allowed), optionally anchored with ^.
func prefixTemplate(v any) (from, to string, ok bool) {
	s, _ := v.(string)
	m := prefixTemplateRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	re := strings.TrimPrefix(m[1], "^")
	var lit strings.Builder
	for i := 0; i < len(re); i++ {
		c := re[i]
		switch {
		case c == '\\' && i+1 < len(re) && strings.IndexByte(`\\.+*?()|[]{}^$/-`, re[i+1]) >= 0:
			i++
			lit.WriteByte(re[i])
		case strings.IndexByte(`\\.+*?()|[]{}^$`, c) >= 0:
			return "", "", false
		default:
			lit.WriteByte(c)
		}
	}
	if lit.Len() == 0 || m[2] == "" {
		return "", "", false
	}
	return lit.String(), m[2], true
}

// postUpgradeTasks translates Renovate's {commands, fileFilters, executionMode}. Renovate
// templates commands (`{{{depName}}}`); deps passes the update as DEPS_* variables instead,
// so templated commands are dropped.
func (t *translator) postUpgradeTasks(path string, v any) (omap, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		t.n.warn(path, "must be an object; ignored")
		return nil, false
	}
	cmds, ok := strs(m["commands"])
	if !ok {
		t.n.warn(path, "commands must be a list of strings; dropped")
		return nil, false
	}
	for _, c := range cmds {
		if strings.Contains(c, "{{") {
			t.n.warn(path, "command %q is a template; deps passes the update as DEPS_* environment variables (deps help environment); dropped", c)
			return nil, false
		}
	}
	out := omap{{"commands", cmds}}
	for _, k := range slices.Sorted(mapKeys(m)) {
		switch k {
		case "commands":
		case "executionMode":
			out.set("executionMode", m[k])
		case "fileFilters":
			files, ok := strs(m[k])
			if !ok {
				t.n.warn(path+".fileFilters", "must be a list of strings; ignored")
				continue
			}
			out.set("files", files)
		case "dataFileTemplate", "installTools", "workingDirTemplate":
			t.n.warn(path+"."+k, "no deps equivalent; ignored")
		default:
			t.untranslated(path+"."+k, k, m[k])
		}
	}
	t.n.info(path, "deps runs postUpgradeTasks only with --allow-tasks (or DEPS_ALLOW_TASKS) listing the commands, like Renovate's allowedCommands")
	return out, true
}
