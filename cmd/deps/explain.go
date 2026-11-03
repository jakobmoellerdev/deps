package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
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
		Long: "Explain resolves only the dependencies whose name matches any of the path.Match globs\n" +
			"and prints, per occurrence, the matching rules, the effective policy, the rejected\n" +
			"candidates by reason and the resulting update.",
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
		if _, err := path.Match(g, ""); err != nil {
			return &usageError{fmt.Errorf("invalid glob %q: %w", g, err)}
		}
	}
	return nil
}

func explain(cmd *cobra.Command, o *rootOpts, globs []string) error {
	ctx := cmd.Context()
	info, _ := platform.FromEnv(os.Getenv)
	s, err := newSession(o, info, envToken(), newHTTPClient(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	matched := make([]bool, len(globs))
	var traces []engine.Trace
	// Filter and Trace run sequentially on the Resolve goroutine.
	opts := s.options()
	opts.Filter = func(d dep.Dep) bool {
		ok := false
		for i, g := range globs {
			if m, _ := path.Match(g, d.Name); m {
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
		fmt.Fprintf(stderr, "error   %v\n", e)
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
		cur += "@" + d.Digest[:min(7, len(d.Digest))]
	}
	line("current", "%s", orDash(cur))
	if d.Skip != "" && t.Policy.Updates == nil { // skipped by the manager: no policy was evaluated
		line("result", "skipped: %s", d.Skip)
		return
	}
	if len(t.Rules) == 0 {
		line("rules", "none")
	}
	for i, r := range t.Rules {
		label := ""
		if i == 0 {
			label = "rules"
		}
		line(label, "#%d %s", r, describeRule(cfg.Rules[r]))
	}
	line("policy", "%s", describePolicy(t.Policy))
	if d.Skip != "" {
		line("result", "skipped: %s", d.Skip)
		return
	}
	line("candidates", "%d", t.Candidates)
	line("rejected", "%s", describeRejected(d.Versioning, t.Rejected))
	switch {
	case t.Err != nil:
		line("result", "error: %v", t.Err)
	case t.Update == nil:
		line("result", "up to date")
	default:
		from, to := t.Update.FromTo()
		line("result", "update %s -> %s (%s)", from, to, t.Update.Type)
		for _, a := range t.Update.Advisories {
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
	if r.Ignore != nil {
		add("ignore", strconv.FormatBool(*r.Ignore))
	}
	if r.Pin != "" {
		add("pin", r.Pin)
	}
	if r.Allow != "" {
		allow := r.Allow
		if c, err := semver.NewConstraint(allow); err == nil { // validated by config.Load
			allow = c.String()
		}
		add("allow", allow)
	}
	if r.Updates != nil {
		add("updates", "["+strings.Join(r.Updates, " ")+"]")
	}
	if r.Group != "" {
		add("group", r.Group)
	}
	if r.Schedule != nil {
		add("schedule", "["+strings.Join(r.Schedule, "; ")+"]")
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
	return fmt.Sprintf("pin=%s allow=%s updates=%s group=%s schedule=%s",
		p.Pin, allow, orDash(strings.Join(updates, ",")), orDash(p.Group), p.Schedule.String())
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
