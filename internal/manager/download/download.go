// Package download recognizes the version of a download URL that managers pin with a
// checksum (Homebrew formulae, Nix fixed-output fetches): GitHub, Gitea, Forgejo and GitLab
// tag archives and release assets, and npm registry tarballs. Locate turns such a URL into the
// lookup of a dep, with the spans of its version and the artifact template its new checksum is
// computed from; Follow derives the deps of statements (mirrors, version attributes) that move
// with it.
package download

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

var (
	// forgeArchiveRe is a GitHub, Gitea or Forgejo tag archive (group 1 the host, 2
	// owner/repo, 3 the tag), forgeReleaseRe a release asset of one (1 the host, 2 owner/repo,
	// 3 the tag, 4 the asset). gitlabArchiveRe is a GitLab tag archive (1 the host, 2 the
	// project path, 3 the tag), gitlabReleaseRe a GitLab release asset link (1 the host, 2 the
	// project path, 3 the tag). npmRe is an npm registry tarball (1 the package, 2 its unscoped
	// name, 3 the file name without `.tgz`).
	forgeArchiveRe  = regexp.MustCompile(`^https://([^/]+)/([\w.-]+/[\w.-]+)/archive/(?:refs/tags/)?(.+?)\.(?:tar\.gz|tgz|zip)$`)
	forgeReleaseRe  = regexp.MustCompile(`^https://([^/]+)/([\w.-]+/[\w.-]+)/releases/download/([^/]+)/([^/]+)$`)
	gitlabArchiveRe = regexp.MustCompile(`^https://([^/]+)/((?:[\w.-]+/)+[\w.-]+)/-/archive/([^/]+)/[^/]+\.(?:tar\.gz|tar\.bz2|tar|tgz|zip)$`)
	gitlabReleaseRe = regexp.MustCompile(`^https://([^/]+)/((?:[\w.-]+/)+[\w.-]+)/-/releases/([^/]+)/downloads/.+$`)
	npmRe           = regexp.MustCompile(`^https://registry\.npmjs\.org/((?:@[^/]+/)?([^/@]+))/-/([^/]+)\.tgz$`)
)

// Locate sets the datasource, versioning, name, source, extractVersion, version and version
// spans (RefSpan, VersionSpan, InputSpan: one to three) of d from the URL u at offset off of
// the file, and the artifact template of its checksum, and makes d Together (the downloads of
// a file with d's name move only as a whole); false = not a supported URL.
// The version is the tag after its prefix (`v1.2.3`, `jq-1.8.2`, `libpng16-1.6.43`), which
// the dep's extractVersion maps back; every occurrence of it in the tag and asset is replaced.
// An npm tarball is named `<unscoped name>-<version>.tgz`.
func Locate(d *dep.Dep, u string, off int, env manager.Env) bool {
	var tagAt int // offset in u of the version search
	if m := npmRe.FindStringSubmatchIndex(u); m != nil {
		file, base := u[m[6]:m[7]], u[m[4]:m[5]]
		if len(file) <= len(base)+1 || !strings.HasPrefix(file, base) || file[len(base)] != '-' || !isDigit(file[len(base)+1]) {
			return false
		}
		d.Datasource, d.Versioning, d.Name = "npm", "semver", u[m[2]:m[3]]
		d.Version, tagAt = file[len(base)+1:], m[6]+len(base)+1
	} else {
		dl, ok := forgeDownload(u, env)
		if !ok {
			return false
		}
		d.Datasource, d.Versioning, d.Name, d.Source = dl.ds, "loose", dl.name, dl.source
		tag := u[dl.tagStart:dl.tagEnd]
		i := versionStart(tag)
		if i < 0 {
			return false
		}
		if i > 0 {
			d.ExtractVersion = "^" + regexp.QuoteMeta(tag[:i]) + `(?<version>\d.*)$`
		}
		d.Version, tagAt = tag[i:], dl.tagStart+i
	}
	spans := occurrences(u, d.Version, tagAt)
	if len(spans) == 0 || len(spans) > 3 {
		return false
	}
	tmpl := u
	for _, at := range slices.Backward(spans) {
		tmpl = tmpl[:at] + "{{version}}" + tmpl[at+len(d.Version):]
	}
	d.ChecksumSource, d.Together = &dep.ChecksumSource{Artifact: tmpl}, true
	setSpans(d, spans, off, len(d.Version))
	return true
}

