package gittags

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// bigRepo serves a repository with branches branches and tags tags over protocol v2, honoring
// the ref-prefixes of an ls-refs request, and counts the requests and the bytes it writes.
func bigRepo(tb testing.TB, branches, tags int) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	tb.Helper()
	refs := []string{"1111111111111111111111111111111111111111 HEAD\n"}
	for i := range branches {
		refs = append(refs, fmt.Sprintf("%040d refs/heads/renovate/branch-%d\n", i, i))
	}
	for i := range tags {
		refs = append(refs, fmt.Sprintf("%040d refs/tags/v4.%d.0\n", i, i))
	}
	refs = append(refs, "2222222222222222222222222222222222222222 refs/tags/stable\n")
	var reqs, bytes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		write := func(s string) {
			bytes.Add(int64(len(s)))
			io.WriteString(w, s)
		}
		if r.Method == http.MethodGet {
			write(pkts(serviceHeader, "0000", "version 2\n", "ls-refs\n", "0000"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		prefixes := strings.Split(string(body), "ref-prefix ")[1:]
		var out []string
		for _, line := range refs {
			_, ref, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
			for _, p := range prefixes {
				prefix, _, _ := strings.Cut(p, "\n")
				if strings.HasPrefix(ref, prefix) {
					out = append(out, line)
					break
				}
			}
		}
		write(pkts(append(out, "0000")...))
	}))
	tb.Cleanup(srv.Close)
	return srv, &reqs, &bytes
}

// benchDigest reports the requests and response bytes one digest lookup of ref costs on a
// repository with 3000 branches (what bots and pull requests leave behind) and 50 tags.
func benchDigest(b *testing.B, ref string) {
	srv, reqs, bytes := bigRepo(b, 3000, 50)
	d := dep.Dep{Source: srv.URL + "/a/b"}
	ctx := context.Background()
	n := 0
	reqs.Store(0)
	bytes.Store(0)
	for b.Loop() {
		c, err := New(srv.Client(), nil, nil, nil).Refs().Digest(ctx, d, ref)
		if err != nil || c == "" {
			b.Fatalf("Digest(%s) = %q, %v", ref, c, err)
		}
		n++
	}
	b.ReportMetric(float64(reqs.Load())/float64(n), "reqs/op")
	b.ReportMetric(float64(bytes.Load())/float64(n), "respbytes/op")
}

// BenchmarkRefsDigestBranch is an action or module pinned to a branch (`@main`), the usual
// git-refs dep.
func BenchmarkRefsDigestBranch(b *testing.B) { benchDigest(b, "renovate/branch-7") }

// BenchmarkRefsDigestTag is a ref that looks like a branch but is a tag (`@stable`).
func BenchmarkRefsDigestTag(b *testing.B) { benchDigest(b, "stable") }

// TestRefsDigestSharedListing pins that a branch listing another dep already shared still
// answers without a request, with the same commits as a lookup of its own.
func TestRefsDigestSharedListing(t *testing.T) {
	srv, reqs, _ := bigRepo(t, 20, 5)
	l := New(srv.Client(), nil, nil, nil)
	d := dep.Dep{Source: srv.URL + "/a/b", Versioning: "nixpkgs"}
	if _, err := l.Refs().Releases(t.Context(), d, ""); err != nil { // lists the branches
		t.Fatal(err)
	}
	before := reqs.Load()
	for _, tt := range []struct{ ref, want string }{
		{"renovate/branch-3", fmt.Sprintf("%040d", 3)},
		{"HEAD", "1111111111111111111111111111111111111111"},
	} {
		if got, err := l.Refs().Digest(t.Context(), d, tt.ref); err != nil || got != tt.want {
			t.Errorf("Digest(%s) = %q, %v; want %q", tt.ref, got, err, tt.want)
		}
	}
	if n := reqs.Load() - before; n != 0 {
		t.Errorf("digests from the shared branch listing cost %d requests, want 0", n)
	}
	// A tag is not in that listing: it costs the tag listing, as before.
	if got, err := l.Refs().Digest(t.Context(), d, "stable"); err != nil || got != "2222222222222222222222222222222222222222" {
		t.Errorf("Digest(stable) = %q, %v; want the tag", got, err)
	}
}
