package versioning

import (
	"cmp"
	"strings"
)

// IvyKind is the kind of an Ivy dynamic revision.
type IvyKind uint8

// Ivy dynamic revision kinds (Renovate's ivy versioning, parseDynamicRevision).
const (
	// IvyLatest is `latest.<status>` (`latest.integration`, `latest.release`) or `latest`.
	IvyLatest IvyKind = iota + 1
	// IvySubRevision is `<version>.+` (`1.+`, `1.2.+`).
	IvySubRevision
	// IvyRange is one Maven range restriction, with Ivy's brackets also accepted: `]` opens
	// and `[` closes an exclusive bound (`]1.0,2.0[` is `(1.0,2.0)`).
	IvyRange
)

// IvyRevision is an Ivy dynamic revision as Renovate's ivy versioning reads it: the latest
// revision of a status, a sub-revision of a version prefix, or a version range with a single
// restriction. Plain versions are not revisions; their scheme is `ivy` (Maven's order).
type IvyRevision struct {
	Kind IvyKind
	// Prefix is the version a sub-revision extends (`1.2` of `1.2.+`).
	Prefix string
	// Range is the restriction of a range, in Maven's brackets.
	Range MavenRange
	text  string // as written
}

// IsIvyDynamic reports whether s is written as an Ivy dynamic revision rather than a fixed
// version: `latest.*`, a `+` or a range bracket. Not every such string parses
// (ParseIvyRevision): Ivy's `1.0+` and `+` and ranges with several restrictions do not.
func IsIvyDynamic(s string) bool {
	return isIvyLatest(s) || strings.Contains(s, "+") || s != "" && strings.IndexByte("[]()", s[0]) >= 0
}

func isIvyLatest(s string) bool {
	l := strings.ToLower(s)
	return l == "latest" || strings.HasPrefix(l, "latest.")
}

// ParseIvyRevision parses an Ivy dynamic revision as Renovate's ivy versioning does: `latest`
// or `latest.<status>`, `<version>.+` with a Maven version before `.+`, or a Maven range of
// one restriction whose brackets may also be Ivy's `]1.0,2.0[`.
func ParseIvyRevision(s string) (IvyRevision, bool) {
	switch {
	case isIvyLatest(s):
		return IvyRevision{Kind: IvyLatest, text: s}, true
	case strings.HasSuffix(s, ".+"):
		p := strings.TrimSuffix(s, ".+")
		if !validMaven(p) {
			return IvyRevision{}, false
		}
		return IvyRevision{Kind: IvySubRevision, Prefix: p, text: s}, true
	}
	r, err := ParseMavenRange(mavenBrackets(s))
	if err != nil || len(r) != 1 {
		return IvyRevision{}, false
	}
	return IvyRevision{Kind: IvyRange, Range: r, text: s}, true
}

// mavenBrackets rewrites Ivy's exclusive brackets of s (a leading `]`, a trailing `[`) to
// Maven's `(` and `)`; offsets stay the same.
func mavenBrackets(s string) string {
	if s == "" || s[0] != ']' && s[len(s)-1] != '[' {
		return s
	}
	b := []byte(s)
	if b[0] == ']' {
		b[0] = '('
	}
	if b[len(b)-1] == '[' {
		b[len(b)-1] = ')'
	}
	return string(b)
}

// Exact reports whether the revision is a range of a single version (`[1.0]`); its version
// is Range[0].Min at Range[0].MinSpan.
func (r IvyRevision) Exact() bool {
	return r.Kind == IvyRange && r.Range[0].Exact()
}

// Anchor is the version candidates are ranked from: the prefix of a sub-revision, the lower
// bound of a range (else its upper bound); "" for latest.
func (r IvyRevision) Anchor() string {
	switch r.Kind {
	case IvySubRevision:
		return r.Prefix
	case IvyRange:
		return cmp.Or(r.Range[0].Min, r.Range[0].Max)
	}
	return ""
}

// Admits reports whether Ivy resolves the revision to the version v: latest admits every
// version, a sub-revision those starting with its prefix and a dot (`1.+`: 1.0, 1.5.2, not
// 10.0), a range those it contains.
func (r IvyRevision) Admits(v string) bool {
	if !validMaven(v) {
		return false
	}
	switch r.Kind {
	case IvyLatest:
		return true
	case IvySubRevision:
		return strings.HasPrefix(v, r.Prefix+".")
	case IvyRange:
		return r.Range.Admits(v)
	}
	return false
}

// Extend returns the revision rewritten to admit v as Renovate's ivy getNewValue does: a
// range is moved like a Maven range (MavenRange.Extend), keeping its brackets; latest and
// sub-revisions are kept (false), as Ivy resolves them itself. false too when the revision
// admits v already.
func (r IvyRevision) Extend(v string) (string, bool) {
	if r.Kind != IvyRange {
		return "", false
	}
	out, ok := r.Range.Extend(mavenBrackets(r.text), v)
	if !ok {
		return "", false
	}
	// Only the bounds change: the brackets keep their offsets from either end.
	b := []byte(out)
	b[0], b[len(b)-1] = r.text[0], r.text[len(r.text)-1]
	return string(b), true
}
