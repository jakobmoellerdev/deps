package azuretasks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// testdata/builtin.json and testdata/marketplace.json are entries of
// renovatebot/azure-devops-marketplace's lists (fetched 2026-11-05, versions cut to the newest).
func servePublic(t *testing.T, hits map[string]*atomic.Int32) *httptest.Server {
	t.Helper()
	files := map[string]string{builtinFile: "testdata/builtin.json", marketplaceFile: "testdata/marketplace.json"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := files[r.URL.Path]
		if !ok || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.NotFound(w, r)
			return
		}
		hits[r.URL.Path].Add(1)
		body, err := os.ReadFile(f)
		if err != nil {
			t.Error(err)
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(vals map[string]string) func(string) string { return func(k string) string { return vals[k] } }

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestPublicLists(t *testing.T) {
	hits := map[string]*atomic.Int32{builtinFile: {}, marketplaceFile: {}}
	srv := servePublic(t, hits)
	s := New(srv.Client(), make(chan struct{}, 1), env(map[string]string{"DEPS_AZURE_PIPELINES_TASKS_URL": srv.URL + "/"}))
	s.Plan([]dep.Dep{{Name: "Bash"}, {Name: "ReplaceTokens"}})
	for _, tt := range []struct {
		name string
		want []string
	}{
		{"Bash", []string{"3.278.0", "3.278.1", "3.279.0", "3.279.1", "3.282.0", "3.282.1", "3.278", "3", "3.279", "3.282"}},
		{"6c731c3c-3c68-459a-a5c9-bde6e6595b5b", []string{"3.279.1", "3.282.0", "3.282.1", "3.279", "3", "3.282"}},
		{"ReplaceTokens", []string{"6.2.0", "6.3.0", "6.3.1", "7.0.0", "6.2", "6", "6.3", "7.0", "7"}},
		// Not planned: the marketplace list was already scanned for the planned tasks only.
		{"gittools.gittools.setup-gitversion-task.gitversion/setup", nil},
	} {
		rs, err := s.Releases(t.Context(), dep.Dep{Name: tt.name}, "")
		if tt.want == nil {
			if err == nil || !strings.Contains(err.Error(), "not found") {
				t.Errorf("%s: err = %v, want not found", tt.name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := versions(rs); !slices.Equal(got, tt.want) {
			t.Errorf("%s: releases = %v\nwant %v", tt.name, got, tt.want)
		}
	}
	if hits[builtinFile].Load() != 1 || hits[marketplaceFile].Load() != 1 {
		t.Errorf("requests = %d builtin, %d marketplace; want 1 each", hits[builtinFile].Load(), hits[marketplaceFile].Load())
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "Bash"}, "3"); err == nil {
		t.Error("Digest: want error")
	}
}

func TestPublicListErrors(t *testing.T) {
	for body, want := range map[string]string{
		`[]`:           "not an object",
		`{"bash": 1}`:  "bash",
		`{"bash": [`:   "EOF",
		`{"x": [], 1}`: "member name must be a string",
		`{"x": {"a"`:   "EOF",
		`not json`:     "invalid character",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == builtinFile {
				w.Write([]byte(`{}`))
				return
			}
			w.Write([]byte(body))
		}))
		s := New(srv.Client(), nil, env(map[string]string{"DEPS_AZURE_PIPELINES_TASKS_URL": srv.URL}))
		s.Plan([]dep.Dep{{Name: "bash"}})
		_, err := s.Releases(t.Context(), dep.Dep{Name: "bash"}, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", body, err, want)
		}
		srv.Close()
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer down.Close()
	s := New(down.Client(), nil, env(map[string]string{"DEPS_AZURE_PIPELINES_TASKS_URL": down.URL}))
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "bash"}, ""); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("err = %v", err)
	}
	if New(http.DefaultClient, nil, env(nil)).base != DefaultURL {
		t.Error("default base")
	}
}

// orgTasks is a trimmed GET https://dev.azure.com/<org>/_apis/distributedtask/tasks/ response
// (the shape of Renovate's fixture): a built-in task in two versions, an extension task, and a
// task without version.
const orgTasks = `{"count": 4, "value": [
{"id": "e213ff0f-5d5c-4791-802d-52ea3e7be1f1", "name": "PowerShell", "version": {"major": 2, "minor": 247, "patch": 1, "isTest": false}, "serverOwned": true},
{"id": "e213ff0f-5d5c-4791-802d-52ea3e7be1f1", "name": "PowerShell", "version": {"major": 1, "minor": 2, "patch": 3, "isTest": false}, "serverOwned": true},
{"id": "e7a67fb8-8a05-4ffb-8a56-1a1ed0e5e46b", "name": "TerraformInstaller", "contributionIdentifier": "ms-devlabs.custom-terraform-tasks", "version": {"major": 0, "minor": 1, "patch": 15}},
{"id": "00000000-0000-0000-0000-000000000000", "name": "NoVersion", "version": null}
]}`

