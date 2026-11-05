package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// maxExamples is the number of versions listed per rejection reason.
const maxExamples = 3

func newExplain(o *rootOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "explain <name-glob>...",
		Short: "Explain the rules, candidates and decision for each matching dependency",
		Long:  helpText("explain"),
		Example: "  deps explain 'actions/*'\n" +
			"  deps explain 'k8s.io/**' github.com/spf13/cobra",
		Args: globArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return explain(cmd, o, args)
		},
	}
}

func globArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return &usageError{errors.New("requires at least one name glob")}
	}
	for _, g := range args {
		if _, err := doublestar.Match(g, ""); err != nil {
			return &usageError{fmt.Errorf("invalid glob %q: %w", g, err)}
		}
	}
	return nil
}

func explain(cmd *cobra.Command, o *rootOpts, globs []string) error {
	ctx := cmd.Context()
	info, _ := platform.FromEnv(os.Getenv)
	s, err := newSession(o, info, envToken(), newHTTPClient())
	if err != nil {
		return err
	}
	defer s.close()
	matched := make([]bool, len(globs))
	var traces []engine.Trace
	// Filter and Trace run sequentially on the Resolve goroutine.
	opts := s.options()
	opts.Filter = func(d dep.Dep) bool {
		ok := false
		for i, g := range globs {
			if m, _ := doublestar.Match(g, d.Name); m {
				matched[i], ok = true, true
			}
		}
		return ok
	}
	opts.Trace = func(t engine.Trace) { traces = append(traces, t) }
	res, err := engine.Resolve(ctx, s.root, s.cfg, s.env, opts)
	if err != nil {
		return err
	}
	slices.SortStableFunc(traces, func(a, b engine.Trace) int {
		return cmp.Or(strings.Compare(a.Dep.File, b.Dep.File), cmp.Compare(a.Dep.Line, b.Dep.Line))
	})
	var out bytes.Buffer
	for i, t := range traces {
		if i > 0 {
			out.WriteByte('\n')
		}
		writeTrace(&out, s.cfg, t)
	}
	if _, err := cmd.OutOrStdout().Write(out.Bytes()); err != nil {
		return fmt.Errorf("write explanation: %w", err)
	}
	stderr := cmd.ErrOrStderr()
	for _, e := range res.Errors {
		reportError(stderr, o.term, e)
	}
	var unmatched []string
	for i, g := range globs {
		if !matched[i] {
			unmatched = append(unmatched, g)
		}
	}
	if len(unmatched) > 0 {
		return fmt.Errorf("no dependency matches %s", strings.Join(unmatched, ", "))
	}
	if len(res.Errors) > 0 {
		return errReported
	}
	return nil
}

