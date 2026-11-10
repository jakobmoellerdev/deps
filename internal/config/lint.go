package config

import (
	"cmp"
	"fmt"
	"iter"
	"maps"
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

// ruleFields are the fields of Rule with a yaml key (keyFields).
var ruleFields = keyFields(reflect.TypeFor[Rule]())

func ruleApplyFields() []string {
	var out []string
	for _, f := range ruleFields {
		if k := yamlKey(f); !slices.Contains(RuleMatchFields, k) {
			out = append(out, k)
		}
	}
	return out
}

// keyFields returns the fields of struct t that have a yaml key (yamlKey), in declaration
// order; the fields of an embedded struct tagged `yaml:",inline"` take its place. Their Index
// is relative to t (reflect.Value.FieldByIndex).
func keyFields(t reflect.Type) []reflect.StructField {
	var out []reflect.StructField
	for f := range t.Fields() {
		if inline(f) {
			for _, g := range keyFields(f.Type) {
				g.Index = append(slices.Clone(f.Index), g.Index...)
				out = append(out, g)
			}
		} else if yamlKey(f) != "" {
			out = append(out, f)
		}
	}
	return out
}

// inline reports whether f is an embedded struct whose keys are those of its parent.
func inline(f reflect.StructField) bool {
	return f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("yaml") == ",inline"
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
// looking at the repository: rules that set no apply field, match fields no dep can satisfy
// together (a dep type no listed manager produces, a datasource no listed manager or dep type
// uses), and groups entries no policy group has (no rule sets the group, and no built-in group
// rule applying has the name). Rules that match no dep of the repository are a run-time
// finding (UnusedRules).
func (c *Config) Lint() []Warning {
	ws := make([]Warning, 0, len(c.Rules))
	for i := range c.Rules {
		ws = append(ws, c.Rules[i].lint(fmt.Sprintf("rules[%d]", i))...)
	}
	return append(ws, c.lintGroups()...)
}

// lintGroups reports the groups entries whose name is no group a policy can have.
func (c *Config) lintGroups() []Warning {
	var ws []Warning
	for _, name := range slices.Sorted(maps.Keys(c.Groups)) {
		builtin := BuiltinGroup(name)
		if name == "" || (builtin && c.MonoreposEnabled()) || slices.ContainsFunc(c.Rules, func(r Rule) bool { return r.Group == name }) {
			continue
		}
		msg := fmt.Sprintf("no rule sets group %q, so its settings apply to nothing", name)
		switch {
		case builtin:
			msg = fmt.Sprintf("no rule sets group %q and the built-in groups are off (builtins.monorepos: false), so its settings apply to nothing", name)
		case slices.ContainsFunc(c.Rules, func(r Rule) bool { return r.SecurityGroup == name }):
			msg += " (a securityGroup names no groups entry: security updates follow the entry of their dep's group)"
		}
		ws = append(ws, Warning{"groups." + name, msg})
	}
	return ws
}

// SetApplyFields returns the keys of the apply fields r sets, in declaration order: a
// non-nil list (also []), a non-nil pointer or a non-empty string.
func (r *Rule) SetApplyFields() []string { return r.setFields(RuleApplyFields) }

// EachSet yields the yaml key and value of each field r sets, in declaration order: a
// non-zero value (a non-nil list, also [], a non-nil pointer or a non-empty string).
func (r *Rule) EachSet() iter.Seq2[string, reflect.Value] {
	return eachSet(reflect.ValueOf(r).Elem(), ruleFields)
}

// EachSet yields the yaml key and value of each setting g sets, in declaration order (see
// Rule.EachSet).
func (g *GroupSettings) EachSet() iter.Seq2[string, reflect.Value] {
	return eachSet(reflect.ValueOf(g).Elem(), groupFields)
}

// groupFields are the key fields of GroupSettings.
var groupFields = keyFields(reflect.TypeFor[GroupSettings]())

func eachSet(v reflect.Value, fields []reflect.StructField) iter.Seq2[string, reflect.Value] {
	return func(yield func(string, reflect.Value) bool) {
		for _, f := range fields {
			if fv := v.FieldByIndex(f.Index); !fv.IsZero() && !yield(yamlKey(f), fv) {
				return
			}
		}
	}
}

// setFields returns the keys among keys of the fields r sets, in declaration order.
func (r *Rule) setFields(keys []string) []string {
	var out []string
	for k := range r.EachSet() {
		if slices.Contains(keys, k) {
			out = append(out, k)
		}
	}
	return out
}

// hasSet reports whether r sets a field among keys (setFields).
func (r *Rule) hasSet(keys []string) bool { return len(r.setFields(keys)) > 0 }

// sets reports whether r sets key to a value: like hasSet, but an empty list sets nothing.
func (r *Rule) sets(key string) bool {
	i := slices.IndexFunc(ruleFields, func(f reflect.StructField) bool { return yamlKey(f) == key })
	if i < 0 {
		return false
	}
	fv := reflect.ValueOf(r).Elem().FieldByIndex(ruleFields[i].Index)
	return !fv.IsZero() && (fv.Kind() != reflect.Slice || fv.Len() > 0)
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
