package pypi

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// syntheticJSON returns a PyPI JSON API document shaped like botocore's: n releases with a
// wheel and an sdist each, every file with digests and the other metadata of the real API.
func syntheticJSON(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"info":{"name":"botocore","home_page":"https://github.com/boto/botocore","project_urls":{"Source":"https://github.com/boto/botocore"},"requires_python":">=3.9"},"last_serial":1,"releases":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		v := fmt.Sprintf("1.%d.%d", i/100, i%100)
		fmt.Fprintf(&b, `"%s":[`, v)
		for j, file := range []string{"botocore-" + v + "-py3-none-any.whl", "botocore-" + v + ".tar.gz"} {
			if j > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"comment_text":"","core-metadata":{"sha256":"%064x"},"data-dist-info-metadata":{"sha256":"%064x"},`, i, j)
			fmt.Fprintf(&b, `"digests":{"blake2b_256":"%064x","md5":"%032x","sha256":"%064x"},"downloads":-1,"filename":%q,`, i, i, j, file)
			fmt.Fprintf(&b, `"has_sig":false,"md5_digest":"%032x","packagetype":"bdist_wheel","python_version":"py3","requires_python":">= 3.9","size":%d,`, i, 13000000+i)
			fmt.Fprintf(&b, `"upload_time":"2025-01-%02dT20:21:%02d","upload_time_iso_8601":"2025-01-%02dT20:21:%02d.123456Z",`, 1+i%28, i%60, 1+i%28, i%60)
			fmt.Fprintf(&b, `"url":"https://files.pythonhosted.org/packages/ab/cd/%064x/%s","yanked":%t,"yanked_reason":null}`, i, file, i%50 == 49)
		}
		b.WriteByte(']')
	}
	b.WriteString(`},"urls":[],"vulnerabilities":[]}`)
	return []byte(b.String())
}

func BenchmarkDecodeJSON(b *testing.B) {
	data := syntheticJSON(2500)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := decodeJSON(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