// Follow returns the dep of type depType of a statement whose string value is at offset off
// (line line) of the file, a mirror URL or a version, that moves with the download of dep d:
// d's manager, file and lookup, with spans at the occurrences of d's version in value (after
// the host of a URL; one to three of them, not inside a longer version), Together with d.
// false when d is skipped or value has no such occurrences.
func Follow(d dep.Dep, depType, value string, off, line int) (dep.Dep, bool) {
	if d.Skip != "" {
		return dep.Dep{}, false
	}
	from := 0
	if i := strings.Index(value, "://"); i >= 0 {
		if j := strings.IndexByte(value[i+3:], '/'); j >= 0 {
			from = i + 3 + j
		}
	}
	var spans []int
	for _, at := range occurrences(value, d.Version, from) {
		if at == 0 || !isDigit(value[at-1]) && value[at-1] != '.' {
			spans = append(spans, at)
		}
	}
	if len(spans) == 0 || len(spans) > 3 {
		return dep.Dep{}, false
	}
	f := dep.Dep{
		Manager: d.Manager, DepType: depType, File: d.File, Line: line, Datasource: d.Datasource, Versioning: d.Versioning,
		Name: d.Name, Source: d.Source, ExtractVersion: d.ExtractVersion, Version: d.Version, Together: true,
	}
	setSpans(&f, spans, off, len(d.Version))
	return f, true
}

// setSpans sets the version spans of d to the offsets spans (at most three) relative to off.
func setSpans(d *dep.Dep, spans []int, off, n int) {
	targets := [...]*dep.Span{&d.RefSpan, &d.VersionSpan, &d.InputSpan}
	for i, at := range spans {
		*targets[i] = dep.Span{Start: off + at, End: off + at + n}
	}
}

// forgeLookup is the lookup of a forge download URL: datasource, name, source and the
// offsets of the tag in the URL.
type forgeLookup struct {
	ds, name, source string
	tagStart, tagEnd int
}

// forgeDownload recognizes tag archives and release assets: of github.com; of Gitea and
// Forgejo on the checkout's forge (env.ForgeURL), gitea.com, codeberg.org and
// code.forgejo.org; and of GitLab on any host (its `/-/` paths). Archives are looked up as
// the repository's tags (git-tags), assets as its releases.
func forgeDownload(u string, env manager.Env) (forgeLookup, bool) {
	if m := gitlabArchiveRe.FindStringSubmatchIndex(u); m != nil {
		host, path := u[m[2]:m[3]], u[m[4]:m[5]]
		return forgeLookup{"git-tags", path, "https://" + host + "/" + path, m[6], m[7]}, true
	}
	if m := gitlabReleaseRe.FindStringSubmatchIndex(u); m != nil {
		return forgeLookup{forge.GitLabReleases, u[m[4]:m[5]], "https://" + u[m[2]:m[3]], m[6], m[7]}, true
	}
	if m := forgeArchiveRe.FindStringSubmatchIndex(u); m != nil {
		host, repo := u[m[2]:m[3]], u[m[4]:m[5]]
		if _, ok := forgeKind(host, env); !ok {
			return forgeLookup{}, false
		}
		return forgeLookup{"git-tags", repo, "https://" + host + "/" + repo, m[6], m[7]}, true
	}
	if m := forgeReleaseRe.FindStringSubmatchIndex(u); m != nil {
		host, repo := u[m[2]:m[3]], u[m[4]:m[5]]
		ds, ok := forgeKind(host, env)
		if !ok {
			return forgeLookup{}, false
		}
		source := "https://" + host
		if ds == "github-releases" {
			source += "/" + repo
		}
		return forgeLookup{ds, repo, source, m[6], m[7]}, true
	}
	return forgeLookup{}, false
}

// forgeKind is the release datasource of a GitHub, Gitea or Forgejo host; false for other
// hosts, whose `/<owner>/<repo>/archive/…` URLs may be anything.
func forgeKind(host string, env manager.Env) (string, bool) {
	switch strings.ToLower(host) {
	case "github.com":
		return "github-releases", true
	case "codeberg.org", "code.forgejo.org":
		return forge.ForgejoReleases, true
	case "gitea.com":
		return forge.GiteaReleases, true
	}
	if f, err := url.Parse(env.ForgeURL); err == nil && f.Host != "" && strings.EqualFold(f.Host, host) {
		return forge.GiteaReleases, true
	}
	return "", false
}

// versionStart is the offset of the version in tag, -1 for none or a commit hash: the first
// digit at a boundary (the tag start, after `-` or `_`, or after a `v` at the tag start or
// after `-` or `_`), so digits in a name prefix (`wget2-`, `k3s-v`) are skipped; without
// one, the first digit (`r1234`).
func versionStart(tag string) int {
	if manager.IsHex(tag, 40, 64) {
		return -1
	}
	for i := range len(tag) {
		if !isDigit(tag[i]) {
			continue
		}
		if i == 0 || isSep(tag[i-1]) || (tag[i-1] == 'v' || tag[i-1] == 'V') && (i == 1 || isSep(tag[i-2])) {
			return i
		}
	}
	return strings.IndexAny(tag, "0123456789")
}

func isSep(c byte) bool { return c == '-' || c == '_' }

// occurrences are the offsets in u, from the first at from, of v not followed by more
// version (a digit, or a dot and a digit).
func occurrences(u, v string, from int) []int {
	var out []int
	for i := from; ; {
		j := strings.Index(u[i:], v)
		if j < 0 {
			return out
		}
		i += j
		end := i + len(v)
		if !(end < len(u) && isDigit(u[end])) && !(end+1 < len(u) && u[end] == '.' && isDigit(u[end+1])) {
			out = append(out, i)
		}
		i = end
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
