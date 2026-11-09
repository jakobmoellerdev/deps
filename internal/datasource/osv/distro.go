package osv

import (
	"cmp"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// ecoRedHat is the ecosystem Red Hat Enterprise Linux advisories are queried under: OSV lists
// them under per-stream ecosystems ("Red Hat:enterprise_linux:9::appstream") that a query
// cannot name, so the stream is selected locally.
const ecoRedHat = "Red Hat"

// debianSuites maps Debian codenames to their OSV ecosystem. Aliases (stable, testing,
// unstable, sid) are left out: they move to another release over time.
var debianSuites = map[string]string{
	"buster":   "Debian:10",
	"bullseye": "Debian:11",
	"bookworm": "Debian:12",
	"trixie":   "Debian:13",
	"forky":    "Debian:14",
}

// ubuntuSuites maps the codenames of Ubuntu releases with standard support to their OSV
// ecosystem version. LTS releases carry the ":LTS" suffix OSV writes.
var ubuntuSuites = map[string]string{
	"focal":    "20.04:LTS",
	"jammy":    "22.04:LTS",
	"noble":    "24.04:LTS",
	"oracular": "24.10",
	"plucky":   "25.04",
	"questing": "25.10",
	"resolute": "26.04:LTS",
}

var (
	alpineBranch = regexp.MustCompile(`^v\d+\.\d+$`)
	rhelSegment  = regexp.MustCompile(`^(?:rhel|ubi)(\d+)$`)
	majorSegment = regexp.MustCompile(`^(\d+)(?:\.\d+)?$`)
	leapSegment  = regexp.MustCompile(`^15\.\d+$`)
)

// distroTarget returns the target of a deb, apk or rpm dep whose registry URL names a
// release OSV has an ecosystem for, or nil. The first of the dep's whitespace-separated
// registry URLs that names one decides.
func distroTarget(d dep.Dep) *target {
	v := cmp.Or(d.Locked, d.Version)
	if d.Name == "" || v == "" {
		return nil
	}
	for r := range strings.FieldsSeq(d.Source) {
		u, err := url.Parse(r)
		if err != nil {
			continue
		}
		var eco string
		switch d.Datasource {
		case "deb":
			eco = debEcosystem(u)
		case "apk":
			eco = apkEcosystem(u)
		case "rpm":
			var stream string
			if eco, stream = rpmEcosystem(u); stream != "" {
				t := newTarget(query{Package: pkg{Name: d.Name, Ecosystem: eco}, Version: v})
				t.distro, t.stream = true, stream
				return t
			}
		}
		if eco != "" {
			t := newTarget(query{Package: pkg{Name: d.Name, Ecosystem: eco}, Version: v})
			t.distro = true
			return t
		}
	}
	return nil
}

// debEcosystem maps the suite of a deb registry URL (`?suite=bookworm-security`; Renovate's
// `release` alias) to Debian:<n>, Ubuntu:<version> or, on esm.ubuntu.com, Ubuntu:Pro:<version>.
func debEcosystem(u *url.URL) string {
	q := u.Query()
	suite := cmp.Or(q.Get("suite"), q.Get("release"))
	suite, _, _ = strings.Cut(suite, "-") // bookworm-security, jammy-updates, jammy-infra-security
	if eco, ok := debianSuites[suite]; ok {
		return eco
	}
	if ver, ok := ubuntuSuites[suite]; ok {
		if strings.EqualFold(u.Hostname(), "esm.ubuntu.com") {
			return "Ubuntu:Pro:" + ver
		}
		return "Ubuntu:" + ver
	}
	return ""
}

// apkEcosystem maps an apk registry URL to Wolfi (packages.wolfi.dev), Chainguard
// (packages.cgr.dev) or Alpine:v<x.y>: the `branch` parameter, else a `v<x.y>` path segment
// (`https://dl-cdn.alpinelinux.org/alpine/v3.20/main/x86_64/APKINDEX.tar.gz`). edge has no
// ecosystem.
func apkEcosystem(u *url.URL) string {
	switch strings.ToLower(u.Hostname()) {
	case "packages.wolfi.dev":
		return "Wolfi"
	case "packages.cgr.dev":
		return "Chainguard"
	}
	if b := u.Query().Get("branch"); b != "" {
		if alpineBranch.MatchString(b) {
			return "Alpine:" + b
		}
		return ""
	}
	for s := range strings.SplitSeq(u.Path, "/") {
		if alpineBranch.MatchString(s) {
			return "Alpine:" + s
		}
	}
	return ""
}

// rpmEcosystem maps the path of an rpm repository URL, on any mirror, to Rocky Linux:<major>
// (`/rocky/9/BaseOS/…`, `/rocky/9.4/…`), AlmaLinux:<major> (`/almalinux/9/…`),
// openSUSE:Leap <x.y> (`/leap/15.6/…`) or Red Hat (`/rhel9/…`, `/ubi9/…`). For Red Hat it also
// returns the ecosystem prefix of the release's streams ("Red Hat:enterprise_linux:9:").
func rpmEcosystem(u *url.URL) (eco, stream string) {
	segs := strings.Split(strings.ToLower(u.Path), "/")
	for i, s := range segs {
		next := ""
		if i+1 < len(segs) {
			next = segs[i+1]
		}
		switch s {
		case "rocky", "almalinux":
			if m := majorSegment.FindStringSubmatch(next); m != nil {
				if s == "rocky" {
					return "Rocky Linux:" + m[1], ""
				}
				return "AlmaLinux:" + m[1], ""
			}
		case "leap":
			if leapSegment.MatchString(next) {
				return "openSUSE:Leap " + next, ""
			}
		default:
			if m := rhelSegment.FindStringSubmatch(s); m != nil {
				return ecoRedHat, ecoRedHat + ":enterprise_linux:" + m[1] + ":"
			}
		}
	}
	return "", ""
}

// streamEntries returns the entries of v for the Red Hat package name in the release stream.
func (v *vuln) streamEntries(name, stream string) []affected {
	var out []affected
	for _, a := range v.Affected {
		if a.Package.Name == name && strings.HasPrefix(a.Package.Ecosystem, stream) {
			out = append(out, a)
		}
	}
	return out
}

// rpmAffected evaluates the ECOSYSTEM ranges of entries against the dep version with RPM
// ordering, following the OSV spec's evaluation algorithm. OSV matches a "Red Hat" query
// against every release, so a release stream's ranges are checked here.
func (t *target) rpmAffected(entries []affected) bool {
	s, _ := versioning.Get("rpm")
	v := t.qs[0].Version
	if !s.Valid(v) {
		return false
	}
	at := func(bound string, ok func(int) bool) bool {
		return s.Valid(bound) && ok(s.Compare(v, bound))
	}
	for _, a := range entries {
		for _, r := range a.Ranges {
			if r.Type != "ECOSYSTEM" {
				continue
			}
			in := false
			for _, e := range r.Events {
				switch {
				case e.Introduced != "":
					if e.Introduced == "0" || at(e.Introduced, func(c int) bool { return c >= 0 }) {
						in = true
					}
				case e.Fixed != "":
					if at(e.Fixed, func(c int) bool { return c >= 0 }) {
						in = false
					}
				case e.LastAffected != "":
					if at(e.LastAffected, func(c int) bool { return c > 0 }) {
						in = false
					}
				}
			}
			if in {
				return true
			}
		}
	}
	return false
}

// distroFixed returns the fixed versions as published, without the zero epoch RPM advisories
// write ("0:7.76.1-26.el9_3.2" → "7.76.1-26.el9_3.2"), which is the same version.
func distroFixed(entries []affected) []string {
	var out []string
	fixedEvents(entries, func(f string) {
		if f = strings.TrimPrefix(f, "0:"); !slices.Contains(out, f) {
			out = append(out, f)
		}
	})
	return out
}
