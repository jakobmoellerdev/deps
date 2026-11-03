package actions

import (
	"net/url"
	"strings"
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
		return Uses{Skip: "docker reference (not supported yet)"}
	}
	at := strings.LastIndexByte(value, '@')
	if at < 0 {
		return Uses{Skip: "unrecognized uses"}
	}
	p, ref := value[:at], value[at+1:]
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
