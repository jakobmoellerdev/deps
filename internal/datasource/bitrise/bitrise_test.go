package bitrise

import (
	"encoding/base64"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
)

// testdata holds bitrise-io/bitrise-steplib's contents API listing of steps/script and the
// step.yml of script 1.1.0 and 1.2.1 (fetched 2026-11-09).
func newSource(t *testing.T) (*Source, func() []string) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	file := func(name string) []byte {
		b, err := json.Marshal(map[string]string{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString(read(name))})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/bitrise-io/bitrise-steplib/contents/steps/script":
			w.Write(read("script-dir.json"))
		case "/repos/bitrise-io/bitrise-steplib/contents/steps/script/1.2.1/step.yml":
			w.Write(file("script-1.2.1-step.yml"))
		case "/repos/bitrise-io/bitrise-steplib/contents/steps/script/1.1.0/step.yml":
			w.Write(file("script-1.1.0-step.yml"))
		case "/repos/bitrise-io/bitrise-steplib/contents/steps/script/1.0.0/step.yml":
			w.Write([]byte(`{"type":"file","encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte("published_at: [\n")) + `"}`))
		case "/repos/acme/steplib/contents/steps/script":
			w.Write([]byte(`[{"name":"2.0.0-beta.1","type":"dir"},{"name":"README.md","type":"file"}]`))
		case "/repos/acme/steplib/contents/steps/empty":
			w.Write([]byte(`[{"name":"assets","type":"dir"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	gh := githubapi.New(srv.Client(), nil, make(chan struct{}, 2), githubapi.WithAPI(srv.URL, srv.URL+"/graphql"))
	return New(gh), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(hits)
	}
}

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	s, hits := newSource(t)
	d := dep.Dep{Name: "script"}
	rels, err := s.Releases(t.Context(), d, "")
	want := []string{"0.9.0", "0.9.1", "1.0.0", "1.1.0", "1.1.1", "1.1.2", "1.1.3", "1.1.4", "1.1.5", "1.1.6", "1.2.0", "1.2.1", "0.9", "0", "1.0", "1", "1.1", "1.2"}
	if err != nil || !slices.Equal(versions(rels), want) {
		t.Fatalf("Releases = %v, %v\nwant %v", versions(rels), err, want)
	}
	// The default steplib written out is the same library: one listing.
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "script", Source: DefaultSteplib}, ""); err != nil || len(hits()) != 1 {
		t.Errorf("second lookup: %v, hits %v", err, hits())
	}

	ctx := t.Context()
	if tm, err := s.ReleaseTime(ctx, d, "1.2.1"); err != nil || !tm.Equal(time.Date(2023, 9, 18, 13, 21, 55, 992180288, time.UTC)) {
		t.Errorf("ReleaseTime(1.2.1) = %v, %v", tm, err)
	}
	// 1.1 and 1.1.0 share the step.yml of 1.1.0, the line's first release.
	for _, v := range []string{"1.1", "1.1.0"} {
		if tm, err := s.ReleaseTime(ctx, d, v); err != nil || tm.UTC() != time.Date(2015, 10, 14, 14, 50, 36, 159715639, time.UTC) {
			t.Errorf("ReleaseTime(%s) = %v, %v", v, tm, err)
		}
	}
	if src, err := s.SourceRepo(ctx, d, "1.1.0"); err != nil || src != "https://github.com/bitrise-io/steps-script" {
		t.Errorf("SourceRepo = %q, %v", src, err)
	}
	if n := len(hits()); n != 3 {
		t.Errorf("requests = %v", hits())
	}
	if _, err := s.ReleaseTime(ctx, d, "1.0.0"); err == nil {
		t.Error("malformed step.yml: want error")
	}
	if _, err := s.ReleaseTime(ctx, d, "1.1.1"); err == nil {
		t.Error("missing step.yml: want error")
	}
	if _, err := s.Digest(ctx, d, "1.2.1"); err == nil {
		t.Error("Digest: want error")
	}
}

func TestReleasesCustomSteplib(t *testing.T) {
	s, _ := newSource(t)
	ctx := t.Context()
	rels, err := s.Releases(ctx, dep.Dep{Name: "script", Source: "https://github.com/acme/steplib.git"}, "")
	if err != nil || !slices.Equal(versions(rels), []string{"2.0.0-beta.1"}) {
		t.Errorf("Releases = %v, %v", versions(rels), err)
	}
	for _, d := range []dep.Dep{
		{Name: "empty", Source: "https://github.com/acme/steplib.git"},
		{Name: "missing"},
		{Name: "../x"},
		{Name: "script", Source: "ftp://example.com/lib"},
	} {
		if _, err := s.Releases(ctx, d, ""); err == nil {
			t.Errorf("%+v: want error", d)
		}
		if _, err := s.SourceRepo(ctx, d, "1.0.0"); err == nil {
			t.Errorf("%+v: SourceRepo: want error", d)
		}
	}
}
