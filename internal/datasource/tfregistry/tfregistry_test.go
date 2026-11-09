package tfregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Response shapes trimmed from registry.terraform.io (2026-11-04):
// /v1/providers/hashicorp/random/versions, /v1/providers/hashicorp/random/3.6.0,
// /v1/modules/terraform-aws-modules/vpc/aws/versions and /v1/modules/terraform-aws-modules/vpc/aws/5.0.0.
const (
	providerVersions = `{"id":"hashicorp/random","versions":[
{"version":"3.6.0","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]},
{"version":"3.6.1","protocols":["5.0"],"platforms":[{"os":"linux","arch":"amd64"}]},
{"version":"3.7.0-alpha1","protocols":["5.0"],"platforms":[]},
{"version":"3.7.2","protocols":["5.0"],"platforms":[{"os":"darwin","arch":"arm64"}]},
{"version":"3.7.1","protocols":["5.0"],"platforms":[]}],"warnings":null}`
	providerDoc = `{"id":"hashicorp/random/3.7.1","owner":"hashicorp","namespace":"hashicorp","name":"random","version":"3.7.1","tag":"v3.7.1",
"source":"https://github.com/hashicorp/terraform-provider-random","published_at":"2025-02-11T09:59:51Z","downloads":1}`
	moduleVersions = `{"modules":[{"source":"terraform-aws-modules/vpc/aws","versions":[
{"version":"5.0.0","root":{"providers":[{"name":"aws","namespace":"","source":"","version":"","aliases":null}],"dependencies":[]},"submodules":[],"deprecation":null},
{"version":"6.0.0","root":{"providers":[],"dependencies":[]},"submodules":[{"path":"modules/vpc-endpoints","providers":[],"dependencies":[]}],"deprecation":null}]}]}`
	moduleDoc = `{"id":"terraform-aws-modules/vpc/aws/5.0.0","namespace":"terraform-aws-modules","name":"vpc","version":"5.0.0","provider":"aws",
"source":"https://github.com/terraform-aws-modules/terraform-aws-vpc","tag":"v5.0.0","published_at":"2025-06-19T11:07:27.949303Z"}`
)

type registry struct {
	srv  *httptest.Server
	hits sync.Map // path -> *atomic.Int32
	auth atomic.Value
}

func (r *registry) count(p string) int32 {
	v, _ := r.hits.LoadOrStore(p, new(atomic.Int32))
	return v.(*atomic.Int32).Load()
}

