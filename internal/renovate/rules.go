package renovate

import (
	"fmt"
	"maps"
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
	{"major", "major"}, {"minor", "minor"}, {"patch", "patch"}, {"digest", "digest"}, {"pinDigest", "pin"},
	{"replacement", "replacement"},
}

// effectOrder is the output order of rule effects (the field order of config.Rule).
var effectOrder = []string{"ignore", "pin", "allow", "rangeStrategy", "versioning", "extractVersion", "replacementName",
	"replacementVersion", "replacementPrefix", "separateMajorMinor",
	"separateMultipleMajor", "separateMinorPatch", "group", "commitType", "commitScope", "schedule",
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
	for _, k := range []string{"pin", "rollback"} {
		if _, ok := cfg[k]; ok {
			done[k] = true
			t.n.info(k, "no deps equivalent; ignored")
		}
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
			t.rule("ignoreDeps", map[string]any{"matchDepNames": toAny(l), "enabled": false})
		}
	}
}

func toAny(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// regexEntry converts a deprecated *Patterns entry into the /re/ form.
func regexEntry(p string) string {
	if p == "*" || p == ".*" {
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
		matchDatasources matchUpdateTypes matchManagers matchCategories`) {
		m[k] = true
	}
	return m
}()

// isBlock reports whether top-level key k is a manager or update-type block.
func isBlock(k string) bool {
	_, manager := managerMap[k]
	return manager || slices.ContainsFunc(updateTypeBlocks, func(b struct{ key, typ string }) bool { return b.key == k })
}

// rule translates one Renovate rule (a packageRules element or a block) into rules.
func (t *translator) rule(origin string, r map[string]any) {
	// A manager or update-type block only built-in presets set (nuget of
	// :ignoreModulesAndTests) is reported like their packageRules.
	t.n.builtin = builtinOrigin(origin) || (isBlock(origin) && t.builtinOnly(origin))
	defer func() { t.n.builtin = false }()
	var pkg, dep, paths, depTypes, datasources, updateTypes, managers, cats []string
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
				case "pinDigest":
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
		for _, k := range []string{"pin", "rangeStrategy", "versioning", "extractVersion", "replacementName", "replacementVersion", "replacementPrefix"} {
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
			ks = append(ks, "extractVersion")
		}
		for _, k := range ks {
			if e, ok := eff[k]; ok {
				t.n.warn(origin+"."+e.src, "deps cannot combine %s with %s; dropped", k, c.k)
				delete(eff, k)
			}
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
		}{{"names", names}, {"depTypes", depTypes}, {"paths", paths}, {"datasources", datasources}} {
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

// effects translates the effect keys of a rule; each is validated alone and dropped with
// a note when deps rejects it.
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
		case "allowedVersions":
			set("allow", v)
		case "versioning", "extractVersion", "minimumReleaseAgeBehaviour":
			set(k, v)
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
		case "groupName":
			set("group", v)
		case "schedule":
			if s, ok := t.schedule(p, v); ok {
				set("schedule", s)
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
		case "separateMajorMinor", "separateMultipleMajor", "separateMinorPatch", "automerge":
			set(k, v)
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
		case "replacementName", "replacementVersion":
			set(k, v)
		case "dependencyDashboardApproval":
			set("dashboardApproval", v)
		case "draftPR":
			set(k, v)
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
		case "semanticCommitType":
			set("commitType", v)
		case "semanticCommitScope":
			if v == nil {
				v = ""
			}
			set("commitScope", v)
		default:
			if titleKeys[k] && !builtinOrigin(origin) {
				continue // composed below
			}
			if !strings.HasPrefix(k, "match") && !strings.HasPrefix(k, "exclude") && k != "matchManagers" {
				t.untranslated(p, k)
			}
		}
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

// addRule validates a translated rule and adds it, or drops it with a note.
func (t *translator) addRule(origin string, body omap) {
	if msg := check(t.ruleContext(omap{{"customDatasources", t.cds}, {"rules", []item{{body: body}}}})); msg != "" {
		t.n.warn(origin, "dropped: %s", msg)
		return
	}
	t.rules = append(t.rules, item{origin: origin, body: body})
}

func appendNew(l []string, s string) []string {
	if slices.Contains(l, s) {
		return l
	}
	return append(l, s)
}

// exclusions computes the rules for managers Renovate does not run (enabledManagers,
// enabled: false blocks, pre-commit and azure-pipelines off by default, deps managers
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
		case (rm == "pre-commit" || rm == "azure-pipelines") && !(hasBE && be) && !enabledSet[rm]:
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
			t.untranslated(path+"."+k, k)
		}
	}
	t.n.info(path, "deps runs postUpgradeTasks only with --allow-tasks (or DEPS_ALLOW_TASKS) listing the commands, like Renovate's allowedCommands")
	return out, true
}
