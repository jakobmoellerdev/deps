package osv

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// pysecShaped builds a PYSEC-like advisory: a PyPI package whose affected entry lists every
// release ever published (n versions) next to one ECOSYSTEM range.
func pysecShaped(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"id":"PYSEC-2021-1","summary":"s","details":"` + strings.Repeat("d", 512) + `",` +
		`"aliases":["CVE-2021-1","GHSA-aaaa-bbbb-cccc"],"references":[{"type":"WEB","url":"https://example.com/a"}],` +
		`"affected":[{"package":{"ecosystem":"PyPI","name":"django"},` +
		`"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"3.2.4"}]}],"versions":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"%d.%d.%d"`, i/100, (i/10)%10, i%10)
	}
	b.WriteString(`]}]}`)
	return []byte(b.String())
}

func BenchmarkLazyVersionsDecodePYSEC(b *testing.B) {
	data := pysecShaped(2000)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		var v vuln
		if err := json.Unmarshal(data, &v); err != nil {
			b.Fatal(err)
		}
	}
}

// A GitHub Actions advisory whose "versions" holds non-strings still decodes and is matched
// by its ranges alone.
func TestLazyVersionsNonStringVersionsMatchByRange(t *testing.T) {
	data := []byte(`{"id":"GHSA-x","affected":[{"package":{"ecosystem":"GitHub Actions","name":"o/r"},` +
		`"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"4.0.0"},{"fixed":"4.2.0"}]}],"versions":[1,2]}]}`)
	var v vuln
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	tag, ok := versioning.ParseTag("v4.1.3")
	if !ok {
		t.Fatal("ParseTag failed")
	}
	tg := &target{tag: tag, cmp: [3]int{4, 1, 3}}
	if !tg.affectedBy(v.Affected) {
		t.Error("v4.1.3 inside [4.0.0, 4.2.0) not affected")
	}
	tg = &target{tag: tag, cmp: [3]int{4, 2, 0}}
	if tg.affectedBy(v.Affected) {
		t.Error("v4.2.0 (fixed) reported affected")
	}
}
