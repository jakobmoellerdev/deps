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

// versionsRe finds the versions marker of a major bucket's PR body (versionsMarker).
var versionsRe = regexp.MustCompile(`<!-- deps:versions=(\S+(?: \S+)*) -->`)

// securitySuffix ends the title of a PR with security updates (engine.GroupUpdates).
const securitySuffix = " [security]"

// majorBucket reports whether ups are the updates of a major PR bucket: "major", or "v<N>"
// per major with separateMultipleMajor (the other buckets are "", "minor" and "patch").
func majorBucket(ups []dep.Update) bool {
	if len(ups) == 0 {
		return false
	}
	b := ups[0].Bucket
	return b == "major" || strings.HasPrefix(b, "v")
}

// versionsMarker renders the marker that records the target version of every dependency of
// a major bucket's PR, "<!-- deps:versions=<name>=<version> ... -->" with names and versions
// query-escaped, so a closed PR suppresses later versions of the same majors (closedFor).
// Other buckets get none ("").
func versionsMarker(ups []dep.Update) string {
	if !majorBucket(ups) {
		return ""
	}
	var entries []string
	for _, u := range ups {
		if u.NewVersion == "" {
			continue
		}
		if e := url.QueryEscape(u.Dep.Name) + "=" + url.QueryEscape(u.NewVersion); !slices.Contains(entries, e) {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return ""
	}
	return "<!-- deps:versions=" + strings.Join(entries, " ") + " -->"
}

// bodyVersions returns the versions recorded by the versions marker of body, by dependency
// name; nil without a marker.
func bodyVersions(body string) map[string][]string {
	m := versionsRe.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	out := map[string][]string{}
	for e := range strings.SplitSeq(m[1], " ") {
		k, v, ok := strings.Cut(e, "=")
		name, errK := url.QueryUnescape(k)
		ver, errV := url.QueryUnescape(v)
		if ok && errK == nil && errV == nil {
			out[name] = append(out[name], ver)
		}
	}
	return out
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
	title = strings.TrimSuffix(title, securitySuffix)
	i := strings.LastIndex(title, " to ")
	if i < 0 {
		return "", "", false
	}
	return title[:i], title[i+len(" to "):], true
}

// closedFor returns the closed unmerged PR of grp's branch that suppresses grp, with the skip
// status, or nil. Like Renovate, closing a PR ignores its version (StatusSkipClosed: the same
// title, which names the version, or the digest of a digest or pin update); closing a major
// bucket's PR ignores that major (StatusSkipClosedMajor): every dependency of grp targets the
// major its closed PR recorded (versionsMarker; PRs without the marker: the version of the
// title, when grp's title differs only in it). Updates fixing advisories are not suppressed
// by a closed PR without them (its title lacks " [security]"). An exact match wins.
func closedFor(closed []platform.PR, grp engine.Group) (*platform.PR, string) {
	major := majorBucket(grp.Updates)
	security := slices.ContainsFunc(grp.Updates, func(u dep.Update) bool { return len(u.Advisories) > 0 })
	var found *platform.PR
	for i := range closed {
		c := &closed[i]
		switch {
		case c.Head != grp.Branch || c.Merged:
			continue
		case !major:
			if c.Title == grp.Title {
				return c, StatusSkipClosed
			}
			continue
		case security && !strings.HasSuffix(c.Title, securitySuffix):
			continue
		}
		exact, same := closedMajor(*c, grp)
		if exact {
			return c, StatusSkipClosed
		}
		if same && found == nil {
			found = c
		}
	}
	if found != nil {
		return found, StatusSkipClosedMajor
	}
	return nil, ""
}

// closedMajor compares the closed PR c of a major bucket with grp: exact when c targeted the
// versions of grp, same when it targeted their majors.
func closedMajor(c platform.PR, grp engine.Group) (exact, same bool) {
	versions := bodyVersions(c.Body)
	if versions == nil {
		// A PR without the marker: its title names the version, if any.
		if c.Title == grp.Title {
			return true, true
		}
		stem, v, ok := titleVersion(c.Title)
		newStem, _, newOK := titleVersion(grp.Title)
		return false, ok && newOK && stem == newStem && !slices.ContainsFunc(grp.Updates, func(u dep.Update) bool { return !sameMajor(u, v) })
	}
	exact, same = true, true
	for _, u := range grp.Updates {
		vs := versions[u.Dep.Name]
		exact = exact && slices.Contains(vs, u.NewVersion)
		same = same && slices.ContainsFunc(vs, func(v string) bool { return sameMajor(u, v) })
	}
	return exact, same
}
