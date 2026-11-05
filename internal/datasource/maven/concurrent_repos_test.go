package mavends

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// TestReleasesReposConcurrent pins that an artifact's repositories are fetched together: each
// handler blocks until every repository's request has arrived, which sequential fetching never
// reaches. The result keeps repository order. A one-slot semaphore must still complete (no
// request holds a slot while waiting for another).
func TestReleasesReposConcurrent(t *testing.T) {
	const repos = 3
	var (
		mu      sync.Mutex
		barrier bool
		arrived sync.WaitGroup
		all     = make(chan struct{})
	)
	arrived.Add(repos)
	go func() { arrived.Wait(); close(all) }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		wait := barrier
		mu.Unlock()
		if wait {
			arrived.Done()
			select {
			case <-all:
			case <-time.After(10 * time.Second):
				http.Error(w, "repositories fetched sequentially", http.StatusTeapot)
				return
			}
		}
		repo, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		_, _ = w.Write([]byte(`<metadata><versioning><versions><version>` + repo + `</version><version>1.0</version></versions></versioning></metadata>`))
	}))
	t.Cleanup(srv.Close)
	d := dep.Dep{Name: "org.example:lib", Source: srv.URL + "/c " + srv.URL + "/a " + srv.URL + "/b"}
	want := []string{"c", "1.0", "a", "b"}

	rels, err := New(srv.Client(), make(chan struct{}, 1), noenv).Releases(context.Background(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versions(rels); !slices.Equal(got, want) {
		t.Errorf("one slot: merged = %v, want %v", got, want)
	}

	mu.Lock()
	barrier = true
	mu.Unlock()
	rels, err = New(srv.Client(), nil, noenv).Releases(context.Background(), d, "")
	if err != nil {
		t.Fatalf("repositories fetched sequentially: %v", err)
	}
	if got := versions(rels); !slices.Equal(got, want) {
		t.Errorf("merged = %v, want %v", got, want)
	}
}
