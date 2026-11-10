package bot

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// versionsRe finds the versions marker of a PR body (versionsMarker).
var versionsRe = regexp.MustCompile(`<!-- deps:versions=(\S+(?: \S+)*) -->`)

// majorBucket reports whether ups are the updates of a major PR bucket: "major", or "v<N>"
// per major with separate: majors (the other buckets are "", "minor" and "patch").
func majorBucket(ups []dep.Update) bool {
	if len(ups) == 0 {
		return false
	}
	b := ups[0].Bucket
	return b == "major" || strings.HasPrefix(b, "v")
}

// versionsMarker renders the marker that records the target of every dependency of a PR,
// "<!-- deps:versions=<name>=<version>[@<digest>] ... -->" with names, versions and digests
// query-escaped, so a closed PR suppresses the same targets, and in a major bucket later
// versions of the same majors (closedFor), whatever the PR titles say. Updates without a
// version or digest (lock file maintenance) get no entry, and "" means no entries.
func versionsMarker(ups []dep.Update) string {
	var entries []string
	for _, u := range ups {
		if u.NewVersion == "" && u.NewDigest == "" {
			continue
		}
		e := url.QueryEscape(u.Dep.Name) + "=" + url.QueryEscape(u.NewVersion)
		if u.NewDigest != "" {
			e += "@" + url.QueryEscape(u.NewDigest)
		}
		if !slices.Contains(entries, e) {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return ""
	}
	return "<!-- deps:versions=" + strings.Join(entries, " ") + " -->"
}

// target is a dependency's new version and digest ("" = none) in a versions marker.
type target struct{ version, digest string }

// bodyVersions returns the targets recorded by the versions marker of body, by dependency
// name; nil without a marker.
func bodyVersions(body string) map[string][]target {
	m := versionsRe.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	out := map[string][]target{}
	for e := range strings.SplitSeq(m[1], " ") {
		k, v, ok := strings.Cut(e, "=")
		v, d, _ := strings.Cut(v, "@")
		name, errK := url.QueryUnescape(k)
		ver, errV := url.QueryUnescape(v)
		dig, errD := url.QueryUnescape(d)
		if ok && errK == nil && errV == nil && errD == nil {
			out[name] = append(out[name], target{ver, dig})
		}
	}
	return out
}

// is reports whether t records u's target. A marker entry without a digest (one written
// before digests were recorded) matches any digest.
func (t target) is(u dep.Update) bool {
	return t.version == u.NewVersion && (t.digest == "" || t.digest == u.NewDigest)
}

// sameMajor reports whether version v is in the major of u's new version under u's
// versioning scheme. Versions the scheme cannot parse are a major apart.
func sameMajor(u dep.Update, v string) bool {
	if v == u.NewVersion {
		return true
	}
	s, ok := versioning.Get(u.Dep.Versioning)
	if !ok {
		return false
	}
	lo, hi := v, u.NewVersion
	if s.Compare(lo, hi) > 0 {
		lo, hi = hi, lo
	}
	return s.Kind(lo, hi) != dep.Major
}

// titleVersion splits a PR title "<stem> to <version>[ [security]]" into stem and version.
func titleVersion(title string) (stem, version string, ok bool) {
	title = strings.TrimSuffix(title, engine.SecuritySuffix)
	i := strings.LastIndex(title, " to ")
	if i < 0 {
		return "", "", false
	}
	return title[:i], title[i+len(" to "):], true
}

// closedFor returns the closed unmerged PR of grp's branch that suppresses grp, with the skip
// status, or nil. Like Renovate, closing a PR ignores its targets (StatusSkipClosed: every
// dependency of grp targets the version and digest the closed PR recorded in its versions
// marker); closing a major bucket's PR ignores that major (StatusSkipClosedMajor): every
// dependency of grp targets the major its closed PR recorded. The marker makes this
// independent of the titles (prTitle). Closed PRs without the marker (deps before it) match
// by title, which names the version or digest: the same title, and in a major bucket the
// same title up to the version, with that version in the same major. Updates fixing
// advisories are not suppressed by a closed PR without them (its title lacks
// engine.SecuritySuffix, which every security title ends with). An exact match wins.
func closedFor(closed []platform.PR, grp engine.Group) (*platform.PR, string) {
	major := majorBucket(grp.Updates)
	security := slices.ContainsFunc(grp.Updates, func(u dep.Update) bool { return len(u.Advisories) > 0 })
	var found *platform.PR
	for i := range closed {
		c := &closed[i]
		if c.Head != grp.Branch || c.Merged || security && !strings.HasSuffix(c.Title, engine.SecuritySuffix) {
			continue
		}
		exact, same := closedMatch(*c, grp, major)
		if exact {
			return c, StatusSkipClosed
		}
		if major && same && found == nil {
			found = c
		}
	}
	if found != nil {
		return found, StatusSkipClosedMajor
	}
	return nil, ""
}

// closedMatch compares the closed PR c with grp: exact when c targeted the versions (and
// digests) of grp, same when it targeted their majors (only computed in a major bucket).
func closedMatch(c platform.PR, grp engine.Group, major bool) (exact, same bool) {
	versions := bodyVersions(c.Body)
	if versions == nil {
		// A PR without the marker: its title names the version, if any.
		if c.Title == grp.Title {
			return true, true
		}
		if !major {
			return false, false
		}
		stem, v, ok := titleVersion(c.Title)
		newStem, _, newOK := titleVersion(grp.Title)
		return false, ok && newOK && stem == newStem && !slices.ContainsFunc(grp.Updates, func(u dep.Update) bool { return !sameMajor(u, v) })
	}
	exact, same = true, major
	for _, u := range grp.Updates {
		ts := versions[u.Dep.Name]
		exact = exact && slices.ContainsFunc(ts, func(t target) bool { return t.is(u) })
		same = same && slices.ContainsFunc(ts, func(t target) bool { return sameMajor(u, t.version) })
	}
	return exact, same
}

// declineReplacements adds the usual updates (dep.Update.Declined) of every replacement whose
// PR was closed without merging (closedFor; no open PR on its branch, the dashboard's recreate
// checkbox not ticked): closing a replacement declines the move, not the dep's updates. The
// replacement stays, so its group is reported as skipped for the closed PR and the dashboard
// can recreate it. Order is kept, each fallback right after its replacement.
func declineReplacements(ups []dep.Update, open, closed []platform.PR, prefix string, dash *dashboard) []dep.Update {
	if !slices.ContainsFunc(ups, func(u dep.Update) bool { return len(u.Declined) > 0 }) {
		return ups
	}
	type key struct {
		file    string
		line    int
		name    string
		newName string
	}
	declined := map[key]bool{}
	for _, g := range engine.GroupUpdates(ups, prefix) {
		if prOf(open, g.Branch) != nil || dash.force(g.Branch).recreate {
			continue
		}
		if c, _ := closedFor(closed, g); c == nil {
			continue
		}
		for _, u := range g.Updates {
			if len(u.Declined) > 0 {
				declined[key{u.Dep.File, u.Dep.Line, u.Dep.Name, u.NewName}] = true
			}
		}
	}
	if len(declined) == 0 {
		return ups
	}
	out := make([]dep.Update, 0, len(ups))
	for _, u := range ups {
		out = append(out, u)
		if declined[key{u.Dep.File, u.Dep.Line, u.Dep.Name, u.NewName}] {
			out = append(out, u.Declined...)
		}
	}
	return out
}
