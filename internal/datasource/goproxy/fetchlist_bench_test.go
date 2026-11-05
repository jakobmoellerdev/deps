package goproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// BenchmarkFetchList700 parses a 700-version @v/list body (k8s.io/api scale) through fetch.
func BenchmarkFetchList700(b *testing.B) {
	var sb strings.Builder
	for i := range 700 {
		fmt.Fprintf(&sb, "v0.%d.%d\n", i/10, i%10)
	}
	body := sb.String()
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	s := New(client, make(chan struct{}, 1))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		v, _, err := s.fetch(ctx, "https://proxy.test/m/@v/list")
		if err != nil || len(v) != 700 {
			b.Fatal(len(v), err)
		}
	}
}
