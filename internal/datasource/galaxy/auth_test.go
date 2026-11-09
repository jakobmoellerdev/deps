package galaxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// hub is a private Automation Hub: every API request needs `Authorization: want`; /sso
// exchanges the offline token `offline` for the access token `access` (counting exchanges).
func hub(t *testing.T, want string, exchanges *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sso" {
			exchanges.Add(1)
			if r.Method != http.MethodPost || r.FormValue("grant_type") != "refresh_token" || r.FormValue("client_id") != "cloud-services" {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			switch r.FormValue("refresh_token") {
			case "offline":
				fmt.Fprint(w, `{"access_token":"access","expires_in":900,"token_type":"Bearer"}`)
			case "empty":
				fmt.Fprint(w, `{}`)
			default:
				http.Error(w, "invalid_grant", http.StatusBadRequest)
			}
			return
		}
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.RequestURI() {
		case "/api/automation-hub/content/published/":
			fmt.Fprint(w, root)
		case "/api/automation-hub/content/published/v3/collections/redhat/rhel_system_roles/":
			fmt.Fprint(w, `{"deprecated":false}`)
		case "/api/automation-hub/content/published/v3/collections/redhat/rhel_system_roles/versions/?limit=100":
			fmt.Fprint(w, `{"data":[{"version":"1.80.0","created_at":"2024-05-20T00:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestCollectionsAuth(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		env        func(base string) map[string]string
		exchanges  int32
		err        string
	}{
		{"DEPS_GALAXY_TOKEN", "Token s3cret", func(string) map[string]string {
			return map[string]string{"DEPS_GALAXY_TOKEN": "s3cret"}
		}, 0, ""},
		{"DEPS_GALAXY_AUTH_URL exchange", "Bearer access", func(base string) map[string]string {
			return map[string]string{"DEPS_GALAXY_TOKEN": "offline", "DEPS_GALAXY_AUTH_URL": base + "/sso"}
		}, 1, ""},
		{"ansible-galaxy server list", "Bearer access", func(base string) map[string]string {
			return map[string]string{
				"ANSIBLE_GALAXY_SERVER_LIST":                    "galaxy, automation_hub,",
				"ANSIBLE_GALAXY_SERVER_GALAXY_URL":              "https://galaxy.ansible.com/",
				"ANSIBLE_GALAXY_SERVER_AUTOMATION_HUB_URL":      base + "/api/automation-hub/content/published/",
				"ANSIBLE_GALAXY_SERVER_AUTOMATION_HUB_TOKEN":    "offline",
				"ANSIBLE_GALAXY_SERVER_AUTOMATION_HUB_AUTH_URL": base + "/sso",
			}
		}, 1, ""},
		{"rejected offline token", "Bearer access", func(base string) map[string]string {
			return map[string]string{"DEPS_GALAXY_TOKEN": "expired", "DEPS_GALAXY_AUTH_URL": base + "/sso"}
		}, 1, "token exchange: HTTP 400"},
		{"no access token", "Bearer access", func(base string) map[string]string {
			return map[string]string{"DEPS_GALAXY_TOKEN": "empty", "DEPS_GALAXY_AUTH_URL": base + "/sso"}
		}, 1, "no access_token"},
		{"invalid SSO URL", "Bearer access", func(string) map[string]string {
			return map[string]string{"DEPS_GALAXY_TOKEN": "offline", "DEPS_GALAXY_AUTH_URL": "http://[::1]:namedport/sso"}
		}, 0, "invalid URL"},
		{"no token", "Token s3cret", func(string) map[string]string { return nil }, 0, "HTTP 401"},
	} {
		var exchanges atomic.Int32
		base := hub(t, tt.want, &exchanges)
		env := tt.env(base)
		ds := NewCollections(http.DefaultClient, nil, func(k string) string { return env[k] })
		d := dep.Dep{Name: "redhat.rhel_system_roles", Source: base + "/api/automation-hub/content/published/"}
		if _, ok := env["ANSIBLE_GALAXY_SERVER_LIST"]; !ok {
			d.Source = ""
			ds.base = base + "/api/automation-hub/content/published"
			ds.auths = serverAuths(func(k string) string { return env[k] }, ds.base)
		}
		rels, err := ds.Releases(t.Context(), d, "")
		switch {
		case tt.err != "":
			if err == nil || !strings.Contains(err.Error(), tt.err) || strings.Contains(err.Error(), "offline") {
				t.Errorf("%s: err = %v, want %q", tt.name, err, tt.err)
			}
		case err != nil || len(rels) != 1:
			t.Errorf("%s: %v, %v", tt.name, rels, err)
		}
		// Three requests share one exchange.
		if n := exchanges.Load(); n != tt.exchanges {
			t.Errorf("%s: %d exchanges, want %d", tt.name, n, tt.exchanges)
		}
	}
}

func TestServerAuthsSkipsIncomplete(t *testing.T) {
	env := map[string]string{
		"ANSIBLE_GALAXY_SERVER_LIST":    "a,b",
		"ANSIBLE_GALAXY_SERVER_A_URL":   "https://a.example/",
		"ANSIBLE_GALAXY_SERVER_B_TOKEN": "t",
		"ANSIBLE_GALAXY_SERVER_B_URL":   "::",
	}
	if got := serverAuths(func(k string) string { return env[k] }, DefaultURL); len(got) != 0 {
		t.Errorf("auths = %+v", got)
	}
}
