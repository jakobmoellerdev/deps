package dockerds

import (
	"net/http"
	"strings"
)

// linkFixer repairs pagination Link headers that omit the RFC 8288 angle brackets
// (docker.gitea.com sends `Link: https://…/tags/list?n=50&last=x; rel=next`), which nextLink
// rejects. Well-formed and absent headers pass through without allocation.
type linkFixer struct{ next http.RoundTripper }

func (t linkFixer) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	vals := resp.Header["Link"]
	if len(vals) == 1 {
		if fixed, ok := fixLink(vals[0]); ok {
			vals[0] = fixed
		}
	}
	return resp, err
}

// fixLink rewrites `<url>[; params]` without brackets into `<url>; rel="next"`. It only
// touches values starting with http(s):// or / that contain no '<'.
func fixLink(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if strings.IndexByte(v, '<') >= 0 {
		return "", false
	}
	if !strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		return "", false
	}
	u := v
	if i := strings.IndexByte(v, ';'); i >= 0 {
		u = strings.TrimSpace(v[:i])
	}
	return "<" + u + `>; rel="next"`, true
}
