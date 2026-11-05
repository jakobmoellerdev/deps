package npmds

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

const eslintDoc = `{
  "_id": "eslint",
  "versions": {"10.12.0": {"readme": "big", "dist": {"tarball": "x"}}, "10.14.0": {"deprecated": false}},
  "time": {"created": "2013-06-29T00:00:00.000Z", "10.12.0": "2026-09-01T10:00:00.000Z", "10.14.0": "2026-10-30T14:12:27.179Z", "unpublished": {"time": "x"}},
  "repository": {"type": "git", "url": "git+https://github.com/eslint/eslint.git"},
  "readme": "never read"
}`

func TestReleaseTimeAndSourceRepo(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, strings.Join(strings.Fields(r.Header.Get("Accept")+" "+r.Header.Get("Authorization")+" "+r.URL.EscapedPath()), " "))
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/corp/@corp%2flib", "/pub/eslint":
			io.WriteString(w, eslintDoc)
		case "/pub/strrepo":
			io.WriteString(w, `{"repository":"github:o/r","time":{"1.0.0":"not a time"}}`)
		case "/pub/bad":
			io.WriteString(w, `{"time":{"1.0.0":`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	root := write(t, t.TempDir(), map[string]string{
		".npmrc":       "@corp:registry=" + srv.URL + "/corp\n//" + strings.TrimPrefix(srv.URL, "http://") + "/corp/:_authToken=tok\n",
		"package.json": "{}",
	})
	s := New(srv.Client(), make(chan struct{}, 1), env(map[string]string{"NPM_CONFIG_REGISTRY": srv.URL + "/pub"}))
	corp := dep.Dep{Name: "@corp/lib", File: "package.json"}
	if _, err := s.Releases(t.Context(), corp, root); err != nil {
		t.Fatal(err)
	}
	timer, repoer := s.(datasource.Timer), s.(datasource.SourceRepo)

	// The registry and token of the earlier Releases lookup are reused, and Releases,
	// ReleaseTime and SourceRepo share one packument request per package.
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			got, err := timer.ReleaseTime(t.Context(), corp, "10.14.0")
			if want := time.Date(2026, 10, 30, 14, 12, 27, 179e6, time.UTC); err != nil || !got.Equal(want) {
				t.Errorf("ReleaseTime = %v, %v; want %v", got, err, want)
			}
		})
	}
	wg.Wait()
	if got, err := repoer.SourceRepo(t.Context(), corp, "10.14.0"); err != nil || got != "git+https://github.com/eslint/eslint.git" {
		t.Errorf("SourceRepo = %q, %v", got, err)
	}
	if got, err := timer.ReleaseTime(t.Context(), corp, "9.9.9"); err != nil || !got.IsZero() {
		t.Errorf("unknown version = %v, %v; want zero", got, err)
	}

	// Without an earlier Releases lookup the environment's registry is used.
	if got, err := timer.ReleaseTime(t.Context(), dep.Dep{Name: "eslint", File: "x/package.json"}, "10.12.0"); err != nil || got.Format(time.DateOnly) != "2026-09-01" {
		t.Errorf("eslint ReleaseTime = %v, %v", got, err)
	}
	str := dep.Dep{Name: "strrepo", File: "package.json"}
	if got, err := repoer.SourceRepo(t.Context(), str, "1.0.0"); err != nil || got != "github:o/r" {
		t.Errorf("string repository = %q, %v", got, err)
	}
	if _, err := timer.ReleaseTime(t.Context(), str, "1.0.0"); err == nil || !strings.Contains(err.Error(), "package strrepo: time of 1.0.0") {
		t.Errorf("bad time err = %v", err)
	}
	if _, err := timer.ReleaseTime(t.Context(), dep.Dep{Name: "bad"}, "1.0.0"); err == nil || !strings.Contains(err.Error(), "malformed response") {
		t.Errorf("malformed err = %v", err)
	}
	if _, err := repoer.SourceRepo(t.Context(), dep.Dep{Name: "missing"}, "1.0.0"); err == nil || err.Error() != "package missing not found" {
		t.Errorf("missing err = %v", err)
	}

	want := []string{
		"application/json Bearer tok /corp/@corp%2flib",
		"application/json /pub/eslint",
		"application/json /pub/strrepo",
		"application/json /pub/bad",
		"application/json /pub/missing",
	}
	if !slices.Equal(reqs, want) {
		t.Errorf("requests =\n%q\nwant\n%q", reqs, want)
	}
}

