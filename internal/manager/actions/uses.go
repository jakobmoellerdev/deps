package actions

import (
	"net/url"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Uses is a parsed `uses:` value.
type Uses struct {
	Name   string // owner/repo[/sub]
	Source string // https://<host>/<owner>/<repo>
	Ref    string // text after the last '@'
	Skip   string // non-empty = not updatable
}

// ParseUses parses a `uses:` value; owner/repo refs resolve against defaultHost.
func ParseUses(value, defaultHost string) Uses {
	switch {
	case strings.HasPrefix(value, "./"), strings.HasPrefix(value, "$/"):
		return Uses{Skip: "local action"}
	case strings.HasPrefix(value, "docker://"):
		return Uses{Skip: "docker reference (docker manager)"}
	}
	p, ref, found := strings.CutLast(value, "@")
	if !found {
		return Uses{Skip: "unrecognized uses"}
	}
	host := defaultHost
	if strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "http://") {
		u, err := url.Parse(p)
		if err != nil || u.Host == "" {
			return Uses{Skip: "unrecognized uses"}
		}
		host, p = u.Host, strings.TrimPrefix(u.Path, "/")
	}
	owner, rest, ok := strings.Cut(p, "/")
	if !ok || owner == "" || ref == "" {
		return Uses{Skip: "unrecognized uses"}
	}
	repo, _, _ := strings.Cut(rest, "/")
	if repo == "" {
		return Uses{Skip: "unrecognized uses"}
	}
	return Uses{Name: p, Source: "https://" + host + "/" + owner + "/" + repo, Ref: ref}
}

// subpath returns the path of an action in a subdirectory of its repository (`actions/x` of
// `owner/repo/actions/x`) and the path's last element (`x`); both "" at the repository root.
func subpath(name string) (sub, last string) {
	_, rest, _ := strings.Cut(name, "/")
	_, sub, _ = strings.Cut(rest, "/")
	if sub == "" {
		return "", ""
	}
	return sub, sub[strings.LastIndexByte(sub, '/')+1:]
}

// TagExtract returns the extractVersion expression of the per-action tags of an action in a
// subdirectory of a monorepo, `<path>/<version>` or `<last path element>/<version>`
// (grafana/shared-workflows tags actions/get-vault-secrets `get-vault-secrets/v1.3.1`), ""
// for an action at the repository root.
func TagExtract(name string) string {
	sub, last := subpath(name)
	switch {
	case sub == "":
		return ""
	case sub == last:
		return "^" + regexp.QuoteMeta(sub) + "/(?<version>.+)$"
	}
	return "^(?:" + regexp.QuoteMeta(sub) + "|" + regexp.QuoteMeta(last) + ")/(?<version>.+)$"
}

// tagPrefixLen returns the length of the per-action tag prefix (see TagExtract) that ref of
// action name starts with when the rest is a version tag, else 0.
func tagPrefixLen(name, ref string) int {
	sub, last := subpath(name)
	if sub == "" {
		return 0
	}
	for _, p := range [2]string{sub, last} {
		if len(ref) > len(p) && ref[len(p)] == '/' && strings.HasPrefix(ref, p) {
			if _, ok := versioning.ParseTag(ref[len(p)+1:]); ok {
				return len(p) + 1
			}
		}
	}
	return 0
}
