package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// maxExamples is the number of versions listed per rejection reason.
const maxExamples = 3

func newExplain(o *rootOpts) *cli.Command {
	var base string
	return &cli.Command{
		Name:      "explain",
		ArgsUsage: "<name-glob>...",
		Usage:     "Explain the rules, candidates and decision for each matching dependency",
		Metadata: map[string]any{docgen.MetaExample: "  deps explain 'actions/*'\n" +
			"  deps explain 'k8s.io/**' github.com/spf13/cobra\n" +
			"  deps explain --base release/1.x 'actions/*'"},
		ArgValidator: globArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return explain(ctx, cmd, o, base, cmd.Args().Slice())
		},
		Flags: append(scanFlags(o, scanSession),
			&cli.StringFlag{Name: "base", Destination: &base, Usage: "explain base branch <base> as run resolves it: from the tip of origin's branch (a temporary work tree unless the checkout is at it), its rules[].baseBranches matching it, with useBaseBranchConfig its config merged over the checkout's"},
		),
	}
}

func globArgs(_ context.Context, cmd *cli.Command) error {
	args := cmd.Args().Slice()
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

// explain traces the deps matching globs; base, when set, is the base branch to explain
// (openBase, session.baseSession), named with the config files applied on stderr.
func explain(ctx context.Context, cmd *cli.Command, o *rootOpts, base string, globs []string) error {
	info, _ := platform.FromEnv(os.Getenv)
	s, err := newSession(ctx, o, info, envToken(), newHTTPClient(), cmd.Root().ErrWriter)
	if err != nil {
		return err
	}
	defer s.close()
	stderr := cmd.Root().ErrWriter
	if base != "" {
		repo := gitx.Repo{Dir: s.root}
		b, err := openBase(ctx, repo, base)
		if err != nil {
			return err
		}
		defer closeBases(ctx, repo, []runBase{b})
		if s, err = s.baseSession(ctx, b); err != nil {
			return err
		}
		fmt.Fprintln(stderr, baseLine(base, s.cfg))
	}
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
	if _, err := cmd.Root().Writer.Write(out.Bytes()); err != nil {
		return fmt.Errorf("write explanation: %w", err)
	}
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
	if errs, _ := splitRateLimited(res.Errors); len(errs) > 0 {
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
	if d.Skip != "" && t.Policy.AllowedUpdateTypes == nil { // skipped by the manager: no policy was evaluated
		line("result", "skipped: %s", d.Skip)
		return
	}
	if t.Policy.Monorepo != "" {
		line("builtin", "monorepo %s -> group %q", t.Policy.Monorepo, config.MonorepoGroup(t.Policy.Monorepo))
	}
	if t.Policy.Toolchain != "" {
		line("builtin", "toolchain %s -> group %q", t.Policy.Toolchain, t.Policy.Toolchain)
	}
	if t.Policy.Registry != "" {
		line("builtin", "registry %s -> group %q", t.Policy.Registry, t.Policy.Registry)
	}
	if t.Policy.Replacement != "" {
		line("builtin", "replacement %s -> %s", t.Policy.Replacement, t.Policy.ReplacementName)
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
	if len(t.Rules) == 0 && len(kindRules) == 0 && t.Policy.Monorepo == "" && t.Policy.Toolchain == "" && t.Policy.Registry == "" && t.Policy.Replacement == "" {
		line("rules", "none")
	}
	for i, r := range t.Rules {
		label := ""
		if i == 0 {
			label = "rules"
		}
		desc := describeRule(cfg.Rules[r])
		if from := sourceOf(&cfg.Rules[r]); from != "" {
			desc += " (" + from + ")"
		}
		line(label, "#%d %s", r, desc)
	}
	for i, kr := range kindRules {
		label := ""
		if i == 0 && len(t.Rules) == 0 {
			label = "rules"
		}
		applies := "applies to " + string(kr.kind)
		if from := sourceOf(&cfg.Rules[kr.rule]); from != "" {
			applies += ", " + from
		}
		line(label, "#%d %s (%s)", kr.rule, describeRule(cfg.Rules[kr.rule]), applies)
	}
	// The groups entries applied after the rules: the pre-policy's, then those of the per-type
	// policies, once per group.
	var groups []string
	addGroup := func(p *config.Policy) {
		if p.GroupSettings && !slices.Contains(groups, p.Group) {
			groups = append(groups, p.Group)
		}
	}
	addGroup(&t.Policy)
	for _, tu := range t.Updates {
		addGroup(tu.Policy)
	}
	for _, name := range groups {
		g := cfg.Groups[name]
		desc := describeGroup(g)
		if from := sourceOf(g); from != "" {
			desc += " (" + from + ")"
		}
		line("group", "%s settings: %s", name, desc)
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
	case errors.Is(t.Err, httpx.ErrRateLimited):
		line("result", "skipped: %s", limitReason(t.Err))
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

// sourceOf renders the file and line a rule or groups entry is written in ("from
// .deps.yaml:12", or the extends entry of another repository's file), "" when it is unknown.
func sourceOf(s interface{ Source() (string, int) }) string {
	file, line := s.Source()
	if file == "" {
		return ""
	}
	return fmt.Sprintf("from %s:%d", file, line)
}

// describeGroup lists the keys a groups entry sets, in declaration order ("nothing" when it
// sets none).
func describeGroup(g *config.GroupSettings) string {
	var set []string
	for k, v := range g.EachSet() {
		if s, ok := describeRuleValue(k, v); ok {
			set = append(set, k+"="+s)
		}
	}
	return cmp.Or(strings.Join(set, " "), "nothing")
}

// describeRule lists the fields a rule sets in two groups, in declaration order: the match
// fields ("match: all" when none is set), then the apply fields.
func describeRule(r config.Rule) string {
	var match, apply []string
	for k, v := range r.EachSet() {
		s, ok := describeRuleValue(k, v)
		if !ok {
			continue
		}
		if slices.Contains(config.RuleMatchFields, k) {
			match = append(match, k+"="+s)
		} else {
			apply = append(apply, k+"="+s)
		}
	}
	m := "all"
	if len(match) > 0 {
		m = strings.Join(match, " ")
	}
	a := "nothing"
	if len(apply) > 0 {
		a = strings.Join(apply, " ")
	}
	return "match: " + m + "; apply: " + a
}

// describeRuleValue renders a rule field's value; ok is false when the field is unset.
func describeRuleValue(k string, v reflect.Value) (string, bool) {
	switch k {
	case "allow":
		allow := v.String()
		if allow == "" {
			return "", false
		}
		if a, err := config.ParseAllowed(allow); err == nil { // validated by config.Load
			allow = a.String()
		}
		return allow, true
	case "currentAge":
		if v.String() == "" {
			return "", false
		}
		return "'" + v.String() + "'", true
	}
	switch val := v.Interface().(type) {
	case string:
		return val, val != ""
	case *string:
		if val == nil {
			return "", false
		}
		return strconv.Quote(*val), true
	case *bool:
		if val == nil {
			return "", false
		}
		return strconv.FormatBool(*val), true
	case []string:
		if val == nil {
			return "", false
		}
		sep := " "
		if k == "schedule" {
			sep = "; "
		}
		return "[" + strings.Join(val, sep) + "]", true
	case *config.PostUpgradeTasks:
		if val == nil {
			return "", false
		}
		s := "[" + strings.Join(val.Commands, "; ") + "]"
		if val.ExecutionMode != "" {
			s += " executionMode=" + val.ExecutionMode
		}
		return s, true
	}
	if v.IsZero() {
		return "", false
	}
	return fmt.Sprint(v.Interface()), true
}

func describePolicy(p config.Policy) string {
	allow := "-"
	if p.Allow != nil {
		allow = p.Allow.String()
	}
	var updates []string
	for _, u := range dep.UpdateTypes {
		if p.AllowedUpdateTypes[u] {
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
	s := fmt.Sprintf("pin=%s allow=%s allowedUpdateTypes=%s group=%s schedule=%s minAge=%s",
		p.Pin, allow, orDash(strings.Join(updates, ",")), orDash(p.Group), p.Schedule.String(), minAge)
	if p.SecurityGroup != "" {
		s += " securityGroup=" + p.SecurityGroup
	}
	if p.AdditionalBranchPrefix != "" {
		s += " additionalBranchPrefix=" + strconv.Quote(p.AdditionalBranchPrefix)
	}
	if p.Versioning != "" {
		s += " versioning=" + p.Versioning
	}
	if p.RangeStrategy != "" && p.RangeStrategy != config.RangeAuto {
		s += " rangeStrategy=" + p.RangeStrategy
	}
	if p.ReplacementName != "" {
		s += " replacement=" + p.ReplacementName
		if p.ReplacementVersion != "" {
			s += "@" + p.ReplacementVersion
		}
	}
	switch {
	case p.Automerge && p.AutomergeType == config.AutomergeTypeBranch:
		s += " automerge=branch"
	case p.Automerge:
		s += " automerge=" + p.AutomergeStrategy
	}
	if p.DashboardApproval {
		s += " dashboardApproval=true"
	}
	if p.DraftPR {
		s += " draftPR=true"
	}
	if p.PRCreation != "" && p.PRCreation != config.PRCreationImmediate {
		s += " prCreation=" + p.PRCreation
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
