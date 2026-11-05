package config

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Warning is a finding of Lint: a valid configuration that does not do what it says. Field
// is the key path ("rules[2].depTypes[0]"), Msg the explanation.
type Warning struct{ Field, Msg string }

// RuleMatchFields are the keys of the rule fields that select deps and updates; every other
// rule key is an apply field (RuleApplyFields).
var RuleMatchFields = []string{"managers", "names", "depTypes", "paths", "datasources", "baseBranches", "currentVersion", "currentAge", "updateTypes"}

// RuleApplyFields are the keys of the rule fields that override the policy, in declaration
// order.
var RuleApplyFields = ruleApplyFields()

func ruleApplyFields() []string {
	var out []string
	t := reflect.TypeFor[Rule]()
	for f := range t.Fields() {
		if k := yamlKey(f); k != "" && !slices.Contains(RuleMatchFields, k) {
			out = append(out, k)
		}
	}
	return out
}

// yamlKey is the yaml key of a struct field (its tag name, else the lowercased field name),
// "" for unexported or skipped fields.
func yamlKey(f reflect.StructField) string {
	k, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	if !f.IsExported() || k == "-" {
		return ""
	}
	return cmp.Or(k, strings.ToLower(f.Name))
}

// Lint reports the parts of a validated configuration that cannot have an effect, without
// looking at the repository: rules that set no apply field, and match fields no dep can
// satisfy together (a dep type no listed manager produces, a datasource no listed manager or
// dep type uses). Rules that match no dep of the repository are a run-time finding
// (UnusedRules).
func (c *Config) Lint() []Warning {
	ws := make([]Warning, 0, len(c.Rules))
	for i := range c.Rules {
		ws = append(ws, c.Rules[i].lint(fmt.Sprintf("rules[%d]", i))...)
	}
	return ws
}

// hasSet reports whether a field of r among keys is set: a non-nil list (also []), a non-nil
// pointer or a non-empty string.
func (r *Rule) hasSet(keys []string) bool {
	for f, fv := range reflect.ValueOf(r).Elem().Fields() {
		if k := yamlKey(f); k != "" && slices.Contains(keys, k) && !fv.IsZero() {
			return true
		}
	}
	return false
}

func (r *Rule) lint(f string) []Warning {
	noApply := !r.hasSet(RuleApplyFields)
	if noApply && !r.hasSet(RuleMatchFields) {
		return []Warning{{f, "is empty: no match field and no apply field"}}
	}
	ws := r.lintMatch(f)
	if noApply {
		ws = append([]Warning{{f, "sets only match fields and no apply field (ignore, allowedUpdateTypes, group, …), so it changes nothing"}}, ws...)
	}
	return ws
}

// lintMatch reports match fields of r no dep can satisfy.
func (r *Rule) lintMatch(f string) []Warning {
	ms := slices.DeleteFunc(slices.Clone(r.Managers), func(m string) bool { return managerByName(m) == nil })
	by := "by " + strings.Join(ms, " or ")
	if len(ms) == 0 {
		ms, by = Managers, "by any manager"
	}
	var ws []Warning
	for j, dt := range r.DepTypes {
		if !slices.ContainsFunc(ms, func(m string) bool { return uses(managerByName(m), dt, "") }) {
			ws = append(ws, Warning{fmt.Sprintf("%s.depTypes[%d]", f, j), fmt.Sprintf("dep type %q is not produced %s, so it matches nothing", dt, by)})
		}
	}
	for j, ds := range r.Datasources {
		if !slices.ContainsFunc(ms, func(m string) bool { return uses(managerByName(m), "", CanonicalDatasource(ds)) }) {
			ws = append(ws, Warning{fmt.Sprintf("%s.datasources[%d]", f, j), fmt.Sprintf("datasource %q is not used %s, so it matches nothing", ds, by)})
		}
	}
	if len(ws) > 0 || len(r.DepTypes) == 0 || len(r.Datasources) == 0 {
		return ws
	}
	// Each dep type and datasource is possible on its own; one dep must have both.
	for _, m := range ms {
		mi := managerByName(m)
		for _, dt := range r.DepTypes {
			for _, ds := range r.Datasources {
				if uses(mi, dt, CanonicalDatasource(ds)) {
					return nil
				}
			}
		}
	}
	return []Warning{{f, fmt.Sprintf("matches nothing: no dep of depTypes [%s] uses datasources [%s]", strings.Join(r.DepTypes, ", "), strings.Join(r.Datasources, ", "))}}
}
