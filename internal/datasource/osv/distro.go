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
// cannot name, so the stream is selected locally. SUSE Linux Enterprise is the same shape
// ("SUSE:Linux Enterprise Server 15 SP5-LTSS"; a query naming the product returns nothing).
const (
	ecoRedHat = "Red Hat"
	ecoSUSE   = "SUSE"
)

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
	leapSegment  = regexp.MustCompile(`^\d+\.\d+$`)
	eulerSegment = regexp.MustCompile(`(?i)^openeuler-(\d+\.\d+(?:-[\w.-]+)?)$`)
	sleSegment   = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)(?:-(sp\d+))?$`)
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
				t.distro, t.stream, t.scheme, t.local = true, stream, "rpm", true
				return t
			}
		}
		if eco != "" {
			t := newTarget(query{Package: pkg{Name: d.Name, Ecosystem: eco}, Version: v})
			t.distro, t.scheme = true, d.Datasource // "deb", "apk" and "rpm" are scheme names
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
// openSUSE:Leap <x.y> (`/leap/15.6/…`), openSUSE:Tumbleweed (`/tumbleweed/…`), Azure
// Linux:<major> (`/azurelinux/3.0/…`, `/cbl-mariner/2.0/…`), openEuler:<release>
// (`/openEuler-24.03-LTS-SP1/…`), Mageia:<major> (`/mageia/distrib/9/…`) or Red Hat
// (`/rhel9/…`, `/ubi9/…`) and SUSE (`/SUSE/Updates/SLE-Server/15-SP5/…`). For Red Hat and
// SUSE it also returns the ecosystem prefix of the release's streams ("Red
// Hat:enterprise_linux:9:", "SUSE:Linux Enterprise Server 15 SP5").
func rpmEcosystem(u *url.URL) (eco, stream string) {
	segs := strings.Split(u.Path, "/")
	lower := strings.Split(strings.ToLower(u.Path), "/")
	for i, s := range lower {
		next := ""
		if i+1 < len(lower) {
			next = lower[i+1]
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
		case "tumbleweed":
			return "openSUSE:Tumbleweed", ""
		case "azurelinux", "cbl-mariner":
			// The repositories are versioned "3.0"/"2.0", the ecosystems by major ("Azure Linux:3").
			if m := majorSegment.FindStringSubmatch(next); m != nil {
				return "Azure Linux:" + m[1], ""
			}
		case "distrib":
			// Mageia mirrors: /mageia/distrib/9/x86_64/media/core/updates/.
			if i > 0 && lower[i-1] == "mageia" {
				if m := majorSegment.FindStringSubmatch(next); m != nil {
					return "Mageia:" + m[1], ""
				}
			}
		default:
			if m := rhelSegment.FindStringSubmatch(s); m != nil {
				return ecoRedHat, ecoRedHat + ":enterprise_linux:" + m[1] + ":"
			}
			if m := eulerSegment.FindStringSubmatch(segs[i]); m != nil {
				return "openEuler:" + m[1], ""
			}
			// updates.suse.com: /SUSE/Updates/SLE-Server/15-SP5/x86_64/update/,
			// /SUSE/Updates/SLE-Module-Basesystem/15-SP6/…, /SUSE/Updates/SLE-Micro/5.5/….
			if p := sleProduct(s); p != "" {
				if m := sleSegment.FindStringSubmatch(next); m != nil {
					stream := ecoSUSE + ":" + p + " " + m[1]
					if m[2] != "" {
						stream += " " + strings.ToUpper(m[2])
					}
					return ecoSUSE, stream
				}
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

// sleProduct maps a SUSE Linux Enterprise repository segment to the product name OSV writes
// its ecosystems with ("sle-server" → "Linux Enterprise Server", "sle-module-package-hub" →
// "Linux Enterprise Module for Package Hub"), "" when the segment names no SLE product.
func sleProduct(seg string) string {
	switch {
	case seg == "sle-server":
		return "Linux Enterprise Server"
	case seg == "sle-micro":
		return "Linux Enterprise Micro"
	case strings.HasPrefix(seg, "sle-module-"):
		words := strings.Split(strings.TrimPrefix(seg, "sle-module-"), "-")
		for i, w := range words {
			if w != "" {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		return "Linux Enterprise Module for " + strings.Join(words, " ")
	}
	return ""
}

// schemeAffected evaluates the ECOSYSTEM and SEMVER ranges of entries against the dep version
// with the target's versioning scheme (t.scheme), following the OSV spec's evaluation
// algorithm. It is used where OSV does not evaluate the version server-side: a "Red Hat" or
// "SUSE" query matches every release (so a release stream's ranges are checked here, with RPM
// ordering), and Julia and SwiftURL queries return every advisory of the package.
func (t *target) schemeAffected(entries []affected) bool {
	s, ok := t.rangeScheme()
	v := t.qs[0].Version
	if !ok || !s.Valid(v) {
		return false
	}
	return schemeAffectedAt(entries, v, s)
}

// schemeAffectedAt reports whether the ECOSYSTEM and SEMVER ranges of entries cover the valid
// version v, ordered by s.
func schemeAffectedAt(entries []affected, v string, s versioning.Scheme) bool {
	for _, a := range entries {
		for _, r := range a.Ranges {
			if r.Type != "ECOSYSTEM" && r.Type != "SEMVER" {
				continue
			}
			if inEvents(r.Events, v, func(b string) (string, bool) { return b, s.Valid(b) }, s.Compare) {
				return true
			}
		}
	}
	return false
}

// distroFixed returns the fixed versions as published, without the zero epoch RPM advisories
// write ("0:7.76.1-26.el9_3.2" → "7.76.1-26.el9_3.2"), which is the same version.
func (t *target) distroFixed(entries []affected) []string {
	var out []string
	t.fixedEvents(entries, func(f string) {
		if f = strings.TrimPrefix(f, "0:"); !slices.Contains(out, f) {
			out = append(out, f)
		}
	})
	return out
}