func TestDecodePackument(t *testing.T) {
	tests := []struct {
		name, in, wantRepo, wantErr string
		wantVersions, wantTimes     int
	}{
		{name: "full", in: eslintDoc, wantRepo: "git+https://github.com/eslint/eslint.git", wantVersions: 2, wantTimes: 3},
		{name: "no members", in: `{"versions":{}}`},
		// encoding/json/v2 randomly says "cannot" or "unable to"; match the stable remainder.
		{name: "time not an object", in: `{"time":"x","repository":{"url":1}}`, wantErr: "unmarshal JSON number into Go string"},
		{name: "repository other kind", in: `{"repository":null,"time":{}}`},
		{name: "not an object", in: `[]`, wantErr: "expected {"},
		{name: "truncated", in: `{"versions":{"1.0.0":`, wantErr: "EOF"},
		{name: "truncated time", in: `{"time":{"a":`, wantErr: "EOF"},
		{name: "truncated repository", in: `{"repository":{`, wantErr: "EOF"},
		{name: "truncated after members", in: `{"versions":{},"time":{},"repository":"r",`, wantRepo: "r"},
		{name: "bad key", in: `{"time":{1:2}}`, wantErr: "must be a string"},
		{name: "empty", in: ``, wantErr: "EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, err := decodePackument(strings.NewReader(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || p.repo != tt.wantRepo || len(p.rels) != tt.wantVersions || len(p.times) != tt.wantTimes {
				t.Fatalf("decodePackument = %+v, %v", p, err)
			}
		})
	}
}

// hugeVersions streams `{"versions":{...n versions of ~1 KiB...},"time":{...},"repository":"r",
// "readme":"..."}` without ever holding the document; read counts the bytes handed out.
type hugeVersions struct {
	n, i  int
	buf   []byte
	read  int
	stage int
	tail  int // bytes handed out when the readme started
}

func (h *hugeVersions) Read(p []byte) (int, error) {
	for len(h.buf) == 0 {
		switch {
		case h.stage == 0:
			h.buf = []byte(`{"_id":"huge","versions":{`)
			h.stage++
		case h.stage == 1 && h.i < h.n:
			sep := ","
			if h.i == 0 {
				sep = ""
			}
			h.buf = fmt.Appendf(nil, `%s"1.0.%d":{"name":"huge","readme":"%s","dist":{"tarball":"https://r/huge-1.0.%d.tgz"}}`, sep, h.i, strings.Repeat("x", 1000), h.i)
			h.i++
		case h.stage == 1:
			h.buf = []byte(`},"time":{"1.0.0":"2026-10-30T00:00:00Z"},"repository":"r","readme":"`)
			h.stage++
			h.tail = h.read + len(h.buf)
		case h.stage == 2:
			h.buf = []byte(strings.Repeat("y", 64<<10))
		}
	}
	n := copy(p, h.buf)
	h.buf = h.buf[n:]
	h.read += n
	return n, nil
}

// TestDecodePackumentHugeVersions decodes a 64 MiB `versions` object and checks that it is
// streamed rather than materialized, and that decoding stops before the (endless) readme.
func TestDecodePackumentHugeVersions(t *testing.T) {
	h := &hugeVersions{n: 64 << 10}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	p, _, err := decodePackument(h)
	runtime.ReadMemStats(&after)
	if err != nil || len(p.rels) != h.n || p.repo != "r" || p.times["1.0.0"] != "2026-10-30T00:00:00Z" {
		t.Fatalf("decodePackument = %d versions, %q, %v, %v", len(p.rels), p.repo, p.times, err)
	}
	if h.stage != 2 || h.read < 64<<20 {
		t.Fatalf("read %d bytes (stage %d), want the whole versions object", h.read, h.stage)
	}
	if extra := h.read - h.tail; extra > 1<<20 {
		t.Errorf("read %d readme bytes: decoding did not stop after versions, time and repository", extra)
	}
	// The generator itself allocates ~1 KiB per version (~64 MiB in total), so measure the
	// decoder's own peak: live heap must stay far below the document size.
	if live := int64(after.HeapInuse) - int64(before.HeapInuse); live > 16<<20 {
		t.Errorf("heap grew by %d bytes decoding a 64 MiB document", live)
	}
}
