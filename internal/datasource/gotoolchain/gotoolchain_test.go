package gotoolchain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func TestReleases(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		want       []string
		err        string
	}{
		{
			name: "stable and derived minors",
			body: `[{"version":"go1.27.1","stable":true,"files":[]},{"version":"go1.28rc1","stable":false},
				{"version":"go1.27.0","stable":true},{"version":"go1.21rc2","stable":false},{"version":"go1.20.1","stable":true},{"version":"go1.20","stable":true},
				{"version":"bogus","stable":true}]`,
			status: 200,
			want:   []string{"1.27.1", "1.27", "1.27.0", "1.20.1", "1.20"},
		},
		{name: "http error", status: 500, err: "HTTP 500"},
		{name: "not json", body: "<html>", status: 200, err: "invalid character"},
		{name: "bad element", body: `[{"version":1}]`, status: 200, err: "cannot unmarshal"},
		{name: "empty", body: `[]`, status: 200, err: "no stable releases"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.URL.Query().Get("mode") != "json" || r.URL.Query().Get("include") != "all" {
					t.Errorf("query %q", r.URL.RawQuery)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			s := New(srv.Client(), make(chan struct{}, 1)).(*Source)
			s.url = srv.URL + "/dl/?mode=json&include=all"
			for range 2 {
				rs, err := s.Releases(context.Background(), dep.Dep{Name: "go"}, "")
				if tt.err != "" {
					if err == nil || !strings.Contains(err.Error(), tt.err) {
						t.Fatalf("err = %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				for _, r := range rs {
					got = append(got, r.Version)
				}
				if !slices.Equal(got, tt.want) {
					t.Errorf("got %v", got)
				}
			}
			if hits.Load() != 1 {
				t.Errorf("hits = %d", hits.Load())
			}
		})
	}
}

func TestErrors(t *testing.T) {
	s := New(http.DefaultClient, nil).(*Source)
	if _, err := s.Digest(context.Background(), dep.Dep{Name: "go"}, "1.27"); err == nil {
		t.Error("digest should be unsupported")
	}
	s.url = "http://127.0.0.1:1/"
	if _, err := s.Releases(context.Background(), dep.Dep{}, ""); err == nil {
		t.Error("want connect error")
	}
	s = New(http.DefaultClient, nil).(*Source)
	s.url = "://bad"
	if _, err := s.Releases(context.Background(), dep.Dep{}, ""); err == nil {
		t.Error("want url error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s = New(http.DefaultClient, make(chan struct{})).(*Source)
	if _, err := s.Releases(ctx, dep.Dep{}, ""); err == nil {
		t.Error("want ctx error")
	}
}