// serve answers discovery with a relative providers.v1 and an absolute modules.v1, and the
// given documents by path.
func serve(t *testing.T, docs map[string]string) *registry {
	t.Helper()
	r := &registry{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		v, _ := r.hits.LoadOrStore(req.URL.Path, new(atomic.Int32))
		v.(*atomic.Int32).Add(1)
		r.auth.Store(req.Header.Get("Authorization"))
		if req.URL.Path == "/.well-known/terraform.json" {
			if body, ok := docs["discovery"]; ok {
				_, _ = w.Write([]byte(body))
				return
			}
			_, _ = w.Write([]byte(`{"providers.v1":"/v1/providers/","modules.v1":"` + r.srv.URL + `/api/modules"}`))
			return
		}
		body, ok := docs[req.URL.Path]
		switch {
		case !ok:
			http.NotFound(w, req)
		case strings.HasPrefix(body, "status:"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func newRegistry(r *registry, env map[string]string) *Registry {
	return New(r.srv.Client(), make(chan struct{}, 2), func(k string) string { return env[k] })
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	r := serve(t, map[string]string{
		"/v1/providers/hashicorp/random/versions":             providerVersions,
		"/api/modules/terraform-aws-modules/vpc/aws/versions": moduleVersions,
	})
	host := strings.TrimPrefix(r.srv.URL, "http://")
	token := "TF_TOKEN_" + strings.NewReplacer(".", "_", ":", "_").Replace(strings.Split(host, ":")[0])
	reg := newRegistry(r, map[string]string{token: "secret"})
	providers, modules := reg.Providers(), reg.Modules()
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			rels, err := providers.Releases(ctx, dep.Dep{Name: "hashicorp/random", Source: r.srv.URL + "/"}, "")
			if err != nil || !slices.Equal(versions(rels), []string{"3.6.0", "3.6.1", "3.7.0-alpha1", "3.7.2", "3.7.1"}) {
				t.Errorf("providers = %v, %v", versions(rels), err)
			}
		})
	}
	wg.Wait()
	rels, err := modules.Releases(ctx, dep.Dep{Name: "terraform-aws-modules/vpc/aws", Source: r.srv.URL}, "")
	if err != nil || !slices.Equal(versions(rels), []string{"5.0.0", "6.0.0"}) {
		t.Errorf("modules = %v, %v", versions(rels), err)
	}
	if r.count("/.well-known/terraform.json") != 1 || r.count("/v1/providers/hashicorp/random/versions") != 1 {
		t.Errorf("discovery %d, versions %d requests", r.count("/.well-known/terraform.json"), r.count("/v1/providers/hashicorp/random/versions"))
	}
	if got := r.auth.Load(); got != "Bearer secret" {
		t.Errorf("Authorization = %v", got)
	}
	if _, err := providers.Digest(ctx, dep.Dep{Name: "hashicorp/random"}, "3.7.1"); err == nil || err.Error() != "terraform-provider: digests are not supported for hashicorp/random" {
		t.Errorf("Digest err = %v", err)
	}
}

func TestReleasesErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		docs map[string]string
		dep  string
		want string
	}{
		{"not found", nil, "x/y", "provider x/y not found in {}"},
		{"empty", map[string]string{"/v1/providers/x/y/versions": `{"versions":[]}`}, "x/y", "provider x/y not found in {}"},
		{"server error", map[string]string{"/v1/providers/x/y/versions": "status:500"}, "x/y", "GET {}/v1/providers/x/y/versions: HTTP 500"},
		{"malformed", map[string]string{"/v1/providers/x/y/versions": `{"versions":[`}, "x/y", "GET {}/v1/providers/x/y/versions: jsontext: unexpected EOF"},
		{"no discovery", map[string]string{"discovery": "status"}, "x/y", "GET {}/.well-known/terraform.json: jsontext: invalid character"},
		{"no service", map[string]string{"discovery": `{"modules.v1":"/m/"}`}, "x/y", "{} has no providers.v1 service"},
		{"bad service URL", map[string]string{"discovery": `{"providers.v1":"http://[::1"}`}, "x/y", "providers.v1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := serve(t, tt.docs)
			_, err := newRegistry(r, nil).Providers().Releases(context.Background(), dep.Dep{Name: tt.dep, Source: r.srv.URL}, "")
			want := strings.ReplaceAll(tt.want, "{}", r.srv.URL)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want %q", err, want)
			}
		})
	}
	r := httptest.NewServer(http.NotFoundHandler())
	defer r.Close()
	_, err := New(r.Client(), nil, func(string) string { return "" }).Modules().Releases(context.Background(), dep.Dep{Name: "a/b/c", Source: r.URL}, "")
	if err == nil || err.Error() != r.URL+" is not a Terraform registry (no service discovery)" {
		t.Errorf("err = %v", err)
	}
}

