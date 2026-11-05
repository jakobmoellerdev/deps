package crate

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// BenchmarkFetch500 decodes a 500-release index file and derives its release lines.
func BenchmarkFetch500(b *testing.B) {
	line, _ := os.ReadFile("testdata/serde")
	first := line[:bytes.IndexByte(line, '\n')]
	var body bytes.Buffer
	for i := range 500 {
		v := fmt.Sprintf("%d.%d.%d", i/100, (i/10)%10, i%10)
		body.Write(bytes.Replace(first, []byte(`"0.9.0-rc4"`), []byte(`"`+v+`"`), 1))
		body.WriteByte('\n')
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body.Bytes()) }))
	defer srv.Close()
	s := New(srv.Client(), make(chan struct{}, 1), nil, func(k string) string {
		switch k {
		case "DEPS_CRATES_INDEX_URL":
			return srv.URL + "/index/"
		case "CARGO_HOME":
			return os.TempDir() + "/none"
		}
		return ""
	})
	r := s.locate(dep.Dep{Name: "serde"})
	b.ReportAllocs()
	for b.Loop() {
		if err := s.fetch(context.Background(), r, "serde", &entry{}); err != nil {
			b.Fatal(err)
		}
	}
}
