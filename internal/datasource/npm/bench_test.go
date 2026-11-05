package npmds

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// syntheticVersion appends one version object shaped like a registry's (typescript): many
// dependencies, dist with signatures, and in the full document the per-version metadata.
func syntheticVersion(b *strings.Builder, i int, full bool) {
	v := fmt.Sprintf("5.%d.%d", i/100, i%100)
	fmt.Fprintf(b, `"%s":{"name":"typescript","version":"%s","devDependencies":{`, v, v)
	for d := range 30 {
		if d > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(b, `"@types/dep-%d":"^%d.%d.0"`, d, d, i%7)
	}
	b.WriteString(`},"bin":{"tsc":"bin/tsc","tsserver":"bin/tsserver"},"engines":{"node":">=14.17"},`)
	b.WriteString(`"directories":{},"_hasShrinkwrap":false,`)
	fmt.Fprintf(b, `"dist":{"shasum":"%040d","tarball":"https://registry.npmjs.org/typescript/-/typescript-%s.tgz",`, i, v)
	fmt.Fprintf(b, `"fileCount":178,"integrity":"sha512-%086d==","signatures":[{"sig":"MEUCIQ%060d","keyid":"SHA256:jl3bwswu80PjjokCgh0o2w5c2U4LhQAE57gj9cz1kzA"}],"unpackedSize":%d}`, i, i, 30000000+i)
	if full {
		fmt.Fprintf(b, `,"keywords":["TypeScript","Microsoft","compiler"],"author":{"name":"Microsoft Corp."},"_id":"typescript@%s",`, v)
		b.WriteString(`"maintainers":[{"name":"typescript-bot","email":"typescript@microsoft.com"},{"name":"weswigham","email":"wwigham@gmail.com"}],`)
		b.WriteString(`"repository":{"type":"git","url":"git+https://github.com/microsoft/TypeScript.git"},"scripts":{"test":"hereby runtests-parallel","build":"npm run build:compiler"},`)
		b.WriteString(`"description":"TypeScript is a language for application scale JavaScript development","_npmUser":{"name":"typescript-bot","email":"typescript@microsoft.com"}`)
	}
	if i%10 == 9 {
		b.WriteString(`,"deprecated":"this version has been deprecated"`)
	}
	b.WriteByte('}')
}

// syntheticPackument returns a packument with n versions: the abbreviated metadata document,
// or the full one with `time`, `repository` and a readme after the versions.
func syntheticPackument(n int, full bool) []byte {
	var b strings.Builder
	b.WriteString(`{"_id":"typescript","name":"typescript","dist-tags":{"latest":"5.9.3","next":"6.0.0-dev"},"versions":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		syntheticVersion(&b, i, full)
	}
	b.WriteString(`}`)
	if !full {
		b.WriteString(`,"modified":"2026-11-04T07:12:45.148Z"}`)
		return []byte(b.String())
	}
	b.WriteString(`,"time":{"created":"2012-10-01T15:00:00.000Z"`)
	for i := range n {
		fmt.Fprintf(&b, `,"5.%d.%d":"2020-01-01T00:00:%02d.000Z"`, i/100, i%100, i%60)
	}
	b.WriteString(`},"repository":{"type":"git","url":"git+https://github.com/microsoft/TypeScript.git"},`)
	fmt.Fprintf(&b, `"readme":%q}`, strings.Repeat("TypeScript is a language. ", 2000))
	return []byte(b.String())
}

func BenchmarkDecodeVersions(b *testing.B) {
	data := syntheticPackument(3000, false)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, _, err := decodeVersions(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeDoc(b *testing.B) {
	data := syntheticPackument(3000, true)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := decodeDoc(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
