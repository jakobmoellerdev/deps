package gotoolchain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		{name: "bad element", body: `[{"version":1}]`, status: 200, err: "unmarshal JSON number"}, // json/v2 words the prefix randomly
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

func TestReleaseTime(t *testing.T) {
	const history = `var Releases = []*Release{
	{
		Date: Date{2026, 10, 19}, Version: Version{1, 27, 3},
		Security: &FixSummary{Packages: []string{"crypto/x509"}},
	},
	{
		Date: Date{2026, 8, 19}, Version: Version{1, 27, 0},
	},
	{
		Date:    Date{2023, 2, 1},
		Version: Version{1, 20, 0},
	},
}`
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name, body string
		status     int
		want       map[string]time.Time
		err        string
	}{
		{name: "dates", body: history, status: 200, want: map[string]time.Time{
			"1.27.3": day(2026, 10, 19), "1.27.0": day(2026, 8, 19), "1.27": day(2026, 8, 19), // derived minor: X.Y.0
			"1.20": day(2023, 2, 1), "1.27.1": {}, // pre-1.21 names; a missing version is unknown
		}},
		{name: "http error", status: 404, err: "HTTP 404"},
		{name: "no entries", body: "package history", status: 200, err: "no releases"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			s := New(srv.Client(), make(chan struct{}, 1)).(*Source)
			s.historyURL = srv.URL
			if tt.err != "" {
				if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "go"}, "1.27.0"); err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			for v, want := range tt.want {
				if got, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "go"}, v); err != nil || !got.Equal(want) {
					t.Errorf("ReleaseTime(%s) = %v, %v; want %v", v, got, err, want)
				}
			}
			if hits.Load() != 1 {
				t.Errorf("hits = %d, want 1 (once per run)", hits.Load())
			}
		})
	}
}

func TestErrors(t *testing.T) {
	s := New(http.DefaultClient, nil).(*Source)
	if _, err := s.Digest(context.Background(), dep.Dep{Name: "go"}, "1.27"); err == nil {
		t.Error("digest should be unsupported")
	}
}
