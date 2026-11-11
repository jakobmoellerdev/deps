package nuget

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// TestLookupFeedsConcurrent pins that a dep's feeds are queried together: every service index
// blocks until each feed's request has arrived, which sequential queries never reach. The
// merge keeps feed order, and a one-slot semaphore must still complete (no request holds a
// slot while waiting for another).
func TestLookupFeedsConcurrent(t *testing.T) {
	const feeds = 3
	var (
		mu      sync.Mutex
		barrier bool
		arrived sync.WaitGroup
		all     = make(chan struct{})
	)
	arrived.Add(feeds)
	go func() { arrived.Wait(); close(all) }()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feed, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if rest != "index.json" { // the flat container's listing of the package
			fmt.Fprintf(w, `{"versions":["1.0.0","9.0.%s"]}`, feed)
			return
		}
		mu.Lock()
		wait := barrier
		mu.Unlock()
		if wait {
			arrived.Done()
			select {
			case <-all:
			case <-time.After(10 * time.Second):
				http.Error(w, "feeds queried sequentially", http.StatusTeapot)
				return
			}
		}
		fmt.Fprintf(w, `{"version":"3.0.0","resources":[{"@id":"%s/%s/flat/","@type":"PackageBaseAddress/3.0.0"}]}`, srv.URL, feed)
	}))
	t.Cleanup(srv.Close)
	d := dep.Dep{Name: "Internal.Lib", Source: srv.URL + "/c/index.json " + srv.URL + "/a/index.json " + srv.URL + "/b/index.json"}
	want := []string{"1.0.0", "9.0.c", "9.0.a", "9.0.b"}

	rels, err := New(srv.Client(), make(chan struct{}, 1), func(string) string { return "" }).Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(rels); !slices.Equal(got, want) {
		t.Errorf("one slot: merged = %v, want %v", got, want)
	}

	mu.Lock()
	barrier = true
	mu.Unlock()
	rels, err = New(srv.Client(), nil, func(string) string { return "" }).Releases(t.Context(), d, "")
	if err != nil {
		t.Fatalf("feeds queried sequentially: %v", err)
	}
	if got := versionsOf(rels); !slices.Equal(got, want) {
		t.Errorf("merged = %v, want %v", got, want)
	}
}