// writeTrace renders one dep occurrence: a header, then labeled lines.
func writeTrace(w io.Writer, cfg *config.Config, t engine.Trace) {
	d := t.Dep
	fmt.Fprintf(w, "%s  %s:%d  (%s, %s, %s, %s)\n", d.Name, d.File, d.Line,
		orDash(d.Manager), orDash(d.DepType), orDash(d.Datasource), orDash(d.Versioning))
	line := func(label, format string, a ...any) {
		fmt.Fprintf(w, "  %-9s %s\n", label, fmt.Sprintf(format, a...))
	}
	cur := d.Version
	if d.Digest != "" {
		cur += "@" + dep.ShortDigest(d.Digest)
	}
	if d.Range != "" && d.Range != d.Version {
		cur = d.Range
	}
	switch {
	case d.Locked != "":
		cur += " (locked " + d.Locked + ")"
	case d.LockUnread:
		cur += " (lockfile not read)"
	}
	line("current", "%s", orDash(cur))
	if t.AgeLookup {
		age := "unknown (currentAge rules do not match)"
		if !t.Released.IsZero() {
			age = "released " + t.Released.UTC().Format(time.DateOnly)
		}
		line("age", "%s", age)
	}
	if d.Skip != "" && t.Policy.Updates == nil { // skipped by the manager: no policy was evaluated
		line("result", "skipped: %s", d.Skip)
		return
	}
	if t.Policy.Monorepo != "" {
		line("builtin", "monorepo %s -> group %q", t.Policy.Monorepo, config.MonorepoGroup(t.Policy.Monorepo))
	}
	// Rules with updateTypes that applied to the updates, after the pre-policy rules, and the
	// per-type policies they produced (once per type).
	type kindRule struct {
		rule int
		kind dep.UpdateType
	}
	var kindRules []kindRule
	var kindPolicies []*config.Policy
	for _, tu := range t.Updates {
		n := len(kindRules)
		for _, r := range tu.Policy.Rules {
			if kr := (kindRule{r, tu.Policy.Kind}); len(cfg.Rules[r].UpdateTypes) > 0 && !slices.Contains(kindRules, kr) {
				kindRules = append(kindRules, kr)
			}
		}
		if len(kindRules) > n {
			kindPolicies = append(kindPolicies, tu.Policy)
		}
	}
	if len(t.Rules) == 0 && len(kindRules) == 0 && t.Policy.Monorepo == "" {
		line("rules", "none")
	}
	for i, r := range t.Rules {
		label := ""
		if i == 0 {
			label = "rules"
		}
		line(label, "#%d %s", r, describeRule(cfg.Rules[r]))
	}
	for i, kr := range kindRules {
		label := ""
		if i == 0 && len(t.Rules) == 0 {
			label = "rules"
		}
		line(label, "#%d %s (applies to %s)", kr.rule, describeRule(cfg.Rules[kr.rule]), kr.kind)
	}
	line("policy", "%s", describePolicy(t.Policy))
	for _, kp := range kindPolicies {
		line("", "%s: %s", kp.Kind, describePolicy(*kp))
	}
	if d.Skip != "" {
		line("result", "skipped: %s", d.Skip)
		return
	}
	line("candidates", "%d", t.Candidates)
	line("rejected", "%s", describeRejected(d.Versioning, t.Rejected))
	switch {
	case t.Err != nil:
		line("result", "error: %v", t.Err)
	case t.Updates == nil:
		line("result", "up to date")
	}
	for _, tu := range t.Updates {
		u := tu.Update
		from, to := u.FromTo()
		bucket := ""
		if u.Bucket != "" {
			bucket = ", bucket " + u.Bucket
		}
		if u.Lockfile {
			bucket += ", lockfile"
		}
		line("result", "update %s -> %s (%s%s)", from, to, u.Type, bucket)
		for _, a := range u.Advisories {
			line("security", "%s", strings.Join(nonEmpty(a.ID, a.Summary, a.URL), "  "))
		}
	}
}

// describeRule lists the fields a rule sets, in declaration order.
func describeRule(r config.Rule) string {
	var f []string
	add := func(k, v string) { f = append(f, k+"="+v) }
	if r.Manager != "" {
		add("manager", r.Manager)
	}
	if r.Names != nil {
		add("names", "["+strings.Join(r.Names, " ")+"]")
	}
	if r.DepTypes != nil {
		add("depTypes", "["+strings.Join(r.DepTypes, " ")+"]")
	}
	if r.Paths != nil {
		add("paths", "["+strings.Join(r.Paths, " ")+"]")
	}
	if r.Datasources != nil {
		add("datasources", "["+strings.Join(r.Datasources, " ")+"]")
	}
	if r.CurrentVersion != "" {
		add("currentVersion", r.CurrentVersion)
	}
	if r.CurrentAge != "" {
		add("currentAge", "'"+r.CurrentAge+"'")
	}
	if r.UpdateTypes != nil {
		add("updateTypes", "["+strings.Join(r.UpdateTypes, " ")+"]")
	}
	if r.Ignore != nil {
		add("ignore", strconv.FormatBool(*r.Ignore))
	}
	if r.Pin != "" {
		add("pin", r.Pin)
	}
	if r.Allow != "" {
		allow := r.Allow
		if a, err := config.ParseAllowed(allow); err == nil { // validated by config.Load
			allow = a.String()
		}
		add("allow", allow)
	}
	if r.Updates != nil {
		add("updates", "["+strings.Join(r.Updates, " ")+"]")
	}
	if r.Group != "" {
		add("group", r.Group)
	}
	if r.SecurityGroup != "" {
		add("securityGroup", r.SecurityGroup)
	}
	if r.Schedule != nil {
		add("schedule", "["+strings.Join(r.Schedule, "; ")+"]")
	}
	if r.MinimumReleaseAge != "" {
		add("minimumReleaseAge", r.MinimumReleaseAge)
	}
	if r.MinimumReleaseAgeBehaviour != "" {
		add("minimumReleaseAgeBehaviour", r.MinimumReleaseAgeBehaviour)
	}
	if r.Versioning != "" {
		add("versioning", r.Versioning)
	}
	if r.RangeStrategy != "" {
		add("rangeStrategy", r.RangeStrategy)
	}
	for _, b := range []struct {
		k string
		v *bool
	}{{"separateMajorMinor", r.SeparateMajorMinor}, {"separateMultipleMajor", r.SeparateMultipleMajor}, {"separateMinorPatch", r.SeparateMinorPatch}} {
		if b.v != nil {
			add(b.k, strconv.FormatBool(*b.v))
		}
	}
	if r.Automerge != nil {
		add("automerge", strconv.FormatBool(*r.Automerge))
	}
	if r.AutomergeStrategy != "" {
		add("automergeStrategy", r.AutomergeStrategy)
	}
	if r.AutomergeType != "" {
		add("automergeType", r.AutomergeType)
	}
	if len(f) == 0 {
		return "(matches all)"
	}
	return strings.Join(f, " ")
}

