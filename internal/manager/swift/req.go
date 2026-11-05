package swift

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// reqKind is the form of a package requirement.
type reqKind int

const (
	reqUnsupported reqKind = iota
	reqExact               // exact: "1.2.3", .exact("1.2.3")
	reqFrom                // from: "1.2.3", .upToNextMajor(from: "1.2.3")
	reqNextMinor           // .upToNextMinor(from: "1.2.3")
	reqHalfOpen            // "1.2.3"..<"2.0.0"
	reqClosed              // "1.2.3"..."1.9.0"
	reqBranch              // branch: "main", .branch("main")
	reqRevision            // revision: "<sha>", .revision("<sha>")
)

// version is a version string of a requirement and the span of its content in the
// requirement's text.
type version struct {
	v    string
	span dep.Span
}

// requirement is a parsed package requirement. upper is only set for the binary ranges; label
// is the span of the `from` label of `from: "…"`, whole that of all requirement tokens.
type requirement struct {
	kind         reqKind
	lower, upper version
	label, whole dep.Span
}

// parseRequirement parses the requirement argument text of a `.package(url:…)` call: the
// labeled forms `from:`, `exact:`, `branch:`, `revision:`, the static members `.exact(…)`,
// `.upToNextMajor(from:)`, `.upToNextMinor(from:)`, `.branch(…)`, `.revision(…)`, and the ranges
// `"a"..<"b"`, `"a"..."b"`. Versions must be full semantic versions (`1.2.3[-pre][+build]`), as
// SwiftPM's Version requires. Anything else, such as a variable or `Version(1, 2, 3)`, is
// reqUnsupported.
func parseRequirement(text string) requirement {
	toks := tokenize([]byte(text))
	r := parseTokens(toks)
	if r.kind != reqUnsupported && len(toks) > 0 {
		r.whole = dep.Span{Start: toks[0].start, End: toks[len(toks)-1].end}
	}
	return r
}

// parseTokens is parseRequirement of the requirement's tokens, without whole.
func parseTokens(toks []token) requirement {
	label := ""
	var labelSpan dep.Span
	if len(toks) >= 2 && toks[0].kind == tokIdent && toks[1].kind == tokPunct && toks[1].text == ":" {
		label, labelSpan, toks = toks[0].text, dep.Span{Start: toks[0].start, End: toks[0].end}, toks[2:]
	}
	switch label {
	case "from":
		r := single(reqFrom, toks)
		r.label = labelSpan
		return r
	case "exact":
		return single(reqExact, toks)
	case "branch":
		return requirement{kind: reqBranch}
	case "revision":
		return requirement{kind: reqRevision}
	case "":
	default:
		return requirement{}
	}
	if len(toks) == 3 && toks[0].kind == tokString && toks[1].kind == tokPunct && toks[2].kind == tokString {
		kind := reqClosed
		if toks[1].text == "..<" {
			kind = reqHalfOpen
		} else if toks[1].text != "..." {
			return requirement{}
		}
		lo, ok1 := versionOf(toks[0])
		hi, ok2 := versionOf(toks[2])
		if !ok1 || !ok2 {
			return requirement{}
		}
		return requirement{kind: kind, lower: lo, upper: hi}
	}
	if len(toks) < 4 || toks[0].kind != tokPunct || toks[0].text != "." || toks[1].kind != tokIdent ||
		toks[2].text != "(" || toks[len(toks)-1].text != ")" {
		return requirement{}
	}
	inner := toks[3 : len(toks)-1]
	switch toks[1].text {
	case "exact":
		return single(reqExact, inner)
	case "upToNextMajor", "upToNextMinor":
		if len(inner) != 3 || inner[0].kind != tokIdent || inner[0].text != "from" || inner[1].text != ":" {
			return requirement{}
		}
		kind := reqFrom
		if toks[1].text == "upToNextMinor" {
			kind = reqNextMinor
		}
		return single(kind, inner[2:])
	case "branch":
		return requirement{kind: reqBranch}
	case "revision":
		return requirement{kind: reqRevision}
	}
	return requirement{}
}

// single is the requirement of kind whose only token is its version string.
func single(kind reqKind, toks []token) requirement {
	if len(toks) != 1 {
		return requirement{}
	}
	v, ok := versionOf(toks[0])
	if !ok {
		return requirement{}
	}
	return requirement{kind: kind, lower: v}
}

// versionOf returns the version of a plain string token holding a full semantic version.
func versionOf(t token) (version, bool) {
	if t.kind != tokString || !t.plain {
		return version{}, false
	}
	if _, err := semver.StrictNewVersion(t.text); err != nil {
		return version{}, false
	}
	return version{v: t.text, span: dep.Span{Start: t.start + 1, End: t.end - 1}}, true
}

// isRange reports whether the requirement admits more than one version.
func (r requirement) isRange() bool {
	return r.kind == reqFrom || r.kind == reqNextMinor || r.kind == reqHalfOpen || r.kind == reqClosed
}

