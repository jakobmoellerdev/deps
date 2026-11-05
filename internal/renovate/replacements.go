package renovate

import (
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// replacementPresets are the replacements:* presets of the resolved chain.
type replacementPresets struct {
	all     string   // the extends entry of replacements:all, "" when absent
	entries []string // extends entries of the other replacements:<name> presets, in order
	names   []string // their names
}

func (p *replacementPresets) add(entry, name string) {
	if name == "all" {
		if p.all == "" {
			p.all = entry
		}
		return
	}
	if !slices.Contains(p.names, name) {
		p.entries, p.names = append(p.entries, entry), append(p.names, name)
	}
}

// replacements translates the replacements:* presets: it returns the top-level replacements
// switch and adds the rules it needs before every other rule (Renovate merges presets first).
//
// deps' built-in list (config.Replacements) is Renovate's lib/data/replacements.json as far
// as deps can write it, behind one switch. Renovate only replaces through presets and rules:
//   - replacements:all (in config:recommended): replacements: true, plus rules switching
//     off built-in entries of presets replacements:all does not extend;
//   - only single presets: replacements: false, plus rules with replacementName for the
//     built-in entries of those presets;
//   - no replacements preset: replacements: false.
func (t *translator) replacements(p replacementPresets) bool {
	skipped := func(name string) []string {
		var out []string
		for _, s := range config.ReplacementsSkipped {
			if n, _, _ := strings.Cut(s, "["); n == name {
				out = append(out, s)
			}
		}
		return out
	}
	known := func(name string) bool {
		return slices.ContainsFunc(config.Replacements, func(r config.Replacement) bool { return r.Name == name })
	}
	if p.all != "" {
		t.n.info(p.all, "deps' built-in replacements cover it (replacements: true)")
		for _, name := range config.ReplacementsAll {
			if s := skipped(name); len(s) > 0 {
				t.n.info(p.all, "deps cannot apply %s", strings.Join(s, "; "))
			}
		}
	}
	for i, name := range p.names {
		e := p.entries[i]
		switch s := skipped(name); {
		case known(name) && p.all != "":
			t.n.info(e, "deps' built-in replacements cover it (replacements: true)")
		case known(name):
			t.n.info(e, "translated into rules from deps' built-in replacements")
			if len(s) > 0 {
				t.n.warn(e, "deps cannot apply %s", strings.Join(s, "; "))
			}
			t.replacementRules(e, name, false)
		case len(s) > 0:
			t.n.warn(e, "deps cannot apply %s; skipped", strings.Join(s, "; "))
		default:
			t.n.warn(e, "not in deps' built-in replacements; skipped")
		}
	}
	if p.all == "" {
		return false
	}
	done := map[string]bool{}
	for _, r := range config.Replacements {
		if done[r.Name] || slices.Contains(config.ReplacementsAll, r.Name) || slices.Contains(p.names, r.Name) {
			continue
		}
		done[r.Name] = true
		t.n.info(p.all, "replacements:all does not extend %s from deps' built-in list; switched off by a rule", r.Name)
		t.replacementRules("replacements:"+r.Name+" (not in replacements:all)", r.Name, true)
	}
	return true
}

// replacementRules adds one rule for the managers of each built-in entry of preset name: with
// off, it switches the replacement off, else it applies it.
func (t *translator) replacementRules(origin, name string, off bool) {
	for _, r := range config.Replacements {
		if r.Name != name {
			continue
		}
		names := slices.Clone(r.Names)
		for _, p := range r.Patterns {
			names = append(names, "/"+p+"/")
		}
		b := omap{{"managers", slices.Clone(r.Managers)}, {"names", names}}
		if r.CurrentVersion != "" {
			b.set("currentVersion", r.CurrentVersion)
		}
		if off {
			b.set("updateTypes", []string{string(dep.Replacement)})
			b.set("ignore", true)
		} else {
			b.set("replacementName", r.To)
			if r.Version != "" {
				b.set("replacementVersion", r.Version)
			}
			if r.Prefix != "" {
				b.set("replacementPrefix", r.Prefix)
			}
		}
		t.addRule(origin, b)
	}
}
