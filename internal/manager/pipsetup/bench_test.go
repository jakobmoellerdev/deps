package pipsetup

import (
	"os"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// BenchmarkExtract extracts the setup.py fixtures and one with a module docstring and an
// inline triple-quoted long_description, as many setup.py files carry, once per op.
func BenchmarkExtract(b *testing.B) {
	var srcs [][]byte
	for _, f := range []string{"testdata/renovate-setup.py", "testdata/black-22.1.0.py"} {
		src, err := os.ReadFile(f)
		if err != nil {
			b.Fatal(err)
		}
		srcs = append(srcs, src)
	}
	para := strings.Repeat("A library that does things, with \"quotes\" and 'apostrophes'.\n", 40)
	srcs = append(srcs, []byte(`"""`+para+`"""
from setuptools import setup

setup(
    name="x",
    long_description='''`+para+`''',
    install_requires=["requests>=2.31", "click~=8.0"],
)
`))
	m := New()
	b.ReportAllocs()
	for b.Loop() {
		for _, src := range srcs {
			if _, err := m.Extract("setup.py", src, manager.Env{}); err != nil {
				b.Fatal(err)
			}
		}
	}
}