func TestOrganization(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if u, p, ok := r.BasicAuth(); !ok || u != "deps" || p != "pat" || r.URL.Path != "/org/_apis/distributedtask/tasks/" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(orgTasks))
	}))
	defer srv.Close()
	s := New(srv.Client(), nil, env(map[string]string{"DEPS_AZURE_DEVOPS_URL": srv.URL + "/org/", "DEPS_AZURE_DEVOPS_TOKEN": "pat"}))
	for name, want := range map[string][]string{
		"powershell":                           {"2.247.1", "1.2.3", "2.247", "2", "1.2", "1"},
		"E213FF0F-5D5C-4791-802D-52EA3E7BE1F1": {"2.247.1", "1.2.3", "2.247", "2", "1.2", "1"},
		"ms-devlabs.custom-terraform-tasks.TerraformInstaller":                   {"0.1.15", "0.1", "0"},
		"ms-devlabs.custom-terraform-tasks.e7a67fb8-8a05-4ffb-8a56-1a1ed0e5e46b": {"0.1.15", "0.1", "0"},
	} {
		rs, err := s.Releases(t.Context(), dep.Dep{Name: name}, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := versions(rs); !slices.Equal(got, want) {
			t.Errorf("%s: releases = %v, want %v", name, got, want)
		}
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "NoVersion"}, ""); err == nil || !strings.Contains(err.Error(), "not found in") {
		t.Errorf("NoVersion: err = %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hits.Load())
	}

	// A token is required for the organization; a wrong one fails.
	if s := New(srv.Client(), nil, env(map[string]string{"DEPS_AZURE_DEVOPS_URL": srv.URL + "/org"})); s.org != "" {
		t.Error("organization without token")
	}
	bad := New(srv.Client(), nil, env(map[string]string{"DEPS_AZURE_DEVOPS_URL": srv.URL + "/org", "DEPS_AZURE_DEVOPS_TOKEN": "wrong"}))
	if _, err := bad.Releases(t.Context(), dep.Dep{Name: "PowerShell"}, ""); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("wrong token: err = %v", err)
	}
	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"value": [`)) }))
	defer malformed.Close()
	m := New(malformed.Client(), nil, env(map[string]string{"DEPS_AZURE_DEVOPS_URL": malformed.URL, "DEPS_AZURE_DEVOPS_TOKEN": "pat"}))
	if _, err := m.Releases(t.Context(), dep.Dep{Name: "PowerShell"}, ""); err == nil || !strings.Contains(err.Error(), "malformed response") {
		t.Errorf("malformed: err = %v", err)
	}
	invalid := New(srv.Client(), nil, env(map[string]string{"DEPS_AZURE_DEVOPS_URL": "http://[::1", "DEPS_AZURE_DEVOPS_TOKEN": "pat"}))
	if _, err := invalid.Releases(t.Context(), dep.Dep{Name: "PowerShell"}, ""); err == nil {
		t.Error("invalid URL: want error")
	}
}

// A first caller whose context ended does not leave its error to the tasks looked up after it
// with a live context.
func TestPublicListsAfterCanceledCaller(t *testing.T) {
	hits := map[string]*atomic.Int32{builtinFile: {}, marketplaceFile: {}}
	srv := servePublic(t, hits)
	s := New(srv.Client(), make(chan struct{}, 1), env(map[string]string{"DEPS_AZURE_PIPELINES_TASKS_URL": srv.URL + "/"}))
	s.Plan([]dep.Dep{{Name: "Bash"}, {Name: "ReplaceTokens"}})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Releases(canceled, dep.Dep{Name: "Bash"}, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: err = %v, want context.Canceled", err)
	}
	for _, name := range []string{"Bash", "ReplaceTokens"} {
		if rs, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err != nil || len(rs) == 0 {
			t.Errorf("%s: %d releases, err %v", name, len(rs), err)
		}
	}
	if hits[builtinFile].Load() != 1 || hits[marketplaceFile].Load() != 1 {
		t.Errorf("requests = %d builtin, %d marketplace; want 1 each", hits[builtinFile].Load(), hits[marketplaceFile].Load())
	}
}

func TestReleasesDedupe(t *testing.T) {
	got := versions(releases([]string{"1.0.0", "1.0.0", "2", "1.0.0-rc1"}))
	if want := []string{"1.0.0", "2", "1.0.0-rc1", "1.0", "1"}; !slices.Equal(got, want) {
		t.Errorf("releases = %v, want %v", got, want)
	}
}
