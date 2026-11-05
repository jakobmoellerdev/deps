package tfregistry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// awsShapedVersions is a provider /versions document shaped like hashicorp/aws: 700
// versions, each with protocols and 12 platforms.
func awsShapedVersions() string {
	var b strings.Builder
	b.WriteString(`{"id":"hashicorp/aws","versions":[`)
	for i := range 700 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"version":"%d.%d.%d","protocols":["5.0"],"platforms":[`, i/100, i/10%10, i%10)
		for j, p := range []string{"darwin", "freebsd", "linux", "openbsd", "solaris", "windows"} {
			for k, a := range []string{"amd64", "arm64"} {
				if j+k > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"os":%q,"arch":%q}`, p, a)
			}
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`],"warnings":null}`)
	return b.String()
}

func BenchmarkTfregistryFetchVersions(b *testing.B) {
	doc := awsShapedVersions()
	r := &registry{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(doc)) }))
	b.Cleanup(r.srv.Close)
	s := newRegistry(r, nil).Providers()
	ctx := context.Background()
	d := dep.Dep{Name: "hashicorp/aws", Source: r.srv.URL}
	base := r.srv.URL + "/v1/providers/"
	b.ReportAllocs()
	for b.Loop() {
		rels, err := s.fetchVersions(ctx, d, base)
		if err != nil || len(rels) != 700 {
			b.Fatal(len(rels), err)
		}
	}
}
