package dockerds

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// TestReleasesBracketlessLink pages through a registry that, like docker.gitea.com, sends
// `Link: <url>; rel=next` without the angle brackets.
func TestReleasesBracketlessLink(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
		case r.URL.Path == "/v2/img/tags/list" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", fmt.Sprintf("%s/v2/img/tags/list?n=2&last=1.1; rel=next", srv.URL))
			fmt.Fprint(w, `{"name":"img","tags":["1.0","1.1"]}`)
		case r.URL.Path == "/v2/img/tags/list":
			fmt.Fprint(w, `{"name":"img","tags":["1.2"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	rels, err := New(srv.Client(), nil).Releases(t.Context(), dep.Dep{Name: host + "/img"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rels {
		got = append(got, r.Version)
	}
	if strings.Join(got, ",") != "1.0,1.1,1.2" {
		t.Errorf("tags = %v", got)
	}
}

func TestFixLink(t *testing.T) {
	for in, want := range map[string]string{
		"https://r/v2/x/tags/list?last=a; rel=next": `<https://r/v2/x/tags/list?last=a>; rel="next"`,
		"/v2/x/tags/list?last=a":                    `</v2/x/tags/list?last=a>; rel="next"`,
		`</v2/x>; rel="next"`:                       "",
		"garbage":                                   "",
	} {
		got, ok := fixLink(in)
		if ok != (want != "") || got != want {
			t.Errorf("fixLink(%q) = %q, %v", in, got, ok)
		}
	}
}
