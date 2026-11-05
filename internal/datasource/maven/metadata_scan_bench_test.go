package mavends

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
)

// metadataScanBenchRT serves body for every request without a network round trip, so
// the benchmark measures reading the document.
type metadataScanBenchRT []byte

func (b metadataScanBenchRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
}

func BenchmarkFetchMetadata(b *testing.B) {
	for _, f := range []string{"guava-maven-metadata.xml", "kotlin-jvm-plugin-maven-metadata.xml"} {
		body, err := os.ReadFile("testdata/" + f)
		if err != nil {
			b.Fatal(err)
		}
		s := New(&http.Client{Transport: metadataScanBenchRT(body)}, nil, noenv)
		b.Run(f, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := s.fetchMetadata(context.Background(), "https://repo.example/"+f, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