// constraint returns the requirement as a Masterminds constraint: SwiftPM's `from:` is up to
// the next major version also for 0.x (`0.3.0` admits every 0.y.z ≥ 0.3.0).
func (r requirement) constraint() string {
	lo := semver.MustParse(r.lower.v)
	switch r.kind {
	case reqFrom:
		return fmt.Sprintf(">=%s <%d.0.0", r.lower.v, lo.Major()+1)
	case reqNextMinor:
		return fmt.Sprintf(">=%s <%d.%d.0", r.lower.v, lo.Major(), lo.Minor()+1)
	case reqHalfOpen:
		return ">=" + r.lower.v + " <" + r.upper.v
	case reqClosed:
		return ">=" + r.lower.v + " <=" + r.upper.v
	}
	return "=" + r.lower.v
}

// admits reports whether v satisfies the requirement.
func (r requirement) admits(v string) bool {
	sv, err := semver.NewVersion(v)
	if err != nil {
		return false
	}
	c, err := semver.NewConstraint(r.constraint())
	return err == nil && c.Check(sv)
}

// versionEdit replaces old, the text at span (in the requirement's text), with to.
type versionEdit struct {
	span    dep.Span
	old, to string
}

// edits returns the changes of the requirement text for v under strategy (a
// config.RangeStrategies value; "" is the default). Except under pin only the version strings
// change, so the form is kept: `from:`, `.upToNextMajor(from:)` and `.upToNextMinor(from:)`
// take v as their lower bound under every strategy (widen cannot widen them without another
// form). Binary ranges take v as their lower bound except under widen, which keeps it; an
// upper bound v reaches moves past it: `..."b"` to v, `..<"b"` to the next boundary of b's
// precision above v (`..<"2.0.0"` → `..<"4.0.0"` for 3.1.0, `..<"1.5.0"` → `..<"1.8.0"` for
// 1.7.2). pin makes the requirement exact (pinEdits). text is the requirement's text, exactLabel
// whether its call takes `exact:` (dep.Dep.ExactLabel). Edits are in text order.
func (r requirement) edits(text, v, strategy string, exactLabel bool) []versionEdit {
	v = strings.TrimPrefix(v, "v")
	nv, err := semver.StrictNewVersion(v)
	if !r.isRange() || err != nil {
		return nil
	}
	if strategy == dep.RangePin {
		return r.pinEdits(text, v, exactLabel)
	}
	var out []versionEdit
	if (strategy != dep.RangeWiden || r.kind == reqFrom || r.kind == reqNextMinor) && r.lower.v != v {
		out = append(out, versionEdit{r.lower.span, r.lower.v, v})
	}
	if r.kind == reqHalfOpen || r.kind == reqClosed {
		c := nv.Compare(semver.MustParse(r.upper.v))
		switch {
		case r.kind == reqClosed && c > 0:
			out = append(out, versionEdit{r.upper.span, r.upper.v, v})
		case r.kind == reqHalfOpen && c >= 0:
			out = append(out, versionEdit{r.upper.span, r.upper.v, boundary(r.upper.v, v)})
		}
	}
	return out
}

// pinEdits rewrites the range requirement into the exact requirement of v. With exactLabel it
// becomes `exact: "v"`: `from: "1.2.3"` changes its label and version string, every other
// form (`.upToNextMajor(from:)`, `.upToNextMinor(from:)`, `"a"..<"b"`, `"a"..."b"`) all of its
// tokens. Without it, `.exact("v")` replaces all tokens: the `exact:` label needs
// swift-tools-version 5.6 and does not combine with `name:`, while `.exact(…)` is deprecated
// from 5.6 on and does not combine with `traits:`.
func (r requirement) pinEdits(text, v string, exactLabel bool) []versionEdit {
	if exactLabel && r.label != (dep.Span{}) {
		out := []versionEdit{{r.label, text[r.label.Start:r.label.End], "exact"}}
		if r.lower.v != v {
			out = append(out, versionEdit{r.lower.span, r.lower.v, v})
		}
		return out
	}
	to := `.exact("` + v + `")`
	if exactLabel {
		to = `exact: "` + v + `"`
	}
	return []versionEdit{{r.whole, text[r.whole.Start:r.whole.End], to}}
}

// rewrite returns text, the requirement's text, with the edits for v under strategy.
func (r requirement) rewrite(text, v, strategy string, exactLabel bool) string {
	var b strings.Builder
	pos := 0
	for _, e := range r.edits(text, v, strategy, exactLabel) {
		b.WriteString(text[pos:e.span.Start])
		b.WriteString(e.to)
		pos = e.span.End
	}
	b.WriteString(text[pos:])
	return b.String()
}

// boundary returns the exclusive upper bound above v at the precision of the bound it
// replaces: the next major for `X.0.0`, the next minor for `X.Y.0`, else the next patch.
func boundary(old, v string) string {
	o, n := semver.MustParse(old), semver.MustParse(v)
	switch {
	case o.Minor() == 0 && o.Patch() == 0:
		return fmt.Sprintf("%d.0.0", n.Major()+1)
	case o.Patch() == 0:
		return fmt.Sprintf("%d.%d.0", n.Major(), n.Minor()+1)
	}
	return fmt.Sprintf("%d.%d.%d", n.Major(), n.Minor(), n.Patch()+1)
}