func TestReleaseTimeAndSource(t *testing.T) {
	r := serve(t, map[string]string{
		"/v1/providers/hashicorp/random/versions":             providerVersions,
		"/v1/providers/hashicorp/random/3.6.0":                strings.Replace(providerDoc, "2025-02-11T09:59:51Z", "2023-12-04T14:00:32Z", 1),
		"/v1/providers/hashicorp/random/3.7.1":                providerDoc,
		"/v1/providers/hashicorp/random/3.6.1":                `{"published_at":"yesterday"}`,
		"/v1/providers/hashicorp/random/3.7.2":                `{"source":"https://example.com/r"}`,
		"/api/modules/terraform-aws-modules/vpc/aws/versions": moduleVersions,
		"/api/modules/terraform-aws-modules/vpc/aws/5.0.0":    moduleDoc,
	})
	reg := newRegistry(r, nil)
	p, m := reg.Providers(), reg.Modules()
	ctx := context.Background()
	rnd := dep.Dep{Name: "hashicorp/random", Source: r.srv.URL}
	for _, tt := range []struct {
		src     *Source
		d       dep.Dep
		version string
		want    string
		repo    string
	}{
		{p, rnd, "3.7.1", "2025-02-11T09:59:51Z", "https://github.com/hashicorp/terraform-provider-random"},
		{p, rnd, "v3.7.1", "2025-02-11T09:59:51Z", "https://github.com/hashicorp/terraform-provider-random"},
		{p, rnd, "3.6", "2023-12-04T14:00:32Z", "https://github.com/hashicorp/terraform-provider-random"}, // first of the 3.6 line
		{p, rnd, "3.7.2", "", "https://example.com/r"},                                                    // no published_at
		{p, rnd, "3.9", "", ""},          // no such line
		{p, rnd, "3.7.0-alpha1", "", ""}, // no document
		{m, dep.Dep{Name: "terraform-aws-modules/vpc/aws", Source: r.srv.URL}, "5.0.0", "2025-06-19T11:07:27.949303Z", "https://github.com/terraform-aws-modules/terraform-aws-vpc"},
	} {
		got, err := tt.src.ReleaseTime(ctx, tt.d, tt.version)
		if err != nil || (tt.want == "" && !got.IsZero()) || (tt.want != "" && got.Format(time.RFC3339Nano) != tt.want) {
			t.Errorf("ReleaseTime(%s) = %v, %v, want %s", tt.version, got, err, tt.want)
		}
		repo, err := tt.src.SourceRepo(ctx, tt.d, tt.version)
		if err != nil || repo != tt.repo {
			t.Errorf("SourceRepo(%s) = %q, %v", tt.version, repo, err)
		}
	}
	if _, err := p.ReleaseTime(ctx, rnd, "3.6.1"); err == nil || !strings.Contains(err.Error(), "published_at") {
		t.Errorf("bad time: %v", err)
	}
	if r.count("/v1/providers/hashicorp/random/3.7.1") != 1 {
		t.Errorf("version document fetched %d times", r.count("/v1/providers/hashicorp/random/3.7.1"))
	}
	missing := dep.Dep{Name: "x/y", Source: r.srv.URL}
	if _, err := p.ReleaseTime(ctx, missing, "1.0"); err == nil {
		t.Error("partial version of a missing provider: no error")
	}
	if _, err := p.SourceRepo(ctx, dep.Dep{Name: "x/y", Source: r.srv.URL + "/nope"}, "1.0.0"); err == nil {
		t.Error("missing registry: no error")
	}
}

func TestFirstOfLine(t *testing.T) {
	rels := []datasource.Release{{Version: "5.10.1"}, {Version: "5.2.0"}, {Version: "5.10.0"}, {Version: "5.10.0-rc1"}, {Version: "5.1x.0"}, {Version: "5..1"}, {Version: "50.0.0"}}
	for line, want := range map[string]string{"5.10": "5.10.0", "5": "5.2.0", "6": "", "5.1": ""} {
		if got := firstOfLine(rels, line); got != want {
			t.Errorf("firstOfLine(%s) = %q, want %q", line, got, want)
		}
	}
	if registryOf(dep.Dep{}) != DefaultRegistry || registryOf(dep.Dep{Source: "https://r"}) != "https://r" {
		t.Error("registryOf")
	}
}

func TestCancelled(t *testing.T) {
	r := serve(t, nil)
	reg := New(r.srv.Client(), make(chan struct{}), func(string) string { return "" }) // no free slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reg.Providers().Releases(ctx, dep.Dep{Name: "a/b", Source: r.srv.URL}, ""); err == nil {
		t.Error("no error")
	}
}