func describePolicy(p config.Policy) string {
	allow := "-"
	if p.Allow != nil {
		allow = p.Allow.String()
	}
	var updates []string
	for _, u := range dep.UpdateTypes {
		if p.Updates[u] {
			updates = append(updates, string(u))
		}
	}
	minAge := "-"
	if p.MinAge > 0 {
		minAge = config.FormatAge(p.MinAge)
		if p.TimestampRequired {
			minAge += " (" + config.TimestampRequired + ")"
		}
	}
	s := fmt.Sprintf("pin=%s allow=%s updates=%s group=%s schedule=%s minAge=%s",
		p.Pin, allow, orDash(strings.Join(updates, ",")), orDash(p.Group), p.Schedule.String(), minAge)
	if p.SecurityGroup != "" {
		s += " securityGroup=" + p.SecurityGroup
	}
	if p.Versioning != "" {
		s += " versioning=" + p.Versioning
	}
	if p.RangeStrategy != "" && p.RangeStrategy != config.RangeAuto {
		s += " rangeStrategy=" + p.RangeStrategy
	}
	switch {
	case p.Automerge && p.AutomergeType == config.AutomergeTypeBranch:
		s += " automerge=branch"
	case p.Automerge:
		s += " automerge=" + p.AutomergeStrategy
	}
	return s
}

// describeRejected renders "<reason>: <n> (<newest versions>, ...)" per reason, the most
// frequent first. The "policy: " prefix of policy reasons is dropped.
func describeRejected(scheme string, rejected map[string][]string) string {
	if len(rejected) == 0 {
		return "none"
	}
	reasons := make([]string, 0, len(rejected))
	for r := range rejected {
		reasons = append(reasons, r)
	}
	slices.SortFunc(reasons, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(rejected[b]), len(rejected[a])), strings.Compare(a, b))
	})
	s, _ := versioning.Get(scheme)
	parts := make([]string, len(reasons))
	for i, r := range reasons {
		vs := rejected[r]
		parts[i] = fmt.Sprintf("%s: %d (%s)", strings.TrimPrefix(r, "policy: "), len(vs), examples(s, vs))
	}
	return strings.Join(parts, "  ")
}

// examples returns the newest maxExamples versions (valid ones first, newest first).
func examples(s versioning.Scheme, vs []string) string {
	vs = slices.Clone(vs)
	if s != nil {
		slices.SortStableFunc(vs, func(a, b string) int {
			va, vb := s.Valid(a), s.Valid(b)
			switch {
			case va && vb:
				return s.Compare(b, a)
			case va:
				return -1
			case vb:
				return 1
			}
			return 0
		})
	}
	n := min(maxExamples, len(vs))
	out := strings.Join(vs[:n], ", ")
	if len(vs) > n {
		out += ", ..."
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func nonEmpty(ss ...string) []string {
	return slices.DeleteFunc(ss, func(s string) bool { return s == "" })
}
